package inbox

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

type callback struct {
	ID         string `json:"msgid"`
	BotID      string `json:"aibotid"`
	ChatID     string `json:"chatid"`
	ChatType   string `json:"chattype"`
	Type       string `json:"msgtype"`
	CreateTime int64  `json:"create_time"`
	From       struct {
		UserID string `json:"userid"`
	} `json:"from"`
}

func ParseIncoming(botID, requestID string, body []byte) (Incoming, error) {
	var in Incoming
	var cb callback
	if len(body) > 1<<20 {
		return in, errors.New("callback exceeds 1 MiB")
	}
	if err := json.Unmarshal(body, &cb); err != nil {
		return in, errors.New("invalid callback JSON")
	}
	if cb.ID == "" || cb.From.UserID == "" || len(cb.ID) > 512 {
		return in, errors.New("message identity missing or invalid")
	}
	if cb.BotID != "" && cb.BotID != botID {
		return in, errors.New("callback bot mismatch")
	}
	if cb.ChatType != "single" && cb.ChatType != "group" {
		return in, errors.New("unsupported chat type")
	}
	if cb.ChatType == "group" && cb.ChatID == "" {
		return in, errors.New("group identity missing")
	}
	in.Message = Message{BotID: botID, PlatformID: cb.ID, ChatID: cb.ChatID, ChatType: cb.ChatType, SenderID: cb.From.UserID, Type: cb.Type, ReceivedAt: now()}
	if cb.ChatType == "single" {
		in.Message.ChatID = cb.From.UserID
	}
	if cb.CreateTime > 0 {
		in.Message.PlatformAt = time.Unix(cb.CreateTime, 0).UTC().Format(time.RFC3339)
	}
	in.RequestID = requestID
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return in, err
	}
	content, media, err := extractParts(obj, 0)
	if err != nil {
		return in, err
	}
	in.Message.Content = content
	in.Media = media
	if q := obj["quote"]; len(q) > 0 {
		var quote map[string]json.RawMessage
		if json.Unmarshal(q, &quote) == nil {
			t, ms, e := extractParts(quote, 1)
			if e != nil {
				return in, e
			}
			in.Message.Quote = t
			for i := range ms {
				ms[i].Kind = "quoted_" + ms[i].Kind
			}
			in.Media = append(in.Media, ms...)
		}
	}
	if len(in.Media) > 20 || len(in.Message.Content)+len(in.Message.Quote) > 512*1024 {
		return in, errors.New("message part limit exceeded")
	}
	return in, nil
}

func extractParts(obj map[string]json.RawMessage, depth int) (string, []MediaSource, error) {
	if depth > 2 {
		return "", nil, errors.New("nested message limit exceeded")
	}
	var kind string
	_ = json.Unmarshal(obj["msgtype"], &kind)
	switch kind {
	case "text", "voice", "file", "image", "video", "mixed":
	default:
		return "", nil, errors.New("unsupported message part type")
	}
	media := []MediaSource{}
	if kind == "mixed" {
		var mixed struct {
			Items []map[string]json.RawMessage `json:"msg_item"`
		}
		if err := json.Unmarshal(obj["mixed"], &mixed); err != nil {
			return "", nil, errors.New("invalid mixed message")
		}
		if len(mixed.Items) > 30 {
			return "", nil, errors.New("too many message parts")
		}
		text := []string{}
		for _, p := range mixed.Items {
			t, m, e := extractParts(p, depth+1)
			if e != nil {
				return "", nil, e
			}
			if t != "" {
				text = append(text, t)
			}
			media = append(media, m...)
		}
		return strings.Join(text, "\n"), media, nil
	}
	var part struct{ Content, URL, AESKey, Name string }
	if p := obj[kind]; len(p) > 0 {
		if err := json.Unmarshal(p, &part); err != nil {
			return "", nil, errors.New("invalid message part")
		}
	}
	if part.URL != "" || kind == "file" || kind == "image" || kind == "video" {
		name := part.Name
		if name == "" {
			name = kind
		}
		media = append(media, MediaSource{URL: part.URL, AESKey: part.AESKey, Name: SafeName(name), Kind: kind})
	}
	return part.Content, media, nil
}

func SafeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	r := []rune(name)
	if len(r) > 180 {
		name = string(r[:180])
	}
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

type Downloader struct {
	Client       *http.Client
	AllowPrivate bool
	MaxBytes     int64
}

