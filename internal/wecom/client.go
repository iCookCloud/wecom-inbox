package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/iCookCloud/wecom-inbox/internal/inbox"
)

const Endpoint = "wss://openws.work.weixin.qq.com"

type frame struct {
	Cmd     string `json:"cmd,omitempty"`
	Headers struct {
		RequestID string `json:"req_id"`
	} `json:"headers"`
	Body  json.RawMessage `json:"body,omitempty"`
	Code  *int            `json:"errcode,omitempty"`
	Error string          `json:"errmsg,omitempty"`
}

type Status struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	LastMessage string `json:"last_message,omitempty"`
	Error       string `json:"error,omitempty"`
}

type Client struct {
	Bot       inbox.Bot
	URL       string
	OnMessage func(context.Context, string, string, []byte) error
	Logger    *slog.Logger
	mu        sync.Mutex
	writeMu   sync.Mutex
	ws        *websocket.Conn
	pending   map[string]chan frame
	status    Status
	cancel    context.CancelFunc
	done      chan struct{}
}

func New(bot inbox.Bot, handler func(context.Context, string, string, []byte) error) *Client {
	return &Client{Bot: bot, URL: Endpoint, OnMessage: handler, Logger: slog.Default(), pending: map[string]chan frame{}, status: Status{ID: bot.ID, Name: bot.Name, State: "disconnected"}}
}
func (c *Client) Status() Status { c.mu.Lock(); defer c.mu.Unlock(); return c.status }
func (c *Client) setState(state, err string) {
	c.mu.Lock()
	c.status.State = state
	c.status.Error = err
	c.mu.Unlock()
}
func (c *Client) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		backoff := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			started := time.Now()
			err := c.connect(ctx)
			if ctx.Err() != nil {
				c.setState("stopped", "")
				return
			}
			c.setState("disconnected", err.Error())
			if errors.Is(err, errAuth) {
				c.setState("auth_failed", err.Error())
				return
			}
			if time.Since(started) > 5*time.Minute {
				backoff = time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 60*time.Second)
		}
	}()
}
func (c *Client) Stop() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
	}
}

var errAuth = errors.New("authentication rejected")

func (c *Client) connect(ctx context.Context) error {
	c.setState("connecting", "")
	ws, _, err := (&websocket.Dialer{HandshakeTimeout: 15 * time.Second}).DialContext(ctx, c.URL, nil)
	if err != nil {
		return errors.New("WebSocket connection failed")
	}
	defer ws.Close()
	ws.SetReadLimit(1 << 20)
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-connectionCtx.Done(); ws.Close() }()
	requestID := inbox.RandomID()
	f := frame{Cmd: "aibot_subscribe"}
	f.Headers.RequestID = requestID
	f.Body, _ = json.Marshal(map[string]string{"bot_id": c.Bot.ID, "secret": c.Bot.Secret})
	if err = c.write(ws, f); err != nil {
		return errors.New("subscription write failed")
	}
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		return errors.New("subscription acknowledgement timed out")
	}
	var ack frame
	if json.Unmarshal(data, &ack) != nil || ack.Headers.RequestID != requestID || ack.Code == nil {
		return errors.New("invalid subscription acknowledgement")
	}
	if *ack.Code != 0 {
		return fmt.Errorf("%w (code %d)", errAuth, *ack.Code)
	}
	c.mu.Lock()
	c.ws = ws
	c.status.State = "connected"
	c.status.Error = ""
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.ws == ws {
			c.ws = nil
		}
		c.mu.Unlock()
	}()
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-connectionCtx.Done():
				return
			case <-ticker.C:
				if e := c.call(connectionCtx, "ping", inbox.RandomID(), nil); e != nil {
					ws.Close()
					return
				}
			}
		}
	}()
	for {
		ws.SetReadDeadline(time.Now().Add(75 * time.Second))
		_, data, err = ws.ReadMessage()
		if err != nil {
			return errors.New("WebSocket connection closed")
		}
		var f frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.Cmd == "aibot_msg_callback" {
			if c.OnMessage != nil {
				if e := c.OnMessage(connectionCtx, c.Bot.ID, f.Headers.RequestID, f.Body); e != nil {
					c.Logger.Warn("message was not accepted", "bot", c.Bot.ID, "error", e.Error())
				} else {
					c.mu.Lock()
					c.status.LastMessage = time.Now().UTC().Format(time.RFC3339)
					c.mu.Unlock()
				}
			}
		} else if f.Code != nil {
			c.mu.Lock()
			ch := c.pending[f.Headers.RequestID]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- f:
				default:
				}
			}
		}
	}
}

