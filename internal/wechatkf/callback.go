package wechatkf

import (
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iCookCloud/wecom-inbox/internal/inbox"
)

func decryptCallback(c inbox.WeChatKF, signature, timestamp, nonce, encrypted string) ([]byte, error) {
	stamp, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || stamp < time.Now().Add(-10*time.Minute).Unix() || stamp > time.Now().Add(5*time.Minute).Unix() || nonce == "" || len(nonce) > 256 {
		return nil, errors.New("invalid callback timestamp or nonce")
	}
	parts := []string{c.Token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	h := sha1.Sum([]byte(strings.Join(parts, "")))
	expected := hex.EncodeToString(h[:])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return nil, errors.New("invalid callback signature")
	}
	// 使用与媒体相同的 AES-CBC/PKCS#7 约定，随后校验长度和接收企业。
	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, err
	}
	plain, err := inbox.DecryptMedia(ciphertext, c.EncodingAESKey)
	if err != nil || len(plain) < 20 {
		return nil, errors.New("invalid callback ciphertext")
	}
	n := uint64(binary.BigEndian.Uint32(plain[16:20]))
	if n > uint64(len(plain)-20) {
		return nil, errors.New("invalid callback payload length")
	}
	end := 20 + int(n)
	if subtle.ConstantTimeCompare(plain[end:], []byte(c.CorpID)) != 1 {
		return nil, errors.New("callback receiver mismatch")
	}
	return plain[20:end], nil
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	id := strings.TrimPrefix(r.URL.Path, "/callbacks/wechat-kf/")
	var connection inbox.WeChatKF
	found := false
	for _, c := range s.Config().WeChatKF {
		if c.ID == id && c.Enabled {
			connection = c
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	q := r.URL.Query()
	for _, key := range []string{"msg_signature", "timestamp", "nonce"} {
		if len(q[key]) != 1 {
			http.Error(w, "invalid callback", 400)
			return
		}
	}
	encrypted := q.Get("echostr")
	if r.Method == http.MethodPost {
		var envelope struct {
			XMLName xml.Name `xml:"xml"`
			Encrypt string   `xml:"Encrypt"`
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil || xml.Unmarshal(body, &envelope) != nil || envelope.Encrypt == "" {
			http.Error(w, "invalid callback body", 400)
			return
		}
		encrypted = envelope.Encrypt
	}
	if len(encrypted) > 1<<20 {
		http.Error(w, "callback too large", 413)
		return
	}
	plain, err := decryptCallback(connection, q.Get("msg_signature"), q.Get("timestamp"), q.Get("nonce"), encrypted)
	if err != nil {
		http.Error(w, "invalid callback authentication", 403)
		return
	}
	if r.Method == http.MethodGet {
		w.Write(plain)
		return
	}
	var event struct {
		XMLName  xml.Name `xml:"xml"`
		Receiver string   `xml:"ToUserName"`
		Type     string   `xml:"MsgType"`
		Event    string   `xml:"Event"`
		Token    string   `xml:"Token"`
		OpenKfID string   `xml:"OpenKfId"`
		Created  int64    `xml:"CreateTime"`
	}
	if xml.Unmarshal(plain, &event) != nil || event.Receiver != connection.CorpID {
		http.Error(w, "invalid callback event", 400)
		return
	}
	if event.Type != "event" || event.Event != "kf_msg_or_event" {
		io.WriteString(w, "success")
		return
	}
	if event.Token == "" || len(event.Token) > 128 || event.Created <= 0 || event.Created > time.Now().Add(5*time.Minute).Unix() {
		http.Error(w, "invalid sync notification", 400)
		return
	}
	for _, account := range connection.Accounts {
		if account.OpenKfID != event.OpenKfID {
			continue
		}
		// 必须先持久化再确认回调，API 拉取由后台任务执行。
		if err = s.Store.KFNotify(r.Context(), connection.Source(account).ID, event.Token, event.Created); err != nil {
			http.Error(w, "notification storage unavailable", 503)
			return
		}
		break
	}
	io.WriteString(w, "success")
}
