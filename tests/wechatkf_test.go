package tests

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iCookCloud/wecom-inbox/internal/inbox"
	"github.com/iCookCloud/wecom-inbox/internal/wechatkf"
	"github.com/iCookCloud/wecom-inbox/web"
)

type kfTransport func(*http.Request) (*http.Response, error)

func (fn kfTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func kfResponse(value any) *http.Response {
	b, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(b))}
}
func kfConfig() inbox.Config {
	c := config()
	c.WeChatKF = []inbox.WeChatKF{{ID: "support", CorpID: "ww-test", Secret: "test-application-secret", Token: "TestCallbackToken123", EncodingAESKey: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)), Enabled: true, Accounts: []inbox.KFAccount{{OpenKfID: "wk-test", Name: "测试微信客服", AllowedCustomers: []string{"customer-a"}}}}}
	return c
}
func kfSource(c inbox.Config) string { return c.WeChatKF[0].Source(c.WeChatKF[0].Accounts[0]).ID }
func kfService(s *inbox.Store, c *inbox.Config, fn kfTransport) *wechatkf.Service {
	return &wechatkf.Service{Store: s, Config: func() inbox.Config { return *c }, NewClient: func(connection inbox.WeChatKF) *wechatkf.Client {
		return &wechatkf.Client{Config: connection, HTTP: &http.Client{Transport: fn}}
	}}
}
func kfDue(t *testing.T, s *inbox.Store) {
	t.Helper()
	if _, err := s.DB.Exec("UPDATE kf_sync SET next_at=''"); err != nil {
		t.Fatal(err)
	}
}
func kfMessage(id, kind string, part any) map[string]any {
	return map[string]any{"msgid": id, "open_kfid": "wk-test", "external_userid": "customer-a", "origin": 3, "send_time": time.Now().Unix(), "msgtype": kind, kind: part}
}
func kfPage(cursor string, more int, messages ...any) *http.Response {
	if messages == nil {
		messages = []any{}
	}
	return kfResponse(map[string]any{"errcode": 0, "next_cursor": cursor, "has_more": more, "msg_list": messages})
}
func kfToken() *http.Response {
	return kfResponse(map[string]any{"errcode": 0, "access_token": "secret-access-token", "expires_in": 7200})
}

