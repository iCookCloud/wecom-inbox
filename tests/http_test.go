package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iCookCloud/wecom-inbox/internal/inbox"
	"github.com/iCookCloud/wecom-inbox/web"
)

func request(t *testing.T, client *http.Client, method, url, token, body string) (*http.Response, []byte) {
	t.Helper()
	r, e := http.NewRequest(method, url, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	return resp, data
}

func TestHTTPAuthenticationScopeAndExports(t *testing.T) {
	s := store(t)
	c := config()
	c.Consumers = []inbox.Consumer{{ID: "reader", Token: consumerToken, Bots: []string{"bot-a"}, AllowReply: false}}
	a := archive(t, s, input("public", "bot-a", "=SUM(1,2)"), c)
	b := archive(t, s, input("private", "bot-b", "PRIVATE CONTENT"), c)
	server := httptest.NewServer((&inbox.Server{Store: s, Config: func() inbox.Config { return c }, Assets: web.Assets}).Handler())
	defer server.Close()
	for _, path := range []string{"/v1/messages", "/v1/stats", "/v1/bots", "/v1/export?format=json"} {
		r, _ := request(t, server.Client(), "GET", server.URL+path, "", "")
		if r.StatusCode != 401 {
			t.Fatal(path, r.StatusCode)
		}
	}
	r, body := request(t, server.Client(), "GET", server.URL+"/v1/messages", consumerToken, "")
	if r.StatusCode != 200 || strings.Contains(string(body), "PRIVATE CONTENT") {
		t.Fatal("scope leaked", string(body))
	}
	r, _ = request(t, server.Client(), "GET", server.URL+"/v1/messages/"+itoa(b), consumerToken, "")
	if r.StatusCode != 404 {
		t.Fatal("unscoped detail", r.StatusCode)
	}
	r, body = request(t, server.Client(), "GET", server.URL+"/v1/bots", adminToken, "")
	if r.StatusCode != 200 || strings.Contains(string(body), "test-secret") {
		t.Fatal("bot secret exposed")
	}
	r, body = request(t, server.Client(), "GET", server.URL+"/v1/consumers", adminToken, "")
	if r.StatusCode != 200 || strings.Contains(string(body), consumerToken) {
		t.Fatal("consumer secret exposed")
	}
	r, body = request(t, server.Client(), "GET", server.URL+"/v1/export?format=csv", consumerToken, "")
	if r.StatusCode != 200 || !strings.Contains(string(body), "'=SUM") || strings.Contains(string(body), "PRIVATE CONTENT") {
		t.Fatal("CSV unsafe", string(body))
	}
	r, body = request(t, server.Client(), "GET", server.URL+"/v1/export?format=json", consumerToken, "")
	var exported []inbox.Message
	if r.StatusCode != 200 || json.Unmarshal(body, &exported) != nil || len(exported) != 1 {
		t.Fatal("JSON export", string(body))
	}
	result := `{"idempotency_key":"test","status":"completed","summary":"done","reply":true}`
	r, _ = request(t, server.Client(), "POST", server.URL+"/v1/messages/"+itoa(a)+"/results", consumerToken, result)
	if r.StatusCode != 403 {
		t.Fatal("reply permission bypass", r.StatusCode)
	}
	result = strings.Replace(result, `"reply":true`, `"reply":false`, 1)
	r, _ = request(t, server.Client(), "POST", server.URL+"/v1/messages/"+itoa(a)+"/results", consumerToken, result)
	if r.StatusCode != 201 {
		t.Fatal("result write", r.StatusCode)
	}
	r, body = request(t, server.Client(), "GET", server.URL+"/", "", "")
	if r.StatusCode != 200 || !strings.Contains(string(body), "WeCom Inbox") {
		t.Fatal("missing UI")
	}
	if r.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
}

func TestBrowserCookieCSRFAndLoginOrigin(t *testing.T) {
	s := store(t)
	c := config()
	id := archive(t, s, input("one", "bot-a", "hello"), c)
	server := httptest.NewServer((&inbox.Server{Store: s, Config: func() inbox.Config { return c }}).Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body, _ := json.Marshal(map[string]string{"token": adminToken})
	r, _ := http.NewRequest("POST", server.URL+"/login", strings.NewReader(string(body)))
	r.Header.Set("Origin", "https://evil.example")
	resp, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin login accepted")
	}
	resp, data := request(t, client, "POST", server.URL+"/login", "", string(body))
	if resp.StatusCode != 200 {
		t.Fatal(string(data))
	}
	var auth map[string]string
	json.Unmarshal(data, &auth)
	if auth["csrf"] == "" {
		t.Fatal("missing csrf")
	}
	for _, cookie := range resp.Cookies() {
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatal("unsafe session cookie")
		}
	}
	result := `{"idempotency_key":"browser","status":"completed","summary":"done","reply":false}`
	resp, _ = request(t, client, "POST", server.URL+"/v1/messages/"+itoa(id)+"/results", "", result)
	if resp.StatusCode != 403 {
		t.Fatal("missing CSRF allowed")
	}
	r, _ = http.NewRequest("POST", server.URL+"/v1/messages/"+itoa(id)+"/results", strings.NewReader(result))
	r.Header.Set("X-CSRF-Token", auth["csrf"])
	r.Header.Set("Origin", server.URL)
	resp, e = client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatal(resp.StatusCode)
	}
	r, _ = http.NewRequest("POST", server.URL+"/logout", strings.NewReader("{}"))
	r.Header.Set("X-CSRF-Token", auth["csrf"])
	resp, e = client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	resp, _ = request(t, client, "GET", server.URL+"/v1/messages", "", "")
	if resp.StatusCode != 401 {
		t.Fatal("logout cookie survived")
	}
}

