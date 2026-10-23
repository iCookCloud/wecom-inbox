package inbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

func (s *Store) Deliveries(ctx context.Context, messageID int64) ([]Delivery, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,message_id,consumer_id,status,attempts,error,next_at,updated_at FROM deliveries WHERE message_id=? ORDER BY consumer_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err = rows.Scan(&d.ID, &d.MessageID, &d.ConsumerID, &d.Status, &d.Attempts, &d.Error, &d.NextAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RetryDelivery(ctx context.Context, id string) error {
	r, e := s.DB.ExecContext(ctx, `UPDATE deliveries SET status='pending',attempts=0,error='',next_at=?,updated_at=? WHERE id=? AND status IN ('failed','delivered')`, now(), now(), id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) Result(ctx context.Context, id int64) (Result, error) {
	var r Result
	e := s.DB.QueryRowContext(ctx, `SELECT r.id,r.message_id,r.consumer_id,r.result_key,r.status,r.summary,r.reply,r.created_at,COALESCE(p.status,''),COALESCE(p.error,'') FROM results r LEFT JOIN replies p ON p.result_id=r.id WHERE r.id=?`, id).Scan(&r.ID, &r.MessageID, &r.ConsumerID, &r.Key, &r.Status, &r.Summary, &r.Reply, &r.CreatedAt, &r.ReplyStatus, &r.ReplyError)
	if errors.Is(e, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, e
}

func (s *Store) Results(ctx context.Context, messageID int64) ([]Result, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id FROM results WHERE message_id=? ORDER BY id DESC`, messageID)
	if e != nil {
		return nil, e
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []Result{}
	for _, id := range ids {
		r, e := s.Result(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) AddResult(ctx context.Context, messageID int64, consumerID string, in ResultInput) (Result, bool, error) {
	if in.Key == "" || len(in.Key) > 128 || len(in.Summary) > 4000 || (in.Status != "completed" && in.Status != "failed") {
		return Result{}, false, errors.New("invalid result: key, completed/failed status and summary <=4000 bytes required")
	}
	if in.Reply && in.Summary == "" {
		return Result{}, false, errors.New("reply summary is empty")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, false, err
	}
	defer tx.Rollback()
	var id int64
	var status, summary string
	var reply bool
	err = tx.QueryRowContext(ctx, `SELECT id,status,summary,reply FROM results WHERE message_id=? AND consumer_id=? AND result_key=?`, messageID, consumerID, in.Key).Scan(&id, &status, &summary, &reply)
	if err == nil {
		if status != in.Status || summary != in.Summary || reply != in.Reply {
			return Result{}, false, ErrConflict
		}
		tx.Rollback()
		r, e := s.Result(ctx, id)
		return r, false, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, false, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO results(message_id,consumer_id,result_key,status,summary,reply,created_at) VALUES(?,?,?,?,?,?,?)`, messageID, consumerID, in.Key, in.Status, in.Summary, in.Reply, now())
	if err != nil {
		return Result{}, false, err
	}
	id, err = res.LastInsertId()
	if err != nil {
		return Result{}, false, err
	}
	if in.Reply {
		if _, err = tx.ExecContext(ctx, `INSERT INTO replies(message_id,result_id,content,created_at) VALUES(?,?,?,?)`, messageID, id, in.Summary, now()); err != nil {
			return Result{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Result{}, false, err
	}
	r, e := s.Result(ctx, id)
	return r, true, e
}

type Dispatcher struct {
	Store  *Store
	Config func() Config
}

func WebhookSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// 一次投递只确认接收端的 HTTP 响应，业务结果另由结果接口登记。
func (d *Dispatcher) DeliverOne(ctx context.Context) (bool, error) {
	var v Delivery
	err := d.Store.DB.QueryRowContext(ctx, `SELECT id,message_id,consumer_id,status,attempts,error,next_at,updated_at FROM deliveries WHERE status='pending' AND next_at<=? ORDER BY next_at,id LIMIT 1`, now()).Scan(&v.ID, &v.MessageID, &v.ConsumerID, &v.Status, &v.Attempts, &v.Error, &v.NextAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	c, ok := d.Config().Consumer(v.ConsumerID)
	m, e := d.Store.Get(ctx, v.MessageID)
	if e != nil {
		return true, e
	}
	if !ok || c.URL == "" || !contains(c.Bots, m.BotID) {
		_, e = d.Store.DB.ExecContext(ctx, `UPDATE deliveries SET status='failed',error='consumer missing, disabled or no longer authorized',updated_at=? WHERE id=?`, now(), v.ID)
		return true, e
	}
	body, e := json.Marshal(map[string]any{"schema_version": 1, "event_id": v.ID, "type": "message.archived", "message": m})
	if e != nil {
		return true, e
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if e != nil {
		return true, e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WeCom-Inbox-Event", v.ID)
	req.Header.Set("X-WeCom-Inbox-Timestamp", stamp)
	req.Header.Set("X-WeCom-Inbox-Signature", WebhookSignature(c.Secret, stamp, body))
	status := "pending"
	failure := ""
	attempts := v.Attempts + 1
	// 尝试次数先落盘，进程在请求后中断也不会无限重放。
	if _, e = d.Store.DB.ExecContext(ctx, `UPDATE deliveries SET attempts=?,next_at=?,updated_at=? WHERE id=?`, attempts, time.Now().Add(15*time.Second).UTC().Format(timestampLayout), now(), v.ID); e != nil {
		return true, e
	}
	if attempts > 5 {
		status = "failed"
		failure = "delivery attempt limit exceeded"
	} else {
		client := SafeClient(c.AllowPrivateNetwork, 10*time.Second)
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			failure = "webhook connection or timeout failure"
		} else {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				status = "delivered"
			} else {
				failure = fmt.Sprintf("webhook HTTP %d", resp.StatusCode)
			}
		}
		client.CloseIdleConnections()
		if status != "delivered" && attempts >= 5 {
			status = "failed"
		}
	}
	next := time.Now().Add(time.Duration(1<<min(attempts, 8)) * time.Second).UTC().Format(timestampLayout)
	_, e = d.Store.DB.ExecContext(ctx, `UPDATE deliveries SET status=?,error=?,next_at=?,updated_at=? WHERE id=?`, status, failure, next, now(), v.ID)
	return true, e
}

var ErrOffline = errors.New("bot is disconnected")
var ErrUncertain = errors.New("platform acknowledgement not received; delivery is uncertain")

type Sender interface {
	Send(context.Context, string, string, string, string, string) error
}

func (d *Dispatcher) ReplyOne(ctx context.Context, sender Sender) (bool, error) {
	var id, messageID int64
	var content, reqID, kind, created string
	err := d.Store.DB.QueryRowContext(ctx, `SELECT id,message_id,content,request_id,kind,created_at FROM replies WHERE status='pending' AND next_at<=? ORDER BY id LIMIT 1`, now()).Scan(&id, &messageID, &content, &reqID, &kind, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	m, err := d.Store.Get(ctx, messageID)
	if err != nil {
		return true, err
	}
	status, failure := "delivered", ""
	c := d.Config()
	bot, enabled := c.Bot(m.BotID)
	authorized := enabled && bot.Enabled && (contains(bot.AllowedUsers, m.SenderID) || contains(bot.AllowedChats, m.ChatID))
	if kind == "receipt" {
		authorized = authorized && bot.Receipt
	} else {
		var owner string
		if err = d.Store.DB.QueryRowContext(ctx, "SELECT consumer_id FROM results WHERE id=(SELECT result_id FROM replies WHERE id=?)", id).Scan(&owner); err != nil {
			return true, err
		}
		if owner != "admin" {
			consumer, exists := c.Consumer(owner)
			authorized = authorized && exists && consumer.AllowReply && contains(consumer.Bots, m.BotID)
		}
	}
	if !authorized {
		_, err = d.Store.DB.ExecContext(ctx, "UPDATE replies SET status='cancelled',error='authorization revoked before delivery' WHERE id=?", id)
		return true, err
	}
	at, _ := time.Parse(time.RFC3339Nano, created)
	if kind == "receipt" && time.Since(at) > 5*time.Minute {
		status = "expired"
		failure = "receipt response window elapsed"
	} else {
		// 请求前先记不确定；写出后崩溃时不自动再发，避免重复业务回复。
		if _, err = d.Store.DB.ExecContext(ctx, `UPDATE replies SET status='uncertain',error='send interrupted before acknowledgement' WHERE id=?`, id); err != nil {
			return true, err
		}
		err = sender.Send(ctx, m.BotID, m.ChatID, m.ChatType, content, reqID)
		if errors.Is(err, ErrOffline) {
			status = "pending"
			failure = "bot disconnected"
		} else if errors.Is(err, ErrUncertain) {
			status = "uncertain"
			failure = err.Error()
		} else if err != nil {
			status = "failed"
			failure = err.Error()
		}
	}
	_, err = d.Store.DB.ExecContext(ctx, `UPDATE replies SET status=?,error=?,next_at=? WHERE id=?`, status, failure, time.Now().Add(5*time.Second).UTC().Format(timestampLayout), id)
	return true, err
}