// 独立构造腾讯协议的密文及签名；不调用产品的解密/验签实现生成期望值。
func kfSigned(t *testing.T, c inbox.WeChatKF, plain, receiver string, stamp int64) (string, url.Values) {
	t.Helper()
	key, err := base64.RawStdEncoding.DecodeString(c.EncodingAESKey)
	if err != nil {
		t.Fatal(err)
	}
	data := append([]byte("1234567890abcdef"), make([]byte, 4)...)
	binary.BigEndian.PutUint32(data[16:20], uint32(len(plain)))
	data = append(data, []byte(plain)...)
	data = append(data, []byte(receiver)...)
	padding := 32 - len(data)%32
	data = append(data, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(data, data)
	encrypted := base64.StdEncoding.EncodeToString(data)
	ts := strconv.FormatInt(stamp, 10)
	parts := []string{c.Token, ts, "test-nonce", encrypted}
	sort.Strings(parts)
	h := sha1.Sum([]byte(strings.Join(parts, "")))
	return encrypted, url.Values{"msg_signature": {hex.EncodeToString(h[:])}, "timestamp": {ts}, "nonce": {"test-nonce"}}
}
func kfNotify(t *testing.T, handler http.Handler, c inbox.WeChatKF, token string) *httptest.ResponseRecorder {
	t.Helper()
	plain := fmt.Sprintf("<xml><ToUserName>%s</ToUserName><MsgType>event</MsgType><Event>kf_msg_or_event</Event><Token>%s</Token><OpenKfId>wk-test</OpenKfId><CreateTime>%d</CreateTime></xml>", c.CorpID, token, time.Now().Unix())
	encrypted, q := kfSigned(t, c, plain, c.CorpID, time.Now().Unix())
	req := httptest.NewRequest("POST", "/callbacks/wechat-kf/"+c.ID+"?"+q.Encode(), strings.NewReader("<xml><Encrypt>"+encrypted+"</Encrypt></xml>"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestKFConfigurationAndSourceScopes(t *testing.T) {
	c := kfConfig()
	source := kfSource(c)
	c.Consumers = []inbox.Consumer{{ID: "worker", Token: consumerToken, Bots: []string{source}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if b, ok := c.Bot(source); !ok || b.Kind != "wechat_kf" || len(c.Sources()) != 3 {
		t.Fatal("missing independent source")
	}
	for name, change := range map[string]func(*inbox.Config){
		"empty allowlist":   func(c *inbox.Config) { c.WeChatKF[0].Accounts[0].AllowedCustomers = nil },
		"bad AES":           func(c *inbox.Config) { c.WeChatKF[0].EncodingAESKey = "bad" },
		"bad token":         func(c *inbox.Config) { c.WeChatKF[0].Token = "not a token" },
		"duplicate account": func(c *inbox.Config) { c.WeChatKF = append(c.WeChatKF, c.WeChatKF[0]); c.WeChatKF[1].ID = "other" },
		"reserved source":   func(c *inbox.Config) { c.Bots[0].ID = "kf:ww-test:wk-test" },
	} {
		t.Run(name, func(t *testing.T) {
			c := kfConfig()
			change(&c)
			if c.Validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestKFCallbackVerificationAndDurableNotification(t *testing.T) {
	s := store(t)
	c := kfConfig()
	service := kfService(s, &c, nil)
	handler := (&inbox.Server{Store: s, Config: func() inbox.Config { return c }, Assets: web.Assets, Callbacks: service}).Handler()
	k := c.WeChatKF[0]
	for _, tc := range []struct {
		name, receiver string
		age            time.Duration
		tamper         bool
		status         int
	}{
		{"valid", k.CorpID, 0, false, 200}, {"wrong enterprise", "different", 0, false, 403}, {"stale", k.CorpID, -11 * time.Minute, false, 403}, {"signature", k.CorpID, 0, true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encrypted, q := kfSigned(t, k, "challenge", tc.receiver, time.Now().Add(tc.age).Unix())
			q.Set("echostr", encrypted)
			if tc.tamper {
				q.Set("msg_signature", strings.Repeat("0", 40))
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", "/callbacks/wechat-kf/support?"+q.Encode(), nil))
			if w.Code != tc.status || (w.Code == 200 && w.Body.String() != "challenge") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	w := kfNotify(t, handler, k, "notification-secret")
	if w.Code != 200 || w.Body.String() != "success" {
		t.Fatal(w.Code, w.Body.String())
	}
	state, err := s.KFState(ctx, kfSource(c))
	if err != nil || state.Token != "notification-secret" {
		t.Fatal(state, err)
	}
	var encrypted []byte
	s.DB.QueryRow("SELECT token FROM kf_sync").Scan(&encrypted)
	if bytes.Contains(encrypted, []byte("notification-secret")) {
		t.Fatal("callback token stored unencrypted")
	}
	c.WeChatKF[0].Enabled = false
	if w = kfNotify(t, handler, k, "new"); w.Code != 404 {
		t.Fatal("disabled callback accepted")
	}
}

func TestKFCustomerToArchiveResultAndAsyncFailure(t *testing.T) {
	s := store(t)
	c := kfConfig()
	source := kfSource(c)
	c.Consumers = []inbox.Consumer{{ID: "worker", Token: consumerToken, Bots: []string{source}, AllowReply: true}}
	phase := 0
	tokenCalls := 0
	sendCalls := 0
	sentID := ""
	file := []byte("synthetic customer quote,123\n")
	var sent map[string]any
	service := kfService(s, &c, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "qyapi.weixin.qq.com" {
			t.Fatal("unexpected API origin")
		}
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			tokenCalls++
			return kfToken(), nil
		case "/cgi-bin/kf/sync_msg":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["open_kfid"] != "wk-test" {
				t.Fatal("incorrect account")
			}
			if phase == 0 {
				return kfPage("c1", 1, kfMessage("text-1", "text", map[string]string{"content": "请处理这份报价"}), kfMessage("file-1", "file", map[string]string{"media_id": "media-file"})), nil
			}
			if phase == 1 {
				if body["cursor"] != "c1" {
					t.Fatal("cursor missing")
				}
				return kfPage("c2", 1), nil
			}
			if phase == 2 {
				return kfPage("c3", 0, kfMessage("file-1", "file", map[string]string{"media_id": "media-file"})), nil
			}
			return kfPage("c4", 0, map[string]any{"msgtype": "event", "origin": 4, "event": map[string]any{"event_type": "msg_send_fail", "open_kfid": "wk-test", "fail_msgid": sentID, "fail_type": 10}}), nil
		case "/cgi-bin/media/get":
			if r.URL.Query().Get("media_id") != "media-file" {
				t.Fatal("wrong media")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Disposition": {"attachment; filename=quote.csv"}}, Body: io.NopCloser(bytes.NewReader(file))}, nil
		case "/cgi-bin/kf/send_msg":
			sendCalls++
			json.NewDecoder(r.Body).Decode(&sent)
			sentID = sent["msgid"].(string)
			return kfResponse(map[string]any{"errcode": 0, "msgid": sentID}), nil
		}
		t.Fatalf("unexpected path %s", r.URL.Path)
		return nil, nil
	})
	handler := (&inbox.Server{Store: s, Config: func() inbox.Config { return c }, Callbacks: service, BotStates: func() map[string]string { return service.States(ctx) }}).Handler()
	if w := kfNotify(t, handler, c.WeChatKF[0], "sync-token"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for phase = 0; phase < 3; phase++ {
		kfDue(t, s)
		if _, err := service.SyncOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.List(ctx, inbox.Filter{BotID: source})
	if err != nil || len(p.Messages) != 2 {
		t.Fatal("dedup failed", err, len(p.Messages))
	}
	ing := inbox.Ingestor{Store: s, Config: func() inbox.Config { return c }, DownloadMedia: service.Download}
	for range 2 {
		if _, err = ing.ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var fileMessage inbox.Message
	p, _ = s.List(ctx, inbox.Filter{BotID: source})
	for _, m := range p.Messages {
		if m.Type == "file" {
			fileMessage = m
		}
	}
	if fileMessage.Status != "ready" || len(fileMessage.Attachments) != 1 || fileMessage.Attachments[0].Name != "quote.csv" {
		t.Fatal(fileMessage)
	}
	f, err := s.OpenAttachment(fileMessage.Attachments[0])
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if !bytes.Equal(data, file) {
		t.Fatal("original bytes changed")
	}
	result, _, err := s.AddResult(ctx, fileMessage.ID, "worker", inbox.ResultInput{Key: "processed", Status: "completed", Summary: "报价已核对", Reply: true})
	if err != nil {
		t.Fatal(err)
	}
	d := inbox.Dispatcher{Store: s, Config: func() inbox.Config { return c }}
	if _, err = d.ReplyOne(ctx, service); err != nil {
		t.Fatal(err)
	}
	result, _ = s.Result(ctx, result.ID)
	if result.ReplyStatus != "accepted" || sent["touser"] != "customer-a" || sent["open_kfid"] != "wk-test" {
		t.Fatal(result, sent)
	}
	kfDue(t, s)
	if _, err = service.SyncOne(ctx); err != nil {
		t.Fatal(err)
	}
	result, _ = s.Result(ctx, result.ID)
	if result.ReplyStatus != "failed" || !strings.Contains(result.ReplyError, "10") {
		t.Fatal("async failure lost", result)
	}
	d.ReplyOne(ctx, service)
	if sendCalls != 1 || tokenCalls != 1 {
		t.Fatal("replayed send or no token cache", sendCalls, tokenCalls)
	}
}

func TestKFPageCrashReplayAndNotificationRace(t *testing.T) {
	s := store(t)
	c := kfConfig()
	source := kfSource(c)
	phase := 0
	service := kfService(s, &c, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			return kfToken(), nil
		}
		if phase == 0 {
			// 第一条已落盘后，第二条身份错误令整页游标不推进。
			wrong := kfMessage("wrong", "text", map[string]string{"content": "wrong"})
			wrong["open_kfid"] = "other-account"
			return kfPage("cursor-1", 0, kfMessage("same", "text", map[string]string{"content": "persist"}), wrong), nil
		}
		if err := s.KFNotify(ctx, source, "newer-token", time.Now().Unix()+1); err != nil {
			t.Fatal(err)
		}
		return kfPage("cursor-2", 0, kfMessage("same", "text", map[string]string{"content": "persist"})), nil
	})
	if _, err := service.SyncOne(ctx); err == nil {
		t.Fatal("mismatched source accepted")
	}
	state, _ := s.KFState(ctx, source)
	if state.Cursor != "" {
		t.Fatal("advanced past unpersisted record")
	}
	phase = 1
	kfDue(t, s)
	if _, err := service.SyncOne(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ = s.KFState(ctx, source)
	if state.Cursor != "cursor-2" || state.Token != "newer-token" || state.NextAt != "" {
		t.Fatal("lost notification during pull", state)
	}
	page, _ := s.List(ctx, inbox.Filter{BotID: source})
	if len(page.Messages) != 1 {
		t.Fatal("replay duplicated records")
	}
	// 重开同一数据库验证 checkpoint/token 保留，不借用进程内变量。
	path := s.Dir
	s.Close()
	reopened, err := inbox.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err = reopened.KFState(ctx, source)
	if err != nil || state.Cursor != "cursor-2" || state.Token != "newer-token" {
		t.Fatal("restart state lost", state, err)
	}
}

func TestKFStructuredAndUnsupportedMessages(t *testing.T) {
	s := store(t)
	c := kfConfig()
	inner, _ := json.Marshal(map[string]any{"msgtype": "voice", "voice": map[string]string{"media_id": "voice-id"}})
	merged := kfMessage("merged", "merged_msg", map[string]any{"title": "合并记录", "item": []any{map[string]any{"sender_name": "演示客户", "send_time": time.Now().Unix(), "msg_content": string(inner)}}})
	staff := kfMessage("staff", "text", map[string]string{"content": "人工已处理"})
	staff["origin"] = 5
	staff["servicer_userid"] = "employee-a"
	service := kfService(s, &c, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			return kfToken(), nil
		}
		return kfPage("next", 0, merged, kfMessage("unknown", "future_type", map[string]string{"private": "raw detail"}), staff), nil
	})
	if _, err := service.SyncOne(ctx); err != nil {
		t.Fatal(err)
	}
	page, _ := s.List(ctx, inbox.Filter{BotID: kfSource(c)})
	if len(page.Messages) != 3 {
		t.Fatal(page)
	}
	for _, m := range page.Messages {
		switch m.PlatformID {
		case "merged":
			if len(m.Attachments) != 1 || m.Attachments[0].Kind != "voice" || !strings.Contains(m.Content, "演示客户") {
				t.Fatal(m)
			}
		case "unknown":
			if m.Status != "failed" || !strings.Contains(m.Error, "unsupported") {
				t.Fatal("unknown type falsely archived", m)
			}
			var payload []byte
			s.DB.QueryRow("SELECT payload FROM messages WHERE id=?", m.ID).Scan(&payload)
			if len(payload) == 0 || bytes.Contains(payload, []byte("raw detail")) {
				t.Fatal("raw record not retained encrypted")
			}
		case "staff":
			if m.ChatType != "wechat_kf_event" || m.SenderID != "employee-a" {
				t.Fatal(m)
			}
		}
	}
}

func TestKFTokenRefreshMediaErrorsAndSecretRedaction(t *testing.T) {
	c := kfConfig().WeChatKF[0]
	tokens := 0
	calls := 0
	client := &wechatkf.Client{Config: c, HTTP: &http.Client{Transport: kfTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			tokens++
			return kfToken(), nil
		}
		calls++
		if calls == 1 {
			return kfResponse(map[string]any{"errcode": 42001, "errmsg": "hidden-secret"}), nil
		}
		return kfResponse(map[string]any{"errcode": 0, "next_cursor": "cursor", "has_more": 0}), nil
	})}}
	if err := client.Call(ctx, "/cgi-bin/kf/sync_msg", map[string]any{}, nil); err != nil || tokens != 2 || calls != 2 {
		t.Fatal(err, tokens, calls)
	}
	client.HTTP.Transport = kfTransport(func(r *http.Request) (*http.Response, error) {
		return kfResponse(map[string]any{"errcode": 40007, "errmsg": "hidden-secret"}), nil
	})
	if _, _, err := client.Download(ctx, inbox.MediaSource{MediaID: "expired", Name: "file"}, 1024); err == nil || strings.Contains(err.Error(), "hidden-secret") {
		t.Fatal("media JSON error archived or leaked", err)
	}
	client.HTTP.Transport = kfTransport(func(r *http.Request) (*http.Response, error) { return nil, fmt.Errorf("failure at %s", r.URL.String()) })
	err := client.Call(ctx, "/cgi-bin/kf/send_msg", map[string]any{}, nil)
	if err != inbox.ErrUncertain || strings.Contains(err.Error(), "secret-access-token") {
		t.Fatal("network ambiguity or token leak", err)
	}
}

func TestKFReplyFailureBeforeSynchronousAcceptance(t *testing.T) {
	s := store(t)
	c := kfConfig()
	source := kfSource(c)
	in := input("reply-race", source, "hello")
	in.Message.ChatType = "wechat_kf"
	in.Message.ChatID = "customer-a"
	in.Message.SenderID = "customer-a"
	in.Message.PlatformAt = time.Now().UTC().Format(time.RFC3339)
	id := archive(t, s, in, c)
	result, _, err := s.AddResult(ctx, id, "admin", inbox.ResultInput{Key: "race", Status: "completed", Summary: "done", Reply: true})
	if err != nil {
		t.Fatal(err)
	}
	service := kfService(s, &c, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			return kfToken(), nil
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		msgid := body["msgid"].(string)
		if err := s.KFSendFailure(ctx, source, msgid, "recipient rejected"); err != nil {
			t.Fatal(err)
		}
		return kfResponse(map[string]any{"errcode": 0, "msgid": msgid}), nil
	})
	d := inbox.Dispatcher{Store: s, Config: func() inbox.Config { return c }}
	if _, err = d.ReplyOne(ctx, service); err != nil {
		t.Fatal(err)
	}
	result, _ = s.Result(ctx, result.ID)
	if result.ReplyStatus != "failed" || result.ReplyError != "recipient rejected" {
		t.Fatal("async failure overwritten", result)
	}
}

func TestKFHTTPScopesAndReplyLimits(t *testing.T) {
	s := store(t)
	c := kfConfig()
	source := kfSource(c)
	c.Consumers = []inbox.Consumer{{ID: "bot-only", Token: consumerToken, Bots: []string{"bot-a"}}}
	in := input("kf-private", source, "customer detail")
	in.Message.ChatType = "wechat_kf"
	in.Message.SenderID = "customer-a"
	in.Message.ChatID = "customer-a"
	id := archive(t, s, in, c)
	server := httptest.NewServer((&inbox.Server{Store: s, Config: func() inbox.Config { return c }}).Handler())
	defer server.Close()
	resp, data := request(t, server.Client(), "GET", server.URL+"/v1/messages", consumerToken, "")
	if resp.StatusCode != 200 || strings.Contains(string(data), "customer detail") {
		t.Fatal("customer record leaked")
	}
	resp, data = request(t, server.Client(), "GET", server.URL+"/v1/bots", adminToken, "")
	if resp.StatusCode != 200 || !strings.Contains(string(data), "wechat_kf") || strings.Contains(string(data), c.WeChatKF[0].Secret) {
		t.Fatal("source metadata wrong", string(data))
	}
	body, _ := json.Marshal(inbox.ResultInput{Key: "long", Status: "completed", Summary: strings.Repeat("中", 700), Reply: true})
	resp, _ = request(t, server.Client(), "POST", server.URL+"/v1/messages/"+itoa(id)+"/results", adminToken, string(body))
	if resp.StatusCode != 400 {
		t.Fatal("platform would truncate this reply", resp.StatusCode)
	}
	resp, _ = request(t, server.Client(), "GET", server.URL+"/v1/bots/"+url.PathEscape(source)+"/sessions", adminToken, "")
	if resp.StatusCode != 400 {
		t.Fatal("KF source passed to Bot API", resp.StatusCode)
	}
}

func TestKFReplyWindowAndRevocation(t *testing.T) {
	for _, scenario := range []string{"old", "revoked", "staff"} {
		t.Run(scenario, func(t *testing.T) {
			s := store(t)
			c := kfConfig()
			source := kfSource(c)
			in := input("reply-guard", source, "hello")
			in.Message.ChatType = "wechat_kf"
			in.Message.ChatID = "customer-a"
			in.Message.SenderID = "customer-a"
			in.Message.PlatformAt = time.Now().Add(-49 * time.Hour).UTC().Format(time.RFC3339)
			if scenario == "staff" {
				in.Message.ChatType = "wechat_kf_event"
			}
			id := archive(t, s, in, c)
			result, _, err := s.AddResult(ctx, id, "admin", inbox.ResultInput{Key: "guard", Status: "completed", Summary: "done", Reply: true})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "revoked" {
				c.WeChatKF[0].Accounts[0].AllowedCustomers = []string{"another-customer"}
			}
			service := kfService(s, &c, func(r *http.Request) (*http.Response, error) { t.Fatal("should not contact platform"); return nil, nil })
			d := inbox.Dispatcher{Store: s, Config: func() inbox.Config { return c }}
			if _, err = d.ReplyOne(ctx, service); err != nil {
				t.Fatal(err)
			}
			result, _ = s.Result(ctx, result.ID)
			if result.ReplyStatus != "failed" && result.ReplyStatus != "cancelled" {
				t.Fatal("unsafe reply", result)
			}
		})
	}
}

var _ inbox.ReplySender = (*wechatkf.Service)(nil)

func TestKFSyncFairnessAndRevokedMedia(t *testing.T) {
	s := store(t)
	c := kfConfig()
	c.WeChatKF[0].Accounts = append(c.WeChatKF[0].Accounts, inbox.KFAccount{OpenKfID: "second", AllowedCustomers: []string{"customer-a"}})
	calls := map[string]int{}
	service := kfService(s, &c, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			return kfToken(), nil
		}
		if r.URL.Path == "/cgi-bin/media/get" {
			t.Error("downloaded after customer authorization revoked")
			return nil, fmt.Errorf("denied")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		account := body["open_kfid"].(string)
		calls[account]++
		return kfPage("cursor", 0), nil
	})
	for range 5 {
		if err := s.KFNotify(ctx, kfSource(c), "token", time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := service.SyncOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls["second"] == 0 {
		t.Error("busy first account starved second account")
	}
	c.WeChatKF[0].Accounts[0].AllowedCustomers = []string{"customer-b"}
	_, _, err := service.Download(ctx, inbox.Message{BotID: kfSource(c), ChatID: "customer-a"}, inbox.MediaSource{MediaID: "private", Name: "file"}, 100)
	if err == nil {
		t.Error("revoked media accepted")
	}
}

func TestKFMediaErrorWithoutJSONHeader(t *testing.T) {
	c := kfConfig().WeChatKF[0]
	client := &wechatkf.Client{Config: c, HTTP: &http.Client{Transport: kfTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			return kfToken(), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader(`{"errcode":40007,"errmsg":"invalid media_id"}`))}, nil
	})}}
	if _, _, err := client.Download(ctx, inbox.MediaSource{MediaID: "bad", Name: "file"}, 1024); err == nil {
		t.Fatal("API error body treated as an archived file")
	}
}

func TestKFAccountDiscoveryAndContactLink(t *testing.T) {
	c := kfConfig().WeChatKF[0]
	pages := 0
	client := &wechatkf.Client{Config: c, HTTP: &http.Client{Transport: kfTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			return kfToken(), nil
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/cgi-bin/kf/account/list" {
			if int(body["offset"].(float64)) != pages*100 {
				t.Fatal("bad account pagination")
			}
			pages++
			accounts := []any{}
			if pages == 1 {
				for n := range 100 {
					accounts = append(accounts, map[string]string{"open_kfid": fmt.Sprintf("wk-%d", n), "name": "test"})
				}
			}
			return kfResponse(map[string]any{"errcode": 0, "account_list": accounts}), nil
		}
		if body["open_kfid"] != "wk-test" {
			t.Fatal("incorrect contact account")
		}
		return kfResponse(map[string]any{"errcode": 0, "url": "https://work.weixin.qq.com/kf/synthetic-test-link"}), nil
	})}}
	accounts, err := client.Accounts(ctx)
	if err != nil || len(accounts) != 100 || pages != 2 {
		t.Fatal(accounts, err, pages)
	}
	link, err := client.ContactLink(ctx, "wk-test")
	if err != nil || link != "https://work.weixin.qq.com/kf/synthetic-test-link" {
		t.Fatal(link, err)
	}
	if _, err = inbox.LoadConfig("../config.example.toml"); err != nil {
		t.Fatal("example config invalid", err)
	}
}

func TestKFMissingPaginationDoesNotResetCursorAndJSONFileIsPreserved(t *testing.T) {
	s := store(t)
	c := kfConfig()
	source := kfSource(c)
	s.EnsureKF(ctx, source)
	state, _ := s.KFState(ctx, source)
	s.KFAdvance(ctx, source, state, "existing-cursor", false)
	kfDue(t, s)
	service := kfService(s, &c, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			return kfToken(), nil
		}
		return kfResponse(map[string]any{"errcode": 0}), nil
	})
	if _, err := service.SyncOne(ctx); err == nil {
		t.Fatal("missing pagination fields accepted")
	}
	state, _ = s.KFState(ctx, source)
	if state.Cursor != "existing-cursor" {
		t.Fatal("cursor reset")
	}
	file := `{"errcode":40007,"errmsg":"this is a legitimate JSON file"}`
	client := &wechatkf.Client{Config: c.WeChatKF[0], HTTP: &http.Client{Transport: kfTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			return kfToken(), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Content-Disposition": {"attachment; filename=example.json"}}, Body: io.NopCloser(strings.NewReader(file))}, nil
	})}}
	data, name, err := client.Download(ctx, inbox.MediaSource{MediaID: "json-file", Name: "file"}, 1024)
	if err != nil || string(data) != file || name != "example.json" {
		t.Fatal("JSON original misclassified as API error", string(data), name, err)
	}
}

