package inbox

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("idempotency key already used for different content")

type Store struct {
	DB   *sql.DB
	Dir  string
	aead cipher.AEAD
}

func RandomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "encryption.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, e := os.Stat(filepath.Join(dir, "inbox.db")); e == nil {
			return nil, errors.New("encryption.key missing beside existing database; restore it from backup")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, err = f.Write(key)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if syncErr != nil {
			return nil, syncErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil {
		return nil, err
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid encryption.key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "inbox.db")
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, Dir: dir, aead: aead}
	var hasVersion int
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_version'").Scan(&hasVersion); err != nil {
		db.Close()
		return nil, err
	}
	if hasVersion > 0 {
		var v int
		if err = db.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&v); err != nil || v != 1 {
			db.Close()
			return nil, errors.New("unsupported database version; use a compatible release")
		}
	}

	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(dbPath, 0600); err != nil {
		db.Close()
		return nil, err
	}
	var verifier []byte
	err = db.QueryRow("SELECT value FROM settings WHERE name='key_check'").Scan(&verifier)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec("INSERT INTO settings(name,value) VALUES('key_check',?)", s.seal([]byte("wecom-inbox:v1")))
	} else if err == nil {
		var plain []byte
		plain, err = s.unseal(verifier)
		if err == nil && string(plain) != "wecom-inbox:v1" {
			err = errors.New("key mismatch")
		}
	}
	if err != nil {
		db.Close()
		return nil, errors.New("encryption key does not match database")
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) seal(p []byte) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return s.aead.Seal(nonce, nonce, p, nil)
}
func (s *Store) unseal(p []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(p) < n {
		return nil, errors.New("invalid encrypted payload")
	}
	return s.aead.Open(nil, p[:n], p[n:], nil)
}

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS settings(name TEXT PRIMARY KEY,value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);
INSERT OR IGNORE INTO schema_version VALUES(1);
CREATE TABLE IF NOT EXISTS messages (
 id INTEGER PRIMARY KEY AUTOINCREMENT, bot_id TEXT NOT NULL, platform_id TEXT NOT NULL,
 chat_id TEXT NOT NULL,chat_type TEXT NOT NULL,sender_id TEXT NOT NULL,type TEXT NOT NULL,
 content TEXT NOT NULL,quote TEXT NOT NULL DEFAULT '',received_at TEXT NOT NULL,platform_at TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'pending',error TEXT NOT NULL DEFAULT '',payload BLOB,
 UNIQUE(bot_id,platform_id));
CREATE INDEX IF NOT EXISTS messages_bot ON messages(bot_id,id);
CREATE INDEX IF NOT EXISTS messages_pending ON messages(status,id);
CREATE TABLE IF NOT EXISTS attachments (
 id TEXT PRIMARY KEY,message_id INTEGER NOT NULL REFERENCES messages(id),name TEXT NOT NULL,kind TEXT NOT NULL,
 size INTEGER NOT NULL DEFAULT 0,sha256 TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'pending',
 error TEXT NOT NULL DEFAULT '',storage_key TEXT NOT NULL DEFAULT '',ordinal INTEGER NOT NULL,
 UNIQUE(message_id,ordinal));
CREATE TABLE IF NOT EXISTS deliveries (
 id TEXT PRIMARY KEY,message_id INTEGER NOT NULL REFERENCES messages(id),consumer_id TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',
 next_at TEXT NOT NULL,updated_at TEXT NOT NULL,UNIQUE(message_id,consumer_id));
CREATE INDEX IF NOT EXISTS deliveries_pending ON deliveries(status,next_at);
CREATE TABLE IF NOT EXISTS results (
 id INTEGER PRIMARY KEY AUTOINCREMENT,message_id INTEGER NOT NULL REFERENCES messages(id),consumer_id TEXT NOT NULL,
 result_key TEXT NOT NULL,status TEXT NOT NULL,summary TEXT NOT NULL,reply INTEGER NOT NULL,created_at TEXT NOT NULL,
 UNIQUE(message_id,consumer_id,result_key));
CREATE TABLE IF NOT EXISTS replies (
 id INTEGER PRIMARY KEY AUTOINCREMENT,message_id INTEGER NOT NULL REFERENCES messages(id),result_id INTEGER UNIQUE REFERENCES results(id),
 content TEXT NOT NULL,request_id TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'pending',
 error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,kind TEXT NOT NULL DEFAULT 'result',next_at TEXT NOT NULL DEFAULT '');