func TestAttachmentDownloadAndConsumerResultIsolation(t *testing.T) {
	s := store(t)
	c := config()
	c.Consumers = []inbox.Consumer{{ID: "reader", Token: consumerToken, Bots: []string{"bot-a"}}}
	in := input("file", "bot-a", "document")
	in.Media = []inbox.MediaSource{{Name: "<unsafe>.html", Kind: "file"}}
	id := put(t, s, in)
	m := message(t, s, id)
	if e := s.WriteAttachment(ctx, m.Attachments[0], []byte("<script>alert(1)</script>"), "<unsafe>.html"); e != nil {
		t.Fatal(e)
	}
	s.Finalize(ctx, id, c, "")
	s.AddResult(ctx, id, "another-consumer", inbox.ResultInput{Key: "secret", Status: "completed", Summary: "another consumer private output"})
	server := httptest.NewServer((&inbox.Server{Store: s, Config: func() inbox.Config { return c }}).Handler())
	defer server.Close()
	r, data := request(t, server.Client(), "GET", server.URL+"/v1/attachments/"+m.Attachments[0].ID, consumerToken, "")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment") || r.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatal("unsafe download", r.Header)
	}
	if string(data) != "<script>alert(1)</script>" {
		t.Fatal("download changed source bytes")
	}
	r, data = request(t, server.Client(), "GET", server.URL+"/v1/messages/"+itoa(id), consumerToken, "")
	if strings.Contains(string(data), "another consumer private output") {
		t.Fatal("consumer result leaked")
	}
	c.Consumers[0].Bots = []string{"bot-b"}
	r, _ = request(t, server.Client(), "GET", server.URL+"/v1/attachments/"+m.Attachments[0].ID, consumerToken, "")
	if r.StatusCode != 404 {
		t.Fatal("attachment scope leaked")
	}
}

func itoa(v int64) string { b, _ := json.Marshal(v); return string(b) }

func TestSecurityQueuedReplyRechecksRevokedConsumer(t *testing.T) {
	for _, revoke := range []string{"allow_reply", "consumer", "bot_scope"} {
		t.Run(revoke, func(t *testing.T) {
			s := store(t)
			c := config()
			c.Consumers = []inbox.Consumer{{ID: "reader", Token: consumerToken, Bots: []string{"bot-a"}, AllowReply: true}}
			id := archive(t, s, input("queued", "bot-a", "hello"), c)
			server := httptest.NewServer((&inbox.Server{Store: s, Config: func() inbox.Config { return c }}).Handler())
			defer server.Close()
			resp, body := request(t, server.Client(), "POST", server.URL+"/v1/messages/"+itoa(id)+"/results", consumerToken, `{"idempotency_key":"queued","status":"completed","summary":"send after revocation","reply":true}`)
			if resp.StatusCode != 201 {
				t.Fatalf("queue: %d %s", resp.StatusCode, body)
			}
			dispatcher := inbox.Dispatcher{Store: s, Config: func() inbox.Config { return c }}
			sender := &fakeSender{err: inbox.ErrOffline}
			if _, err := dispatcher.ReplyOne(ctx, sender); err != nil {
				t.Fatal(err)
			}
			switch revoke {
			case "allow_reply":
				c.Consumers[0].AllowReply = false
			case "consumer":
				c.Consumers = nil
			case "bot_scope":
				c.Consumers[0].Bots = []string{"bot-b"}
			}
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(`UPDATE replies SET next_at=''`); err != nil {
				t.Fatal(err)
			}
			sender.err = nil
			if _, err := dispatcher.ReplyOne(ctx, sender); err != nil {
				t.Fatal(err)
			}
			if sender.calls != 1 {
				t.Fatalf("revoked consumer caused a send: calls=%d", sender.calls)
			}
		})
	}
}

func TestSecurityConsumerAdminResultIsolation(t *testing.T) {
	s := store(t)
	c := config()
	c.Consumers = []inbox.Consumer{{ID: "admin", Token: consumerToken, Bots: []string{"bot-a"}}}
	// 禁止保留 ID 或对管理员结果单独隔离均可。
	if err := c.Validate(); err != nil {
		return
	}
	id := archive(t, s, input("admin-result", "bot-a", "hello"), c)
	server := httptest.NewServer((&inbox.Server{Store: s, Config: func() inbox.Config { return c }}).Handler())
	defer server.Close()
	resp, body := request(t, server.Client(), "POST", server.URL+"/v1/messages/"+itoa(id)+"/results", adminToken, `{"idempotency_key":"admin-private","status":"completed","summary":"ADMIN-ONLY-SUMMARY","reply":false}`)
	if resp.StatusCode != 201 {
		t.Fatalf("admin result: %d %s", resp.StatusCode, body)
	}
	resp, body = request(t, server.Client(), "GET", server.URL+"/v1/messages/"+itoa(id), consumerToken, "")
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if strings.Contains(string(body), "ADMIN-ONLY-SUMMARY") {
		t.Fatalf("consumer reads administrator result: %s", body)
	}
}
