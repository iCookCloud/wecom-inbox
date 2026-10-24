package tests

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/iCookCloud/wecom-inbox/internal/inbox"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/iCookCloud/wecom-inbox/internal/wecom"
)

func TestWeComAuthenticatedReceiveAndAcknowledgedSend(t *testing.T) {
	var mu sync.Mutex
	commands := []string{}
	received := make(chan string, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		for {
			var f map[string]json.RawMessage
			if ws.ReadJSON(&f) != nil {
				return
			}
			var cmd string
			json.Unmarshal(f["cmd"], &cmd)
			var headers map[string]string
			json.Unmarshal(f["headers"], &headers)
			mu.Lock()
			commands = append(commands, cmd)
			mu.Unlock()
			ws.WriteJSON(map[string]any{"headers": headers, "errcode": 0, "errmsg": "ok"})
			if cmd == "aibot_subscribe" {
				ws.WriteJSON(map[string]any{"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": "source-request"}, "body": map[string]any{"msgid": "received", "msgtype": "text", "chattype": "single", "from": map[string]string{"userid": "user-a"}, "text": map[string]string{"content": "hello"}}})
			}
		}
	}))
	defer server.Close()
	c := wecom.New(config().Bots[0], func(_ context.Context, bot, req string, body []byte) error { received <- req; return nil })
	c.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	c.Start(ctx)
	defer c.Stop()
	select {
	case req := <-received:
		if req != "source-request" {
			t.Fatal(req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("callback not delivered")
	}
	if c.Status().State != "connected" {
		t.Fatal("not authenticated")
	}
	if e := c.Send(ctx, "bot-a", "user-a", "single", "hello", "source-request"); e != nil {
		t.Fatal(e)
	}
	if e := c.Send(ctx, "bot-a", "user-a", "single", "done", ""); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 3 || commands[1] != "aibot_respond_msg" || commands[2] != "aibot_send_msg" {
		t.Fatal(commands)
	}
}

func TestWeComRejectedAuthenticationDoesNotClaimConnected(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		var f map[string]json.RawMessage
		ws.ReadJSON(&f)
		var headers map[string]string
		json.Unmarshal(f["headers"], &headers)
		ws.WriteJSON(map[string]any{"headers": headers, "errcode": 853000, "errmsg": "denied"})
	}))
	defer server.Close()
	c := wecom.New(config().Bots[0], nil)
	c.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	c.Start(ctx)
	defer c.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.Status().State == "auth_failed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(c.Status())
}

