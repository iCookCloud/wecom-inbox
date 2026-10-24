package wechatkf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/iCookCloud/wecom-inbox/internal/inbox"
)

type Service struct {
	Store      *inbox.Store
	Config     func() inbox.Config
	BotSender  inbox.Sender
	NewClient  func(inbox.WeChatKF) *Client
	mu         sync.Mutex
	clients    map[string]*Client
	syncMu     sync.Mutex
	nextSource int
}

func (s *Service) client(c inbox.WeChatKF) *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients == nil {
		s.clients = map[string]*Client{}
	}
	client := s.clients[c.ID]
	if client == nil || client.Config.CorpID != c.CorpID || client.Config.Secret != c.Secret {
		if client != nil {
			client.HTTP.CloseIdleConnections()
		}
		factory := s.NewClient
		if factory == nil {
			factory = NewClient
		}
		client = factory(c)
		s.clients[c.ID] = client
	}
	return client
}

func (s *Service) source(id string) (inbox.WeChatKF, inbox.KFAccount, bool) {
	for _, c := range s.Config().WeChatKF {
		for _, a := range c.Accounts {
			if c.Source(a).ID == id {
				return c, a, true
			}
		}
	}
	return inbox.WeChatKF{}, inbox.KFAccount{}, false
}

func allowed(account inbox.KFAccount, customer string) bool {
	for _, id := range account.AllowedCustomers {
		if id == "*" || id == customer {
			return true
		}
	}
	return false
}

func (s *Service) States(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, c := range s.Config().WeChatKF {
		for _, a := range c.Accounts {
			id := c.Source(a).ID
			state, err := s.Store.KFState(ctx, id)
			switch {
			case !c.Enabled:
				out[id] = "disabled"
			case err != nil:
				out[id] = "connecting"
			case state.Error != "":
				out[id] = "sync_failed"
			case state.LastSuccess != "":
				out[id] = "listening"
			default:
				out[id] = "connecting"
			}
		}
	}
	return out
}

func (s *Service) SyncOne(ctx context.Context) (bool, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	type accountSource struct {
		connection inbox.WeChatKF
		account    inbox.KFAccount
	}
	var sources []accountSource
	for _, c := range s.Config().WeChatKF {
		if !c.Enabled {
			continue
		}
		for _, a := range c.Accounts {
			sources = append(sources, accountSource{c, a})
		}
	}
	// 持续有新通知的账号不能抢占其他客服账号的同步机会。
	for offset := 0; offset < len(sources); offset++ {
		index := (s.nextSource + offset) % len(sources)
		c, a := sources[index].connection, sources[index].account
		source := c.Source(a).ID
		if err := s.Store.EnsureKF(ctx, source); err != nil {
			return false, err
		}
		state, err := s.Store.KFState(ctx, source)
		if err != nil {
			return false, err
		}
		next, _ := time.Parse(time.RFC3339Nano, state.NextAt)
		if next.After(time.Now()) {
			continue
		}
		s.nextSource = (index + 1) % len(sources)
		err = s.syncPage(ctx, c, a, state)
		if err != nil && ctx.Err() == nil {
			if saveErr := s.Store.KFSyncFailure(ctx, source, state, err.Error()); saveErr != nil {
				return true, saveErr
			}
		}
		return true, err
	}
	return false, nil
}

func (s *Service) syncPage(ctx context.Context, c inbox.WeChatKF, a inbox.KFAccount, state inbox.KFSyncState) error {
	body := map[string]any{"cursor": state.Cursor, "open_kfid": a.OpenKfID, "limit": 100, "voice_format": 0}
	if state.Token != "" && time.Now().Unix()-state.TokenAt < 9*60 {
		body["token"] = state.Token
	}
	var page struct {
		Cursor   string             `json:"next_cursor"`
		More     *int               `json:"has_more"`
		Messages *[]json.RawMessage `json:"msg_list"`
	}
	if err := s.client(c).Call(ctx, "/cgi-bin/kf/sync_msg", body, &page); err != nil {
		return err
	}
	if page.More == nil || page.Messages == nil || len(page.Cursor) > 64 || len(*page.Messages) > 1000 || (*page.More != 0 && *page.More != 1) || (*page.More == 1 && (page.Cursor == "" || page.Cursor == state.Cursor)) || (len(*page.Messages) > 0 && page.Cursor == "") {
		return errors.New("invalid customer-service pagination response")
	}
	if page.Cursor == "" {
		page.Cursor = state.Cursor
	}
	source := c.Source(a).ID
	for _, raw := range *page.Messages {
		// 热更新撤销收件权限后，不再使用请求发出时的授权快照。
		current, account, ok := s.source(source)
		if !ok || !current.Enabled {
			return errors.New("customer-service source disabled during sync")
		}
		if err := s.saveMessage(ctx, current, account, raw); err != nil {
			return err
		}
	}
	// 每条消息已持久化之后才推进游标；中断后重复拉取由消息唯一键去重。
	return s.Store.KFAdvance(ctx, source, state, page.Cursor, *page.More == 1)
}

