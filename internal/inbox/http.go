package inbox

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type identity struct {
	Admin      bool
	ID         string
	Bots       []string
	AllowReply bool
	Cookie     bool
	CSRF       string
}
type identityKey struct{}
type loginAttempt struct {
	Count int
	Until time.Time
}

type Server struct {
	Store     *Store
	Config    func() Config
	Assets    fs.FS
	BotStates func() map[string]string
	Sessions  func(context.Context, Bot) ([]Session, error)
	mu        sync.Mutex
	attempts  map[string]loginAttempt
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.Store.DB.PingContext(r.Context()) != nil {
			fail(w, 503, "unavailable", "database unavailable")
			return
		}
		respond(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.auth(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "inbox_session", Path: "/", HttpOnly: true, Secure: s.Config().PublicURL != "" || r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		respond(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /v1/session", s.auth(func(w http.ResponseWriter, r *http.Request) {
		i := who(r)
		respond(w, 200, map[string]any{"admin": i.Admin, "consumer_id": i.ID, "csrf": i.CSRF})
	}))
	mux.HandleFunc("GET /v1/bots", s.auth(s.bots))
	mux.HandleFunc("GET /v1/bots/{id}/sessions", s.auth(s.sessions))
	mux.HandleFunc("GET /v1/stats", s.auth(s.stats))
	mux.HandleFunc("GET /v1/consumers", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if !who(r).Admin {
			fail(w, 403, "forbidden", "administrator required")
			return
		}
		v := s.Config().Consumers
		if v == nil {
			v = []Consumer{}
		}
		respond(w, 200, v)
	}))
	mux.HandleFunc("GET /v1/messages", s.auth(s.list))
	mux.HandleFunc("GET /v1/messages/{id}", s.auth(s.detail))
	mux.HandleFunc("GET /v1/attachments/{id}", s.auth(s.attachment))
	mux.HandleFunc("GET /v1/export", s.auth(s.export))
	mux.HandleFunc("POST /v1/messages/{id}/results", s.auth(s.result))
	mux.HandleFunc("POST /v1/deliveries/{id}/retry", s.auth(s.retry))
	if s.Assets != nil {
		mux.Handle("GET /", http.FileServerFS(s.Assets))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func who(r *http.Request) identity { return r.Context().Value(identityKey{}).(identity) }
func equalSecret(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
func signed(secret, body string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := s.Config()
		var id identity
		valid := false
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			token := strings.TrimPrefix(a, "Bearer ")
			if equalSecret(token, c.AdminToken) {
				id = identity{Admin: true, ID: "admin", AllowReply: true}
				valid = true
			} else {
				for _, v := range c.Consumers {
					if equalSecret(token, v.Token) {
						id = identity{ID: v.ID, Bots: v.Bots, AllowReply: v.AllowReply}
						valid = true
						break
					}
				}
			}
		} else if cookie, e := r.Cookie("inbox_session"); e == nil {
			parts := strings.Split(cookie.Value, ".")
			if len(parts) == 3 {
				expiry, e := strconv.ParseInt(parts[0], 10, 64)
				payload := parts[0] + "." + parts[1]
				if e == nil && expiry > time.Now().Unix() && equalSecret(parts[2], signed(c.AdminToken, payload)) {
					id = identity{Admin: true, ID: "admin", AllowReply: true, Cookie: true, CSRF: signed(c.AdminToken, "csrf:"+payload)}
					valid = true
				}
			}
		}
		if !valid {
			fail(w, 401, "unauthorized", "sign in or supply a valid Bearer token")
			return
		}
		if id.Cookie && r.Method != "GET" && r.Method != "HEAD" {
			if !s.validOrigin(r) || !equalSecret(r.Header.Get("X-CSRF-Token"), id.CSRF) {
				fail(w, 403, "csrf", "invalid request origin or CSRF token")
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	}
}

func (s *Server) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	if e != nil {
		return false
	}
	if s.Config().PublicURL != "" {
		return origin == strings.TrimSuffix(s.Config().PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && u.Host == r.Host && u.Path == "" && u.RawQuery == "" && u.User == nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.validOrigin(r) {
		fail(w, 403, "origin", "invalid request origin")
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	if s.attempts == nil {
		s.attempts = map[string]loginAttempt{}
	}
	for k, v := range s.attempts {
		if time.Now().After(v.Until) {
			delete(s.attempts, k)
		}
	}
	v := s.attempts[ip]
	if v.Count >= 10 || len(s.attempts) > 1024 {
		s.mu.Unlock()
		fail(w, 429, "rate_limit", "try again in 10 minutes")
		return
	}
	v.Count++
	v.Until = time.Now().Add(10 * time.Minute)
	s.attempts[ip] = v
	s.mu.Unlock()
	var body struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	c := s.Config()
	if !equalSecret(body.Token, c.AdminToken) {
		fail(w, 401, "unauthorized", "invalid access token")
		return
	}
	s.mu.Lock()
	delete(s.attempts, ip)
	s.mu.Unlock()
	payload := strconv.FormatInt(time.Now().Add(12*time.Hour).Unix(), 10) + "." + RandomID()
	http.SetCookie(w, &http.Cookie{Name: "inbox_session", Value: payload + "." + signed(c.AdminToken, payload), Path: "/", HttpOnly: true, Secure: c.PublicURL != "" || r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	respond(w, 200, map[string]string{"csrf": signed(c.AdminToken, "csrf:"+payload)})
}

func allowed(id identity, bot string) bool { return id.Admin || contains(id.Bots, bot) }
func (s *Server) message(w http.ResponseWriter, r *http.Request) (Message, bool) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || id < 1 {
		fail(w, 400, "invalid_id", "invalid message ID")
		return Message{}, false
	}
	m, e := s.Store.Get(r.Context(), id)
	if errors.Is(e, ErrNotFound) || e == nil && !allowed(who(r), m.BotID) {
		fail(w, 404, "not_found", "message not found")
		return m, false
	}
	if e != nil {
		fail(w, 500, "storage", "cannot read message")
		return m, false
	}
	return m, true
}

func (s *Server) filter(r *http.Request) (Filter, error) {
	q := r.URL.Query()
	f := Filter{BotID: q.Get("bot_id"), Query: q.Get("q"), Type: q.Get("type"), SenderID: q.Get("sender_id"), Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: 50}
	if !who(r).Admin {
		f.AllowedBots = who(r).Bots
	}
	if len(f.Query) > 200 || len(f.Cursor) > 80 {
		return f, errors.New("query is too long")
	}
	if v := q.Get("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 200 {
			return f, errors.New("limit must be 1..200")
		}
		f.Limit = n
	}
	for key, target := range map[string]*string{"from": &f.From, "to": &f.To} {
		if raw := q.Get(key); raw != "" {
			t, e := time.Parse(time.RFC3339Nano, raw)
			if e != nil {
				return f, errors.New("from/to must be RFC3339 timestamps")
			}
			*target = t.UTC().Format(timestampLayout)
		}
	}
	return f, nil
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	f, e := s.filter(r)
	if e != nil {
		fail(w, 400, "invalid_query", e.Error())
		return
	}
	p, e := s.Store.List(r.Context(), f)
	if e != nil {
		fail(w, 400, "query_failed", "invalid cursor or query unavailable")
		return
	}
	respond(w, 200, p)
}
func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	m, ok := s.message(w, r)
	if !ok {
		return
	}
	ds, e := s.Store.Deliveries(r.Context(), m.ID)
	if e != nil {
		fail(w, 500, "storage", "cannot read deliveries")
		return
	}
	rs, e := s.Store.Results(r.Context(), m.ID)
	if e != nil {
		fail(w, 500, "storage", "cannot read results")
		return
	}
	id := who(r)
	if !id.Admin {
		filtered := []Delivery{}
		for _, d := range ds {
			if d.ConsumerID == id.ID {
				filtered = append(filtered, d)
			}
		}
		ds = filtered
		results := []Result{}
		for _, v := range rs {
			if v.ConsumerID == id.ID {
				results = append(results, v)
			}
		}
		rs = results
	}
	respond(w, 200, map[string]any{"message": m, "deliveries": ds, "results": rs})
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	var messageID int64
	var storageKey, name, status string
	e := s.Store.DB.QueryRowContext(r.Context(), `SELECT message_id,storage_key,name,status FROM attachments WHERE id=?`, r.PathValue("id")).Scan(&messageID, &storageKey, &name, &status)
	if e != nil {
		fail(w, 404, "not_found", "attachment not found")
		return
	}
	m, e := s.Store.Get(r.Context(), messageID)
	if e != nil || !allowed(who(r), m.BotID) {
		fail(w, 404, "not_found", "attachment not found")
		return
	}
	if status != "ready" {
		fail(w, 409, "unavailable", "attachment was not downloaded")
		return
	}
	var attachment Attachment
	for _, a := range m.Attachments {
		if a.ID == r.PathValue("id") {
			attachment = a
			break
		}
	}
	f, e := s.Store.OpenAttachment(attachment)
	if e != nil {
		fail(w, 409, "integrity", "attachment file missing or checksum mismatch")
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		fail(w, 503, "unavailable", "attachment unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	m, ok := s.message(w, r)
	if !ok {
		return
	}
	if m.Status == "pending" {
		fail(w, 409, "pending", "message is still being archived")
		return
	}
	var in ResultInput
	if !readJSON(w, r, &in) {
		return
	}
	id := who(r)
	if in.Reply && !id.AllowReply {
		fail(w, 403, "forbidden", "this consumer cannot send replies")
		return
	}
	v, created, e := s.Store.AddResult(r.Context(), m.ID, id.ID, in)
	if errors.Is(e, ErrConflict) {
		fail(w, 409, "conflict", e.Error())
		return
	}
	if e != nil {
		fail(w, 400, "invalid_result", e.Error())
		return
	}
	status := 200
	if created {
		status = 201
	}
	respond(w, status, v)
}

func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	var messageID int64
	var consumer string
	e := s.Store.DB.QueryRowContext(r.Context(), `SELECT message_id,consumer_id FROM deliveries WHERE id=?`, r.PathValue("id")).Scan(&messageID, &consumer)
	if e != nil {
		fail(w, 404, "not_found", "delivery not found")
		return
	}
	id := who(r)
	m, e := s.Store.Get(r.Context(), messageID)
	if e != nil || !allowed(id, m.BotID) || (!id.Admin && id.ID != consumer) {
		fail(w, 404, "not_found", "delivery not found")
		return
	}
	if e = s.Store.RetryDelivery(r.Context(), r.PathValue("id")); e != nil {
		fail(w, 409, "conflict", "only failed or delivered events can be replayed")
		return
	}
	respond(w, 200, map[string]bool{"queued": true})
}

// 会话名称涉及组织内部信息，只对管理员开放。
func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	if !who(r).Admin {
		fail(w, 403, "forbidden", "administrator access required")
		return
	}
	b, ok := s.Config().Bot(r.PathValue("id"))
	if !ok || !b.Enabled {
		fail(w, 404, "not_found", "bot not found or disabled")
		return
	}
	if s.Sessions == nil {
		fail(w, 501, "unavailable", "session listing is not configured")
		return
	}
	list, err := s.Sessions(r.Context(), b)
	if err != nil {
		fail(w, 502, "upstream_error", err.Error())
		return
	}
	respond(w, 200, map[string]any{"sessions": list})
}

func (s *Server) bots(w http.ResponseWriter, r *http.Request) {
	states := map[string]string{}
	if s.BotStates != nil {
		states = s.BotStates()
	}
	out := []map[string]any{}
	for _, b := range s.Config().Bots {
		if !allowed(who(r), b.ID) {
			continue
		}
		state := states[b.ID]
		if !b.Enabled {
			state = "disabled"
		} else if state == "" {
			state = "disconnected"
		}
		out = append(out, map[string]any{"id": b.ID, "name": b.Name, "enabled": b.Enabled, "state": state})
	}
	respond(w, 200, out)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	where := "1=1"
	args := []any{}
	id := who(r)
	if !id.Admin && !contains(id.Bots, "*") {
		marks := []string{}
		for _, b := range id.Bots {
			marks = append(marks, "?")
			args = append(args, b)
		}
		if len(marks) == 0 {
			where = "1=0"
		} else {
			where = "bot_id IN (" + strings.Join(marks, ",") + ")"
		}
	}
	var total, pending, failed, attachments, bytes int64
	e := s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(status IN ('pending','importing')),0),COALESCE(SUM(status IN ('failed','partial')),0) FROM messages WHERE `+where, args...).Scan(&total, &pending, &failed)
	if e == nil {
		e = s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(size),0) FROM attachments WHERE message_id IN (SELECT id FROM messages WHERE `+where+`)`, args...).Scan(&attachments, &bytes)
	}
	if e != nil {
		fail(w, 500, "storage", "cannot load statistics")
		return
	}
	respond(w, 200, map[string]int64{"messages": total, "pending": pending, "attention": failed, "attachments": attachments, "bytes": bytes})
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	f, e := s.filter(r)
	if e != nil {
		fail(w, 400, "invalid_query", e.Error())
		return
	}
	f.Limit = 200
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "json" {
		fail(w, 400, "format", "format must be json or csv")
		return
	}
	p, e := s.Store.List(r.Context(), f)
	if e != nil {
		fail(w, 400, "invalid_query", "export query unavailable")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="wecom-inbox.`+format+`"`)
	var cw *csv.Writer
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		cw = csv.NewWriter(w)
		cw.Write([]string{"id", "bot_id", "platform_id", "received_at", "sender_id", "chat_id", "type", "status", "content", "attachment_names"})
	} else {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "[")
	}
	first := true
	for {
		for _, m := range p.Messages {
			if format == "json" {
				if !first {
					io.WriteString(w, ",")
				}
				if json.NewEncoder(w).Encode(m) != nil {
					return
				}
				first = false
			} else {
				names := []string{}
				for _, a := range m.Attachments {
					names = append(names, a.Name)
				}
				row := []string{strconv.FormatInt(m.ID, 10), m.BotID, m.PlatformID, m.ReceivedAt, m.SenderID, m.ChatID, m.Type, m.Status, m.Content, strings.Join(names, "; ")}
				for i, v := range row {
					row[i] = csvSafe(v)
				}
				if cw.Write(row) != nil {
					return
				}
			}
		}
		if p.NextCursor == "" {
			break
		}
		f.Cursor = p.NextCursor
		p, e = s.Store.List(r.Context(), f)
		if e != nil {
			return
		}
	}
	if cw != nil {
		cw.Flush()
	} else {
		io.WriteString(w, "]\n")
	}
}
func csvSafe(v string) string {
	trim := strings.TrimLeft(v, " \t\r\n")
	if trim != "" && strings.ContainsAny(trim[:1], "=+-@\t\r") {
		return "'" + v
	}
	return v
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(target); e != nil {
		fail(w, 400, "invalid_json", "invalid JSON request")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(w, 400, "invalid_json", "one JSON value required")
		return false
	}
	return true
}