func (c *Client) write(ws *websocket.Conn, f frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return ws.WriteJSON(f)
}

func (c *Client) call(ctx context.Context, command, requestID string, body any) error {
	c.mu.Lock()
	ws := c.ws
	if ws == nil {
		c.mu.Unlock()
		return inbox.ErrOffline
	}
	if _, ok := c.pending[requestID]; ok {
		c.mu.Unlock()
		return errors.New("request already pending")
	}
	ch := make(chan frame, 1)
	c.pending[requestID] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, requestID); c.mu.Unlock() }()
	f := frame{Cmd: command}
	f.Headers.RequestID = requestID
	if body != nil {
		f.Body, _ = json.Marshal(body)
	}
	if err := c.write(ws, f); err != nil {
		return inbox.ErrUncertain
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return inbox.ErrUncertain
	case <-timer.C:
		return inbox.ErrUncertain
	case ack := <-ch:
		if ack.Code == nil {
			return inbox.ErrUncertain
		}
		if *ack.Code != 0 {
			return fmt.Errorf("platform rejected message (code %d)", *ack.Code)
		}
		return nil
	}
}

func (c *Client) Send(ctx context.Context, _ string, target, chatType, text, requestID string) error {
	if requestID != "" {
		return c.call(ctx, "aibot_respond_msg", requestID, map[string]any{"msgtype": "stream", "stream": map[string]any{"id": inbox.RandomID(), "finish": true, "content": text}})
	}
	return c.call(ctx, "aibot_send_msg", inbox.RandomID(), map[string]any{"chatid": target, "msgtype": "markdown", "markdown": map[string]string{"content": text}})
}

type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client
	handler func(context.Context, string, string, []byte) error
}

func NewManager(handler func(context.Context, string, string, []byte) error) *Manager {
	return &Manager{clients: map[string]*Client{}, handler: handler}
}
func (m *Manager) Reconcile(ctx context.Context, bots []inbox.Bot) {
	wanted := map[string]inbox.Bot{}
	for _, b := range bots {
		if b.Enabled {
			wanted[b.ID] = b
		}
	}
	m.mu.Lock()
	stop := []*Client{}
	for id, c := range m.clients {
		b, ok := wanted[id]
		if !ok || b.Secret != c.Bot.Secret {
			stop = append(stop, c)
			delete(m.clients, id)
		}
	}
	m.mu.Unlock()
	for _, c := range stop {
		c.Stop()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, b := range wanted {
		if _, ok := m.clients[id]; !ok {
			c := New(b, m.handler)
			m.clients[id] = c
			c.Start(ctx)
		}
	}
}
func (m *Manager) Statuses() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Status{}
	for _, c := range m.clients {
		out = append(out, c.Status())
	}
	return out
}
func (m *Manager) Send(ctx context.Context, botID, target, chatType, text, requestID string) error {
	m.mu.RLock()
	c := m.clients[botID]
	m.mu.RUnlock()
	if c == nil {
		return inbox.ErrOffline
	}
	return c.Send(ctx, botID, target, chatType, text, requestID)
}
func (m *Manager) Stop() {
	m.mu.Lock()
	all := m.clients
	m.clients = map[string]*Client{}
	m.mu.Unlock()
	for _, c := range all {
		c.Stop()
	}
}