// 超限错误的文本会写入 attachments.error，回执据此区分"重发无用"与"可重发"。
var (
	ErrAttachmentTooLarge         = errors.New("attachment exceeds size limit")
	ErrMessageAttachmentsTooLarge = errors.New("message attachments exceed total size limit")
)

func isSizeLimitError(message string) bool {
	return message == ErrAttachmentTooLarge.Error() || message == ErrMessageAttachmentsTooLarge.Error()
}

func (d Downloader) Download(ctx context.Context, source MediaSource) ([]byte, string, error) {
	if err := ValidateURL(source.URL, d.AllowPrivate); err != nil {
		return nil, "", err
	}
	client := d.Client
	if client == nil {
		client = SafeClient(d.AllowPrivate, 90*time.Second)
	}
	max := d.MaxBytes
	if max <= 0 {
		max = MaxFileMBLimit << 20
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
		if err != nil {
			return nil, "", errors.New("invalid media URL")
		}
		resp, err := client.Do(req)
		if err != nil {
			last = errors.New("media connection failed")
		} else {
			data, e := io.ReadAll(io.LimitReader(resp.Body, max+33))
			resp.Body.Close()
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return nil, "", fmt.Errorf("media HTTP %d; resend if the download link expired", resp.StatusCode)
			}
			if resp.StatusCode != 200 {
				last = fmt.Errorf("media HTTP %d", resp.StatusCode)
			} else if e != nil {
				last = errors.New("media body read failed")
			} else {
				if int64(len(data)) > max+32 {
					return nil, "", ErrAttachmentTooLarge
				}
				if source.AESKey != "" {
					data, e = DecryptMedia(data, source.AESKey)
					if e != nil {
						return nil, "", e
					}
				}
				if int64(len(data)) > max {
					return nil, "", ErrAttachmentTooLarge
				}
				name := source.Name
				if _, params, e := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); e == nil && params["filename"] != "" {
					name = params["filename"]
					if decoded, e := url.QueryUnescape(name); e == nil {
						name = decoded
					}
				}
				return data, SafeName(name), nil
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
			}
		}
	}
	return nil, "", last
}

// 企业微信使用 32 字节 PKCS#7 约定，AES 分组仍为 16 字节。
func DecryptMedia(data []byte, encoded string) ([]byte, error) {
	if m := len(encoded) % 4; m != 0 {
		encoded += strings.Repeat("=", 4-m)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid media encryption key")
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("invalid encrypted media length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, key[:16]).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > 32 || pad > len(out) || !bytes.Equal(out[len(out)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("invalid media padding")
	}
	return out[:len(out)-pad], nil
}

func (s *Store) WriteAttachment(ctx context.Context, a Attachment, data []byte, name string) error {
	key := a.ID + ".blob"
	dir := filepath.Join(s.Dir, "files")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, key)
	staging := path + ".pending"
	f, err := os.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	h := sha256.Sum256(data)
	a.Name = SafeName(name)
	a.Size = int64(len(data))
	a.SHA256 = hex.EncodeToString(h[:])
	a.StorageKey = key
	a.Status = "ready"
	a.Error = ""
	// 恢复清单先于最终文件发布，数据库提交失败后仍能核对原件并补登记。
	manifest, err := json.Marshal(a)
	if err != nil {
		return err
	}
	meta, err := os.OpenFile(path+".manifest.json.pending", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = meta.Write(manifest)
	if err == nil {
		err = meta.Sync()
	}
	meta.Close()
	if err != nil {
		return err
	}
	if err = os.Rename(path+".manifest.json.pending", path+".manifest.json"); err != nil {
		return err
	}
	if err = os.Rename(staging, path); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return err
	}
	return s.SaveAttachment(ctx, a)
}

// 返回已校验并回到起点的文件；调用者负责关闭。
func (s *Store) OpenAttachment(a Attachment) (*os.File, error) {
	root, e := os.OpenRoot(filepath.Join(s.Dir, "files"))
	if e != nil {
		return nil, e
	}
	defer root.Close()
	f, e := root.Open(a.StorageKey)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() != a.Size {
		f.Close()
		return nil, errors.New("attachment size mismatch")
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		f.Close()
		return nil, e
	}
	if hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		f.Close()
		return nil, errors.New("attachment checksum mismatch")
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}

