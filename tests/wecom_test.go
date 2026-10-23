package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