func TestLocalTransportToIndependentPythonConsumerAndReply(t *testing.T) {
	if _, e := exec.LookPath("python3"); e != nil {
		t.Fatal("python3 is required for the standalone consumer acceptance test")
	}
	s := store(t)
	c := config()
	c.Bots = c.Bots[:1]
	key := bytes.Repeat([]byte{5}, 32)
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="quote.csv"`)
		w.Write(encrypt([]byte("sku,price\nA,12\n"), key))
	}))
	defer media.Close()
	portListener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := portListener.Addr().(*net.TCPAddr).Port
	portListener.Close()
	workerURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	c.Consumers = []inbox.Consumer{{ID: "python-worker", URL: workerURL + "/inbox", Secret: strings.Repeat("s", 32), Token: consumerToken, Bots: []string{"bot-a"}, AllowReply: true, AllowPrivateNetwork: true}}
	apiServer := httptest.NewServer((&inbox.Server{Store: s, Config: func() inbox.Config { return c }}).Handler())
	defer apiServer.Close()
	processCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	bootstrap := `import runpy, socket, sys
def reject_reverse_dns(*args, **kwargs):
    raise RuntimeError("loopback consumer must not require reverse DNS")
socket.getfqdn = reject_reverse_dns
runpy.run_path(sys.argv[1], run_name="__main__")`
	command := exec.CommandContext(processCtx, "python3", "-u", "-c", bootstrap, filepath.Join("..", "examples", "http-consumer", "consumer.py"))
	command.Env = append(os.Environ(), "INBOX_URL="+apiServer.URL, "INBOX_TOKEN="+consumerToken, "WEBHOOK_SECRET="+strings.Repeat("s", 32), "CONSUMER_PORT="+strconv.Itoa(port), "CONSUMER_DATA="+t.TempDir(), "INBOX_REPLY=true")
	consumerLogPath := filepath.Join(t.TempDir(), "consumer.log")
	consumerLog, e := os.OpenFile(consumerLogPath, os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	command.Stdout, command.Stderr = consumerLog, consumerLog
	if e = command.Start(); e != nil {
		consumerLog.Close()
		t.Fatal(e)
	}
	var stopOnce sync.Once
	stopConsumer := func() { stopOnce.Do(func() { cancel(); _ = command.Wait(); _ = consumerLog.Close() }) }
	defer stopConsumer()
	client := http.Client{Timeout: time.Second}
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		response, e := client.Get(workerURL + "/healthz")
		if e == nil {
			response.Body.Close()
			ready = response.StatusCode == 200
			if ready {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		stopConsumer()
		details, _ := os.ReadFile(consumerLogPath)
		t.Fatalf("independent Python consumer did not become ready (%s): %s", command.Path, details)
	}
	ing := inbox.Ingestor{Store: s, Config: func() inbox.Config { return c }, Downloader: inbox.Downloader{AllowPrivate: true}}
	accepted := make(chan int64, 1)
	replied := make(chan string, 1)
	upgrader := websocket.Upgrader{}
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		for {
			var f map[string]json.RawMessage
			if ws.ReadJSON(&f) != nil {
				return
			}
			var cmd string
			json.Unmarshal(f["cmd"], &cmd)
			var headers map[string]string
			json.Unmarshal(f["headers"], &headers)
			if cmd == "aibot_send_msg" {
				replied <- string(f["body"])
			}
			ws.WriteJSON(map[string]any{"headers": headers, "errcode": 0})
			if cmd == "aibot_subscribe" {
				ws.WriteJSON(map[string]any{"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": "workflow-request"}, "body": map[string]any{"msgid": "workflow-file", "chattype": "single", "msgtype": "file", "from": map[string]string{"userid": "user-a"}, "file": map[string]string{"url": media.URL, "aeskey": base64.StdEncoding.EncodeToString(key), "name": "quote.csv"}}})
			}
		}
	}))
	defer platform.Close()
	bot := wecom.New(c.Bots[0], func(ctx context.Context, b, r string, body []byte) error {
		id, _, e := ing.Accept(ctx, b, r, body)
		if e == nil {
			accepted <- id
		}
		return e
	})
	bot.URL = "ws" + strings.TrimPrefix(platform.URL, "http")
	bot.Start(ctx)
	defer bot.Stop()
	var id int64
	select {
	case id = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("transport did not persist callback")
	}
	if _, e = ing.ProcessOne(ctx); e != nil {
		t.Fatal(e)
	}
	d := inbox.Dispatcher{Store: s, Config: func() inbox.Config { return c }}
	if _, e = d.DeliverOne(ctx); e != nil {
		t.Fatal(e)
	}
	deliveries, e := s.Deliveries(ctx, id)
	if e != nil || len(deliveries) != 1 || deliveries[0].Status != "delivered" {
		t.Fatalf("consumer did not acknowledge: %+v %v", deliveries, e)
	}
	results, e := s.Results(ctx, id)
	if e != nil || len(results) != 1 || results[0].Status != "completed" || !strings.Contains(results[0].Summary, "verified 1 attachment") {
		t.Fatal("independent consumer failed to verify original", results, e)
	}
	if _, e = d.ReplyOne(ctx, bot); e != nil {
		t.Fatal(e)
	}
	select {
	case body := <-replied:
		if !strings.Contains(body, "user-a") || !strings.Contains(body, "verified 1 attachment") {
			t.Fatal("wrong reply destination or content", body)
		}
	case <-time.After(time.Second):
		t.Fatal("platform reply not sent")
	}
	results, e = s.Results(ctx, id)
	if e != nil || results[0].ReplyStatus != "delivered" {
		t.Fatal("acknowledged reply status not recorded", e)
	}
}

func TestBotSessionsUseSignedCLIGatewayForAdministratorsOnly(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/auth":
			raw := "test-secret" + "bot-a" + strconv.FormatInt(int64(body["time"].(float64)), 10) + body["nonce"].(string)
			if body["bot_id"] != "bot-a" || body["signature"] != fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) {
				json.NewEncoder(w).Encode(map[string]any{"errcode": 40001, "errmsg": "bad signature"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "token": "cli-token"})
		case "/cli/message/aibot/sessions/list":
			if r.Header.Get("Authorization") != "Bearer cli-token" || body["payload"] != "{}" {
				w.WriteHeader(401)
				return
			}
			result, _ := json.Marshal(map[string]any{"security_notice": "x", "sessions": []map[string]string{{"chat_id": "group-1", "chat_name": "资料群", "chat_type": "group", "last_msg_time": "2026-10-24 15:44:38"}}})
			wrapped, _ := json.Marshal(map[string]string{"result": string(result)})
			json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "results_json": string(wrapped)})
		}
	}))
	defer gateway.Close()
	api := wecom.CLIAPI{HTTP: gateway.Client(), AuthURL: gateway.URL + "/auth", BaseURL: gateway.URL + "/cli"}
	c := config()
	c.Consumers = []inbox.Consumer{{ID: "reader", Token: consumerToken, Bots: []string{"bot-a"}}}
	server := httptest.NewServer((&inbox.Server{Store: store(t), Config: func() inbox.Config { return c }, Sessions: api.Sessions}).Handler())
	defer server.Close()
	r, body := request(t, server.Client(), "GET", server.URL+"/v1/bots/bot-a/sessions", adminToken, "")
	if r.StatusCode != 200 || !strings.Contains(string(body), "资料群") || strings.Contains(string(body), "security_notice") {
		t.Fatalf("admin sessions: %d %s", r.StatusCode, body)
	}
	if r, _ = request(t, server.Client(), "GET", server.URL+"/v1/bots/bot-a/sessions", consumerToken, ""); r.StatusCode != 403 {
		t.Fatalf("consumer listed sessions: %d", r.StatusCode)
	}
}