func (s *Store) RecoverAttachment(ctx context.Context, a Attachment) (bool, error) {
	key := a.ID + ".blob"
	dir := filepath.Join(s.Dir, "files")
	root, e := os.OpenRoot(dir)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer root.Close()
	f, e := root.Open(key + ".manifest.json")
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var saved Attachment
	e = json.NewDecoder(io.LimitReader(f, 16384)).Decode(&saved)
	f.Close()
	if e != nil || saved.ID != a.ID || saved.MessageID != a.MessageID || saved.Size < 0 || saved.Size > MaxFileMBLimit<<20 {
		return false, errors.New("invalid attachment recovery manifest")
	}
	saved.StorageKey = key
	saved.Status = "ready"
	saved.Error = ""
	check, openErr := s.OpenAttachment(saved)
	if errors.Is(openErr, os.ErrNotExist) {
		staging := saved
		staging.StorageKey = key + ".pending"
		check, openErr = s.OpenAttachment(staging)
		if openErr == nil {
			check.Close()
			check = nil
			if e = os.Rename(filepath.Join(dir, key+".pending"), filepath.Join(dir, key)); e != nil {
				return false, e
			}
			parent, e := os.Open(dir)
			if e != nil {
				return false, e
			}
			e = parent.Sync()
			parent.Close()
			if e != nil {
				return false, e
			}
		}
	}
	if check != nil {
		check.Close()
	}
	if openErr != nil {
		return false, openErr
	}
	return true, s.SaveAttachment(ctx, saved)
}

type Ingestor struct {
	Store      *Store
	Config     func() Config
	Downloader Downloader
}

func (i *Ingestor) Accept(ctx context.Context, botID, requestID string, body []byte) (int64, bool, error) {
	in, err := ParseIncoming(botID, requestID, body)
	if err != nil {
		return 0, false, err
	}
	b, ok := i.Config().Bot(botID)
	if !ok || !b.Enabled {
		return 0, false, errors.New("bot is not enabled")
	}
	if !contains(b.AllowedUsers, in.Message.SenderID) && !contains(b.AllowedChats, in.Message.ChatID) {
		return 0, false, errors.New("sender or conversation is not allowed")
	}
	return i.Store.Put(ctx, in)
}

func (i *Ingestor) ProcessOne(ctx context.Context) (bool, error) {
	id, in, err := i.Store.Pending(ctx)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		if id > 0 && ctx.Err() == nil {
			_, saveErr := i.Store.DB.ExecContext(ctx, "UPDATE messages SET status='failed',error='pending event cannot be decoded; retained for operator recovery' WHERE id=?", id)
			return true, saveErr
		}
		return false, err
	}
	m, err := i.Store.Get(ctx, id)
	if err != nil {
		return true, err
	}
	var total int64
	for n, a := range m.Attachments {
		if a.Status == "ready" {
			f, e := i.Store.OpenAttachment(a)
			if e != nil {
				a.Status = "failed"
				a.Error = "archived file missing or checksum mismatch"
				if e = i.Store.SaveAttachment(ctx, a); e != nil {
					return true, e
				}
				continue
			}
			f.Close()
			total += a.Size
			continue
		}
		recovered, e := i.Store.RecoverAttachment(ctx, a)
		if e != nil {
			a.Status = "failed"
			a.Error = "local recovery evidence invalid; files retained for operator recovery"
			if e = i.Store.SaveAttachment(ctx, a); e != nil {
				return true, e
			}
			continue
		}
		if recovered {
			updated, e := i.Store.Get(ctx, id)
			if e != nil {
				return true, e
			}
			total += updated.Attachments[n].Size
			continue
		}
		if n >= len(in.Media) {
			return true, errors.New("attachment manifest mismatch")
		}
		d := i.Downloader
		d.MaxBytes = i.Config().MaxFileMB << 20
		if remaining := MaxMessageAttachmentBytes - total; remaining < d.MaxBytes {
			d.MaxBytes = remaining
		}
		var data []byte
		var name string
		if d.MaxBytes <= 0 {
			err = ErrMessageAttachmentsTooLarge
		} else {
			data, name, err = d.Download(ctx, in.Media[n])
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if err != nil {
			a.Status = "failed"
			a.Error = err.Error()
			if e := i.Store.SaveAttachment(ctx, a); e != nil {
				return true, e
			}
			continue
		}
		if err = i.Store.WriteAttachment(ctx, a, data, name); err != nil {
			return true, err
		}
		total += int64(len(data))
	}
	return true, i.Store.Finalize(ctx, id, i.Config(), in.RequestID)
}
