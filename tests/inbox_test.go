package tests

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iCookCloud/wecom-inbox/internal/inbox"
)

var ctx = context.Background()

const adminToken = "test-admin-token-with-at-least-32-characters"
const consumerToken = "test-consumer-token-with-at-least-32-characters"

func config() inbox.Config {
	return inbox.Config{DataDir: "unused", Listen: "127.0.0.1:8787", AdminToken: adminToken, MaxFileMB: 1, Bots: []inbox.Bot{{ID: "bot-a", Name: "Test inbox", Enabled: true, Secret: "test-secret", AllowedUsers: []string{"user-a"}}, {ID: "bot-b", Name: "Private inbox", Enabled: true, Secret: "test-secret", AllowedUsers: []string{"user-b"}}}}
}
func store(t *testing.T) *inbox.Store {
	t.Helper()
	s, e := inbox.OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func input(id, bot, text string) inbox.Incoming {
	return inbox.Incoming{Message: inbox.Message{PlatformID: id, BotID: bot, ChatID: "user-a", ChatType: "single", SenderID: "user-a", Type: "text", Content: text}}
}
func put(t *testing.T, s *inbox.Store, in inbox.Incoming) int64 {
	t.Helper()
	id, _, e := s.Put(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func archive(t *testing.T, s *inbox.Store, in inbox.Incoming, c inbox.Config) int64 {
	t.Helper()
	id := put(t, s, in)
	if e := s.Finalize(ctx, id, c, ""); e != nil {
		t.Fatal(e)
	}
	return id
}
func message(t *testing.T, s *inbox.Store, id int64) inbox.Message {
	t.Helper()
	m, e := s.Get(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	return m
}

func TestConfigRequiresExplicitIdentityAndScopes(t *testing.T) {
	c := config()
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*inbox.Config){func(c *inbox.Config) { c.AdminToken = "short" }, func(c *inbox.Config) { c.Bots[0].AllowedUsers = nil }, func(c *inbox.Config) { c.Bots[1].ID = "bot-a" }, func(c *inbox.Config) { c.MaxFileMB = 100 }, func(c *inbox.Config) {
		c.Consumers = []inbox.Consumer{{ID: "bad", Token: consumerToken, Bots: []string{"missing"}}}
	}} {
		c = config()
		change(&c)
		if c.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestMixedAndQuotedMediaRemainSeparate(t *testing.T) {
	body := []byte(`{"msgid":"m1","chattype":"group","chatid":"chat-a","from":{"userid":"user-a"},"msgtype":"mixed","mixed":{"msg_item":[{"msgtype":"text","text":{"content":"报价资料"}},{"msgtype":"image","image":{"url":"https://example.com/a","aeskey":"A"}},{"msgtype":"image","image":{"url":"https://example.com/b","aeskey":"B"}}]},"quote":{"msgtype":"file","file":{"url":"https://example.com/c","name":"quote.xlsx"}}}`)
	in, e := inbox.ParseIncoming("bot-a", "req", body)
	if e != nil {
		t.Fatal(e)
	}
	if in.Message.Content != "报价资料" || len(in.Media) != 3 || in.Media[2].Kind != "quoted_file" {
		t.Fatalf("lost parts: %+v", in)
	}
	s := store(t)
	c := config()
	ing := inbox.Ingestor{Store: s, Config: func() inbox.Config { return c }}
	id, fresh, e := ing.Accept(ctx, "bot-a", "req", body)
	if e != nil || !fresh {
		t.Fatal(id, fresh, e)
	}
	_, fresh, e = ing.Accept(ctx, "bot-a", "req2", body)
	if e != nil || fresh {
		t.Fatal("duplicate was inserted", e)
	}
	m := message(t, s, id)
	if len(m.Attachments) != 3 || m.Attachments[0].ID == m.Attachments[1].ID {
		t.Fatal("attachment IDs not unique")
	}
	unauthorized := bytes.ReplaceAll(body, []byte("user-a"), []byte("intruder"))
	if _, _, e = ing.Accept(ctx, "bot-a", "req", unauthorized); e == nil {
		t.Fatal("unapproved sender accepted")
	}
	for _, bad := range []string{`{}`, `{"msgid":"x","chattype":"group","from":{"userid":"u"}}`, `{"msgid":"x","chattype":"single","aibotid":"wrong","from":{"userid":"u"}}`} {
		if _, e := inbox.ParseIncoming("bot-a", "req", []byte(bad)); e == nil {
			t.Fatal("invalid message accepted")
		}
	}
}

func TestConcurrentDeduplicationAndEncryptedPending(t *testing.T) {
	s := store(t)
	in := input("duplicate", "bot-a", "hello")
	in.Media = []inbox.MediaSource{{URL: "https://example.com/private?signature=hidden-secret", AESKey: "secret-key", Name: "x", Kind: "file"}}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, e := s.Put(ctx, in); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	p, e := s.List(ctx, inbox.Filter{})
	if e != nil || len(p.Messages) != 1 {
		t.Fatal("dedup failed", e, len(p.Messages))
	}
	var encrypted []byte
	if e = s.DB.QueryRow(`SELECT payload FROM messages`).Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(encrypted, []byte("hidden-secret")) {
		t.Fatal("pending media credentials were not encrypted")
	}
	data, _ := json.Marshal(p)
	if bytes.Contains(data, []byte("signature")) || bytes.Contains(data, []byte("secret-key")) {
		t.Fatal("public projection leaked credential")
	}
}

func encrypt(data, key []byte) []byte {
	pad := 32 - len(data)%32
	padded := append(append([]byte{}, data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(out, padded)
	return out
}

func TestDownloadDecryptPartialFailureAndRestart(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	original := []byte("0123456789abcd")
	if got, e := inbox.DecryptMedia(encrypt(original, key), base64.StdEncoding.EncodeToString(key)); e != nil || !bytes.Equal(got, original) {
		t.Fatal("32-byte padding rejected", e)
	}
	if _, e := inbox.DecryptMedia([]byte{1, 2}, base64.StdEncoding.EncodeToString(key)); e == nil {
		t.Fatal("bad ciphertext accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''%E6%8A%A5%E4%BB%B7.xlsx")
		w.Write(encrypt(original, key))
	}))
	defer server.Close()
	dir := t.TempDir()
	s, e := inbox.OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	in := input("restart", "bot-a", "报价文件")
	in.Media = []inbox.MediaSource{{URL: server.URL + "/good", AESKey: base64.StdEncoding.EncodeToString(key), Name: "a.xlsx", Kind: "file"}, {URL: server.URL + "/missing", Name: "b.xlsx", Kind: "file"}}
	id := put(t, s, in)
	s.Close()
	s, e = inbox.OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	c := config()
	ing := inbox.Ingestor{Store: s, Config: func() inbox.Config { return c }, Downloader: inbox.Downloader{AllowPrivate: true}}
	worked, e := ing.ProcessOne(ctx)
	if !worked || e != nil {
		t.Fatal(worked, e)
	}
	m := message(t, s, id)
	if m.Status != "partial" || m.Attachments[0].Status != "ready" || m.Attachments[1].Status != "failed" || m.Attachments[0].Name != "报价.xlsx" {
		t.Fatalf("unexpected archive: %+v", m)
	}
	data, e := os.ReadFile(filepath.Join(dir, "files", m.Attachments[0].StorageKey))
	if e != nil || !bytes.Equal(data, original) {
		t.Fatal("lost original bytes", e)
	}
	var pending []byte
	if e = s.DB.QueryRow(`SELECT payload FROM messages WHERE id=?`, id).Scan(&pending); e != nil || pending != nil {
		t.Fatal("temporary credentials retained", e)
	}
	if strings.Contains(m.Attachments[1].Error, server.URL) {
		t.Fatal("download error exposed URL")
	}
}

func TestDownloadRejectsOversizeRedirectAndTraversalNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/large", 302)
			return
		}
		w.Write(bytes.Repeat([]byte("x"), 100))
	}))
	defer server.Close()
	d := inbox.Downloader{AllowPrivate: true, MaxBytes: 10}
	for _, path := range []string{"/large", "/redirect"} {
		if _, _, e := d.Download(ctx, inbox.MediaSource{URL: server.URL + path}); e == nil {
			t.Fatal("unsafe download accepted", path)
		}
	}
	if inbox.ValidateURL("http://127.0.0.1", false) == nil || inbox.ValidateURL("https://user:password@example.com", false) == nil {
		t.Fatal("unsafe URL accepted")
	}
	if n := inbox.SafeName("..\\..\\secret.txt"); n != "secret.txt" {
		t.Fatal(n)
	}
}