func (s *Service) saveMessage(ctx context.Context, c inbox.WeChatKF, a inbox.KFAccount, raw json.RawMessage) error {
	var p platformMessage
	if err := json.Unmarshal(raw, &p); err != nil {
		return errors.New("invalid customer-service message envelope")
	}
	source := c.Source(a).ID
	if p.Type == "event" {
		if p.Event.Type == "msg_send_fail" && p.Event.Account == a.OpenKfID && p.Event.FailedID != "" {
			return s.Store.KFSendFailure(ctx, source, p.Event.FailedID, fmt.Sprintf("WeChat delivery failed (type %d)", p.Event.Failure))
		}
		return nil
	}
	if p.Account != a.OpenKfID {
		return errors.New("customer-service account mismatch in sync response")
	}
	if p.Origin != 3 && p.Origin != 5 {
		return nil
	}
	if !allowed(a, p.Customer) {
		return nil
	}
	in := inbox.Incoming{Source: "wechat_kf", Raw: raw, Message: inbox.Message{BotID: source, PlatformID: p.ID, ChatID: p.Customer, ChatType: "wechat_kf", SenderID: p.Customer, Type: p.Type, PlatformAt: time.Unix(p.Sent, 0).UTC().Format(time.RFC3339)}}
	if p.Origin == 5 {
		in.Message.ChatType = "wechat_kf_event"
		in.Message.SenderID = p.Servicer
	}
	if p.Origin == 3 && a.Receipt {
		in.RequestID = p.ID
	}
	text, media, err := extractMessage(raw, 0)
	if p.ID == "" || len(p.ID) > 512 || p.Customer == "" || p.Sent <= 0 || p.Sent > time.Now().Add(5*time.Minute).Unix() {
		err = errors.New("customer-service message identity or time invalid")
	}
	if len(text) > 512<<10 {
		err = errors.New("customer-service message text exceeds limit")
	}
	if err != nil {
		in.Problem = err.Error()
		in.Message.Content = "无法完整归档此消息 / Message could not be fully archived"
		if p.ID == "" || len(p.ID) > 512 {
			h := sha256.Sum256(raw)
			in.Message.PlatformID = "invalid:" + hex.EncodeToString(h[:])
		}
		in.RequestID = ""
	} else {
		in.Message.Content = text
		in.Media = media
	}
	_, _, err = s.Store.Put(ctx, in)
	return err
}

func (s *Service) Download(ctx context.Context, m inbox.Message, media inbox.MediaSource, maxBytes int64) ([]byte, string, error) {
	c, account, ok := s.source(m.BotID)
	if !ok || !c.Enabled || !allowed(account, m.ChatID) {
		return nil, "", errors.New("customer-service source or customer authorization revoked before download")
	}
	return s.client(c).Download(ctx, media, maxBytes)
}

func (s *Service) Send(ctx context.Context, bot, target, kind, text, request string) error {
	if strings.HasPrefix(bot, "kf:") {
		return errors.New("customer-service replies require a durable reply record")
	}
	if s.BotSender == nil {
		return inbox.ErrOffline
	}
	return s.BotSender.Send(ctx, bot, target, kind, text, request)
}

func (s *Service) SendReply(ctx context.Context, r inbox.Reply) error {
	if r.Message.ChatType != "wechat_kf" {
		return s.Send(ctx, r.Message.BotID, r.Message.ChatID, r.Message.ChatType, r.Content, r.RequestID)
	}
	c, a, ok := s.source(r.Message.BotID)
	if !ok || !c.Enabled || !allowed(a, r.Message.ChatID) {
		return errors.New("customer-service reply authorization revoked")
	}
	if len(r.Content) > 2048 {
		return errors.New("customer-service reply exceeds 2048 bytes; shorten the result")
	}
	var latest string
	if err := s.Store.DB.QueryRowContext(ctx, `SELECT platform_at FROM messages WHERE bot_id=? AND chat_id=? AND chat_type='wechat_kf' ORDER BY platform_at DESC,id DESC LIMIT 1`, r.Message.BotID, r.Message.ChatID).Scan(&latest); err != nil {
		return err
	}
	at, err := time.Parse(time.RFC3339Nano, latest)
	if err != nil || time.Since(at) > 48*time.Hour {
		return errors.New("customer-service 48-hour reply window elapsed; customer must send a new message")
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s", r.Message.BotID, r.Message.PlatformID, r.ID, r.Content)))
	msgid := hex.EncodeToString(h[:16])
	if _, err = s.Store.DB.ExecContext(ctx, `INSERT OR IGNORE INTO kf_outbound(source_id,msgid,reply_id) VALUES(?,?,?)`, r.Message.BotID, msgid, r.ID); err != nil {
		return err
	}
	var result struct {
		ID string `json:"msgid"`
	}
	err = s.client(c).Call(ctx, "/cgi-bin/kf/send_msg", map[string]any{"open_kfid": a.OpenKfID, "touser": r.Message.ChatID, "msgid": msgid, "msgtype": "text", "text": map[string]string{"content": r.Content}}, &result)
	if err != nil {
		return err
	}
	if result.ID != msgid {
		return inbox.ErrUncertain
	}
	return inbox.ErrAccepted
}

func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.clients {
		c.HTTP.CloseIdleConnections()
	}
}