`

func (s *Store) Put(ctx context.Context, in Incoming) (int64, bool, error) {
	m := in.Message
	if m.BotID == "" || m.PlatformID == "" || len(m.Content) > 512*1024 || len(in.Media) > 20 {
		return 0, false, errors.New("invalid message or input limit exceeded")
	}
	if m.ReceivedAt == "" {
		m.ReceivedAt = now()
	}
	in.Message = m
	p, err := json.Marshal(in)
	if err != nil {
		return 0, false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO messages(bot_id,platform_id,chat_id,chat_type,sender_id,type,content,quote,received_at,platform_at,payload) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, m.BotID, m.PlatformID, m.ChatID, m.ChatType, m.SenderID, m.Type, m.Content, m.Quote, m.ReceivedAt, m.PlatformAt, s.seal(p))
	if err != nil {
		return 0, false, err
	}
	n, _ := r.RowsAffected()
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE bot_id=? AND platform_id=?`, m.BotID, m.PlatformID).Scan(&id); err != nil {
		return 0, false, err
	}
	if n > 0 {
		if in.Source == "legacy" {
			if _, err = tx.ExecContext(ctx, "UPDATE messages SET status='importing' WHERE id=?", id); err != nil {
				return 0, false, err
			}
		}
		for i, a := range in.Media {
			if _, err = tx.ExecContext(ctx, `INSERT INTO attachments(id,message_id,name,kind,ordinal) VALUES(?,?,?,?,?)`, RandomID(), id, a.Name, a.Kind, i); err != nil {
				return 0, false, err
			}
		}
	}
	return id, n > 0, tx.Commit()
}

const messageColumns = `id,bot_id,platform_id,chat_id,chat_type,sender_id,type,content,quote,received_at,platform_at,status,error`

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.BotID, &m.PlatformID, &m.ChatID, &m.ChatType, &m.SenderID, &m.Type, &m.Content, &m.Quote, &m.ReceivedAt, &m.PlatformAt, &m.Status, &m.Error)
	m.Attachments = []Attachment{}
	return m, err
}

func (s *Store) Get(ctx context.Context, id int64) (Message, error) {
	m, err := scanMessage(s.DB.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,message_id,name,kind,size,sha256,status,error,storage_key FROM attachments WHERE message_id=? ORDER BY ordinal`, id)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Attachment
		if err = rows.Scan(&a.ID, &a.MessageID, &a.Name, &a.Kind, &a.Size, &a.SHA256, &a.Status, &a.Error, &a.StorageKey); err != nil {
			return m, err
		}
		m.Attachments = append(m.Attachments, a)
	}
	return m, rows.Err()
}

func (s *Store) List(ctx context.Context, f Filter) (Page, error) {
	page := Page{Messages: []Message{}}
	for _, target := range []*string{&f.From, &f.To} {
		if *target != "" {
			t, e := time.Parse(time.RFC3339Nano, *target)
			if e != nil {
				return page, errors.New("invalid timestamp filter")
			}
			*target = t.UTC().Format(timestampLayout)
		}
	}

	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	var upper, last int64
	if f.Cursor != "" {
		if n, e := fmt.Sscanf(f.Cursor, "%d:%d", &upper, &last); e != nil || n != 2 || upper < 1 || last < 1 {
			return page, errors.New("invalid cursor")
		}
	} else {
		if e := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM messages`).Scan(&upper); e != nil {
			return page, e
		}
		last = upper + 1
	}
	where := []string{"id<=?", "id<?"}
	args := []any{upper, last}
	if f.AllowedBots != nil && !contains(f.AllowedBots, "*") {
		if len(f.AllowedBots) == 0 {
			return page, nil
		}
		marks := []string{}
		for _, b := range f.AllowedBots {
			marks = append(marks, "?")
			args = append(args, b)
		}
		where = append(where, "bot_id IN ("+strings.Join(marks, ",")+")")
	}
	for _, v := range [][2]string{{"bot_id", f.BotID}, {"sender_id", f.SenderID}, {"type", f.Type}, {"status", f.Status}} {
		if v[0] == "status" && v[1] == "attention" {
			where = append(where, "status IN ('failed','partial')")
			continue
		}
		if v[1] != "" {
			where = append(where, v[0]+"=?")
			args = append(args, v[1])
		}
	}
	if f.Query != "" {
		where = append(where, `(instr(lower(content),lower(?))>0 OR instr(lower(quote),lower(?))>0 OR EXISTS(SELECT 1 FROM attachments a WHERE a.message_id=messages.id AND instr(lower(a.name),lower(?))>0))`)
		args = append(args, f.Query, f.Query, f.Query)
	}
	if f.From != "" {
		where = append(where, "received_at>=?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "received_at<=?")
		args = append(args, f.To)
	}
	args = append(args, f.Limit+1)
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM messages WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return page, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return page, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(ids) > f.Limit {
		ids = ids[:f.Limit]
		page.NextCursor = fmt.Sprintf("%d:%d", upper, ids[len(ids)-1])
	}
	for _, id := range ids {
		m, e := s.Get(ctx, id)
		if e != nil {
			return page, e
		}
		page.Messages = append(page.Messages, m)
	}
	return page, nil
}