func TestSearchScopeCursorAndTimestampBoundary(t *testing.T) {
	s := store(t)
	c := config()
	for n := 0; n < 4; n++ {
		in := input(fmt.Sprint(n), "bot-a", "中文报价")
		in.Message.ReceivedAt = fmt.Sprintf("2026-10-23T12:00:00.%09dZ", n)
		archive(t, s, in, c)
	}
	archive(t, s, input("private", "bot-b", "secret"), c)
	p, e := s.List(ctx, inbox.Filter{Query: "报价", AllowedBots: []string{"bot-a"}, Limit: 2, From: "2026-10-23T12:00:00Z"})
	if e != nil || len(p.Messages) != 2 || p.NextCursor == "" {
		t.Fatal("search boundary", p, e)
	}
	archive(t, s, input("new", "bot-a", "中文报价"), c)
	second, e := s.List(ctx, inbox.Filter{Query: "报价", AllowedBots: []string{"bot-a"}, Limit: 2, Cursor: p.NextCursor})
	if e != nil || len(second.Messages) != 2 || second.Messages[0].ID >= p.Messages[1].ID {
		t.Fatal("unstable pagination", e)
	}
	if _, e = s.List(ctx, inbox.Filter{Cursor: "bad"}); e == nil {
		t.Fatal("invalid cursor accepted")
	}
	p, e = s.List(ctx, inbox.Filter{AllowedBots: []string{}})
	if e != nil || len(p.Messages) != 0 {
		t.Fatal("empty scope exposed data")
	}
}