func TestKFIncompleteTextKeepsRawAndMissingMessageListKeepsCursor(t *testing.T) {
	for _, part := range []any{nil, map[string]any{}, map[string]any{"content": nil}} {
		s := store(t)
		c := kfConfig()
		c.WeChatKF[0].Accounts[0].Receipt = true
		service := kfService(s, &c, func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/cgi-bin/gettoken" {
				return kfToken(), nil
			}
			return kfPage("next", 0, kfMessage("malformed", "text", part)), nil
		})
		if _, err := service.SyncOne(ctx); err != nil {
			t.Fatal(err)
		}
		ing := inbox.Ingestor{Store: s, Config: func() inbox.Config { return c }, DownloadMedia: service.Download}
		if _, err := ing.ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		var status string
		var raw []byte
		s.DB.QueryRow("SELECT status,payload FROM messages").Scan(&status, &raw)
		var receipts int
		s.DB.QueryRow("SELECT COUNT(*) FROM replies").Scan(&receipts)
		if status != "failed" || len(raw) == 0 || receipts != 0 {
			t.Fatalf("incomplete text falsely archived: status=%s evidence=%d receipts=%d", status, len(raw), receipts)
		}
	}
	for _, nullList := range []bool{false, true} {
		s := store(t)
		c := kfConfig()
		source := kfSource(c)
		s.EnsureKF(ctx, source)
		state, _ := s.KFState(ctx, source)
		s.KFAdvance(ctx, source, state, "existing", false)
		kfDue(t, s)
		service := kfService(s, &c, func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/cgi-bin/gettoken" {
				return kfToken(), nil
			}
			body := map[string]any{"errcode": 0, "next_cursor": "advanced", "has_more": 0}
			if nullList {
				body["msg_list"] = nil
			}
			return kfResponse(body), nil
		})
		if _, err := service.SyncOne(ctx); err == nil {
			t.Error("missing message list accepted")
		}
		state, _ = s.KFState(ctx, source)
		if state.Cursor != "existing" {
			t.Error("advanced cursor without reading message list")
		}
	}
}