func (s *Store) Pending(ctx context.Context) (int64, Incoming, error) {
	var id int64
	var encrypted []byte
	err := s.DB.QueryRowContext(ctx, `SELECT id,payload FROM messages WHERE status='pending' ORDER BY id LIMIT 1`).Scan(&id, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, Incoming{}, ErrNotFound
	}
	if err != nil {
		return 0, Incoming{}, err
	}
	p, err := s.unseal(encrypted)
	if err != nil {
		return id, Incoming{}, err
	}
	var in Incoming
	err = json.Unmarshal(p, &in)
	return id, in, err
}

func (s *Store) SaveAttachment(ctx context.Context, a Attachment) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE attachments SET name=?,size=?,sha256=?,status=?,error=?,storage_key=? WHERE id=?`, a.Name, a.Size, a.SHA256, a.Status, a.Error, a.StorageKey, a.ID)
	return err
}

func (s *Store) Finalize(ctx context.Context, id int64, c Config, requestID string) error {
	m, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	status := "ready"
	fail, oversize := 0, 0
	for _, a := range m.Attachments {
		if a.Status != "ready" {
			fail++
			if isSizeLimitError(a.Error) {
				oversize++
			}
		}
	}
	if fail > 0 {
		status = "partial"
		if fail == len(m.Attachments) && m.Content == "" {
			status = "failed"
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE messages SET status=?,payload=NULL WHERE id=?`, status, id); err != nil {
		return err
	}
	for _, v := range c.Consumers {
		if v.URL != "" && contains(v.Bots, m.BotID) {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO deliveries(id,message_id,consumer_id,next_at,updated_at) VALUES(?,?,?,?,?)`, RandomID(), id, v.ID, now(), now()); err != nil {
				return err
			}
		}
	}
	if b, ok := c.Bot(m.BotID); ok && b.Receipt && requestID != "" {
		text := receiptText(fail, oversize, c.MaxFileMB)
		if _, err = tx.ExecContext(ctx, `INSERT INTO replies(message_id,content,request_id,created_at,kind) VALUES(?,?,?,?,'receipt')`, id, text, requestID, now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// 超限附件重发仍会失败，回执须直接说明上限，不能只提示重新发送。
func receiptText(fail, oversize int, maxFileMB int64) string {
	limits := fmt.Sprintf("单个文件 %d MB、单条消息合计 %d MB", maxFileMB, MaxMessageAttachmentBytes>>20)
	limitsEN := fmt.Sprintf("%d MB per file, %d MB per message", maxFileMB, MaxMessageAttachmentBytes>>20)
	switch {
	case fail == 0:
		return "已归档 / Archived"
	case oversize == fail:
		return "消息已保存，附件超过大小上限（" + limits + "）未能保存，重发也无法接收，请压缩或拆分后再发 / " +
			"Message saved; attachment exceeds the size limit (" + limitsEN + ") and was not saved. Resending will not help; compress or split it."
	case oversize > 0:
		return "消息已保存；部分附件超过大小上限（" + limits + "）未能保存，其余失败的附件请重新发送 / " +
			"Message saved; some attachments exceed the size limit (" + limitsEN + "), others failed; please resend those."
	default:
		return "消息已保存，部分附件接收失败，请重新发送 / Attachment failed; please resend"
	}
}

// 备份数据库使用 SQLite 一致性快照；附件与密钥由停机后的 CLI 备份一并复制。
func (s *Store) Snapshot(ctx context.Context, destination string) error {
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("backup destination already exists or cannot be checked")
	}
	_, err := s.DB.ExecContext(ctx, `VACUUM INTO ?`, destination)
	return err
}