func TestLegacyImportAndBackupRestore(t *testing.T) {
	s := store(t)
	root := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, "quote.csv"), []byte("sku,price\na,12"), 0600); e != nil {
		t.Fatal(e)
	}
	line := `{"ts":"2026-07-31T12:00:00Z","bot_id":"bot-a","msgid":"legacy-1","chat_type":"single","from_userid":"user-a","msgtype":"file","content":"legacy","file_path":"quote.csv","file_name":"报价.csv","raw":{"aeskey":"MUST_NOT_IMPORT"}}`
	report, e := s.ImportJSONL(ctx, strings.NewReader(line+"\ninvalid\n"), root)
	if e != nil || report.Imported != 1 || len(report.InvalidLines) != 1 {
		t.Fatal(report, e)
	}
	report, e = s.ImportJSONL(ctx, strings.NewReader(line), root)
	if e != nil || report.Duplicates != 1 {
		t.Fatal(report, e)
	}
	unsafe := strings.ReplaceAll(strings.ReplaceAll(line, "legacy-1", "legacy-2"), "quote.csv", "../outside.csv")
	report, e = s.ImportJSONL(ctx, strings.NewReader(unsafe), root)
	if e != nil || report.FailedAttachments != 1 {
		t.Fatal("path boundary failed", report, e)
	}
	destination := filepath.Join(t.TempDir(), "backup")
	if e = s.Backup(ctx, destination); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if e = inbox.RestoreBackup(destination, restored); e != nil {
		t.Fatal(e)
	}
	other, e := inbox.OpenStore(restored)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	page, e := other.List(ctx, inbox.Filter{})
	if e != nil || len(page.Messages) != 2 {
		t.Fatal("restore lost messages", e)
	}
	if e = inbox.RestoreBackup(destination, restored); e == nil {
		t.Fatal("restore overwrote existing data")
	}
	if e = os.WriteFile(filepath.Join(destination, "encryption.key"), bytes.Repeat([]byte{1}, 32), 0600); e != nil {
		t.Fatal(e)
	}
	if e = inbox.RestoreBackup(destination, filepath.Join(t.TempDir(), "bad")); e == nil {
		t.Fatal("tampered backup accepted")
	}
	if _, e = os.Stat(filepath.Join(root, "quote.csv")); e != nil {
		t.Fatal("import changed original")
	}
}

func TestWrongKeyAndNewerSchemaFailClosed(t *testing.T) {
	for _, mode := range []string{"key", "schema"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			s, e := inbox.OpenStore(dir)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "schema" {
				s.DB.Exec(`UPDATE schema_version SET version=99`)
			}
			s.Close()
			if mode == "key" {
				os.WriteFile(filepath.Join(dir, "encryption.key"), bytes.Repeat([]byte{4}, 32), 0600)
			}
			other, e := inbox.OpenStore(dir)
			if e == nil {
				other.Close()
				t.Fatal("incompatible state accepted")
			}
		})
	}
}

type fakeSender struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeSender) Send(context.Context, string, string, string, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func TestIndependentConsumersRetryAndResultIdempotency(t *testing.T) {
	s := store(t)
	c := config()
	var seen []string
	var mu sync.Mutex
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		body.ReadFrom(r.Body)
		stamp := r.Header.Get("X-WeCom-Inbox-Timestamp")
		if r.Header.Get("X-WeCom-Inbox-Signature") != inbox.WebhookSignature(strings.Repeat("s", 32), stamp, body.Bytes()) {
			t.Error("invalid signature")
		}
		mu.Lock()
		seen = append(seen, r.Header.Get("X-WeCom-Inbox-Event"))
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	c.Consumers = []inbox.Consumer{{ID: "good", AllowReply: true, URL: good.URL, Secret: strings.Repeat("s", 32), Token: consumerToken, Bots: []string{"bot-a"}, AllowPrivateNetwork: true}, {ID: "bad", URL: bad.URL, Secret: strings.Repeat("t", 32), Token: strings.Repeat("b", 32), Bots: []string{"bot-a"}, AllowPrivateNetwork: true}}
	id := archive(t, s, input("dispatch", "bot-a", "hello"), c)
	d := inbox.Dispatcher{Store: s, Config: func() inbox.Config { return c }}
	for range 9 {
		if _, e := d.DeliverOne(ctx); e != nil {
			t.Fatal(e)
		}
		s.DB.Exec(`UPDATE deliveries SET next_at='2000-01-01T00:00:00Z' WHERE status='pending'`)
	}
	deliveries, e := s.Deliveries(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range deliveries {
		if v.ConsumerID == "good" && v.Status != "delivered" {
			t.Fatal("healthy consumer blocked")
		}
		if v.ConsumerID == "bad" && (v.Status != "failed" || v.Attempts != 5) {
			t.Fatal("retry limit failed", v)
		}
	}
	var event string
	for _, v := range deliveries {
		if v.ConsumerID == "good" {
			event = v.ID
			if e = s.RetryDelivery(ctx, v.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e = d.DeliverOne(ctx); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	if len(seen) != 2 || seen[0] != event || seen[1] != event {
		t.Fatal("replay changed event ID", seen)
	}
	mu.Unlock()
	in := inbox.ResultInput{Key: "job-1", Status: "completed", Summary: "处理完成", Reply: true}
	r, fresh, e := s.AddResult(ctx, id, "good", in)
	if e != nil || !fresh {
		t.Fatal(r, e)
	}
	again, fresh, e := s.AddResult(ctx, id, "good", in)
	if e != nil || fresh || again.ID != r.ID {
		t.Fatal("result duplicate", e)
	}
	in.Summary = "different"
	if _, _, e = s.AddResult(ctx, id, "good", in); !errors.Is(e, inbox.ErrConflict) {
		t.Fatal("conflicting replay accepted", e)
	}
	sender := &fakeSender{err: inbox.ErrUncertain}
	if _, e = d.ReplyOne(ctx, sender); e != nil {
		t.Fatal(e)
	}
	if _, e = d.ReplyOne(ctx, sender); e != nil {
		t.Fatal(e)
	}
	if sender.calls != 1 {
		t.Fatal("uncertain send retried")
	}
	r, e = s.Result(ctx, r.ID)
	if e != nil || r.ReplyStatus != "uncertain" {
		t.Fatal(r, e)
	}
}

func TestCancelledDownloadRemainsRecoverable(t *testing.T) {
	s := store(t)
	c := config()
	in := input("cancel", "bot-a", "hello")
	in.Media = []inbox.MediaSource{{URL: "https://example.com/file", Name: "file", Kind: "file"}}
	id := put(t, s, in)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	ing := inbox.Ingestor{Store: s, Config: func() inbox.Config { return c }}
	_, _ = ing.ProcessOne(cancelled)
	if message(t, s, id).Status != "pending" {
		t.Fatal("cancelled work was lost")
	}
}

func TestPendingSurvivesBackup(t *testing.T) {
	s := store(t)
	in := input("pending", "bot-a", "pending")
	in.Media = []inbox.MediaSource{{URL: "https://example.com/expiring", AESKey: "test", Name: "a", Kind: "file"}}
	put(t, s, in)
	backup := filepath.Join(t.TempDir(), "backup")
	if e := s.Backup(ctx, backup); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if e := inbox.RestoreBackup(backup, restored); e != nil {
		t.Fatal(e)
	}
	other, e := inbox.OpenStore(restored)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	_, got, e := other.Pending(ctx)
	if e != nil || got.Media[0].URL != in.Media[0].URL {
		t.Fatal("encrypted pending work not restored", e)
	}
}

func TestFailedConsumerDoesNotBlockOfflineBot(t *testing.T) {
	s := store(t)
	c := config()
	first := archive(t, s, input("a", "bot-a", "one"), c)
	second := archive(t, s, input("b", "bot-a", "two"), c)
	for _, id := range []int64{first, second} {
		s.AddResult(ctx, id, "admin", inbox.ResultInput{Key: "x", Status: "completed", Summary: "done", Reply: true})
	}
	d := inbox.Dispatcher{Store: s, Config: func() inbox.Config { return c }}
	sender := &fakeSender{err: inbox.ErrOffline}
	d.ReplyOne(ctx, sender)
	sender.err = nil
	d.ReplyOne(ctx, sender)
	rs, e := s.Results(ctx, second)
	if e != nil || rs[0].ReplyStatus != "delivered" {
		t.Fatal("offline reply blocks next message", e)
	}
}

func TestReviewRecoverRenamedAttachmentAfterCommitFailure(t *testing.T) {
	s := store(t)
	expired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) }))
	defer expired.Close()
	in := input("rename-before-commit", "bot-a", "")
	in.Media = []inbox.MediaSource{{URL: expired.URL, Name: "source.txt", Kind: "file"}}
	id := put(t, s, in)
	a := message(t, s, id).Attachments[0]
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.WriteAttachment(cancelled, a, []byte("irreplaceable source"), a.Name); err == nil {
		t.Fatal("expected DB commit cancellation")
	}
	path := filepath.Join(s.Dir, "files", a.ID+".blob")
	if data, err := os.ReadFile(path); err != nil || string(data) != "irreplaceable source" {
		t.Fatalf("setup: durable source missing: %q %v", data, err)
	}
	ing := inbox.Ingestor{Store: s, Config: config, Downloader: inbox.Downloader{AllowPrivate: true}}
	if _, err := ing.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	m := message(t, s, id)
	if m.Status != "ready" || m.Attachments[0].Status != "ready" {
		t.Fatalf("durable source orphaned after recovery: message=%s attachment=%+v", m.Status, m.Attachments[0])
	}
}

func TestReviewImportResumesAfterArchiveWriteFailure(t *testing.T) {
	s := store(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(s.Dir, "files")
	if err := os.WriteFile(blocker, []byte("blocks mkdir"), 0600); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"2026-07-31T12:00:00Z","bot_id":"bot-a","msgid":"interrupted-import","chat_type":"single","from_userid":"user-a","msgtype":"file","file_path":"source.txt"}`
	if _, err := s.ImportJSONL(ctx, strings.NewReader(line), root); err == nil {
		t.Fatal("expected archive write failure")
	}
	if err := os.Rename(blocker, blocker+".saved"); err != nil {
		t.Fatal(err)
	}
	report, err := s.ImportJSONL(ctx, strings.NewReader(line), root)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.List(ctx, inbox.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	m := page.Messages[0]
	if m.Status != "ready" || m.Attachments[0].Status != "ready" {
		t.Fatalf("rerun skipped incomplete import: report=%+v message=%s attachment=%s", report, m.Status, m.Attachments[0].Status)
	}
}

func TestReviewMissingReadyFileCannotFinalizeSuccess(t *testing.T) {
	s := store(t)
	in := input("missing-ready", "bot-a", "")
	in.Media = []inbox.MediaSource{{Name: "source.txt", Kind: "file"}}
	id := put(t, s, in)
	a := message(t, s, id).Attachments[0]
	if err := s.WriteAttachment(ctx, a, []byte("original"), a.Name); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "files", a.ID+".blob")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	ing := inbox.Ingestor{Store: s, Config: config}
	_, err := ing.ProcessOne(ctx)
	m := message(t, s, id)
	if err == nil && m.Status == "ready" {
		t.Fatal("missing source finalized as ready")
	}
}

func TestReviewBackupRejectsChangedArchivedBytes(t *testing.T) {
	s := store(t)
	in := input("corrupted-archive", "bot-a", "")
	in.Media = []inbox.MediaSource{{Name: "source.txt", Kind: "file"}}
	id := put(t, s, in)
	a := message(t, s, id).Attachments[0]
	if err := s.WriteAttachment(ctx, a, []byte("original"), a.Name); err != nil {
		t.Fatal(err)
	}
	if err := s.Finalize(ctx, id, config(), ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "files", a.ID+".blob")
	if err := os.WriteFile(path, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err := s.Backup(ctx, backup); err != nil {
		return
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := inbox.RestoreBackup(backup, restored); err != nil {
		return
	}
	other, err := inbox.OpenStore(restored)
	if err != nil {
		return
	}
	defer other.Close()
	got := message(t, other, id).Attachments[0]
	data, err := os.ReadFile(filepath.Join(restored, "files", got.StorageKey))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" && got.Status == "ready" {
		t.Fatalf("backup/restore succeeded with changed bytes: data=%q catalogHash=%s", data, got.SHA256)
	}
}
