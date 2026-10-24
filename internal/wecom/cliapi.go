package wecom

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/iCookCloud/wecom-inbox/internal/inbox"
)

// 企业微信 CLI 网关：与长连接共用 Bot ID/Secret，但能力受机器人"可使用权限"授权约束（授权约 7 天到期）。
const (
	CLIAuthURL = "https://qyapi.weixin.qq.com/cgi-bin/aibot/cli/get_cli_config"
	CLIBaseURL = "https://qyapi.weixin.qq.com/cli"
)

type CLIAPI struct {
	HTTP    *http.Client
	AuthURL string
	BaseURL string
}

func NewCLIAPI() CLIAPI {
	return CLIAPI{HTTP: &http.Client{Timeout: 15 * time.Second}, AuthURL: CLIAuthURL, BaseURL: CLIBaseURL}
}

// Sessions 返回机器人最近有消息往来、可主动发送的会话（平台最多返回 20 个）。
// 仅供管理员低频查询，每次调用重新换取令牌，不缓存凭据。
func (a CLIAPI) Sessions(ctx context.Context, bot inbox.Bot) ([]inbox.Session, error) {
	token, err := a.token(ctx, bot)
	if err != nil {
		return nil, err
	}
	var out struct {
		Sessions []inbox.Session `json:"sessions"`
	}
	if err = a.call(ctx, token, "/message/aibot/sessions/list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	if out.Sessions == nil {
		out.Sessions = []inbox.Session{}
	}
	return out.Sessions, nil
}

func (a CLIAPI) token(ctx context.Context, bot inbox.Bot) (string, error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	now := time.Now().Unix()
	n := "inbox_" + hex.EncodeToString(nonce)
	sum := sha256.Sum256([]byte(bot.Secret + bot.ID + strconv.FormatInt(now, 10) + n))
	var out struct {
		gatewayStatus
		Token string `json:"token"`
	}
	body := map[string]any{"bot_id": bot.ID, "time": now, "nonce": n, "signature": hex.EncodeToString(sum[:]), "bind_source": 1}
	if err := a.post(ctx, a.AuthURL, "", body, &out); err != nil {
		return "", err
	}
	if err := out.err("CLI authorization"); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("CLI authorization returned no token")
	}
	return out.Token, nil
}

type gatewayStatus struct {
	Code    int    `json:"errcode"`
	Message string `json:"errmsg"`
}

func (g gatewayStatus) err(op string) error {
	if g.Code != 0 {
		return fmt.Errorf("%s failed: errcode %d: %s", op, g.Code, g.Message)
	}
	return nil
}

// 网关信封：请求 {"payload": "<json>"}；响应 results_json 内嵌 {"result": "<json>"} 两层字符串。
func (a CLIAPI) call(ctx context.Context, token, path string, payload, target any) error {
	p, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var out struct {
		gatewayStatus
		Results string `json:"results_json"`
	}
	if err = a.post(ctx, a.BaseURL+path, token, map[string]string{"payload": string(p)}, &out); err != nil {
		return err
	}
	if err = out.err(path); err != nil {
		return err
	}
	var wrapped struct {
		Result string `json:"result"`
	}
	if err = json.Unmarshal([]byte(out.Results), &wrapped); err != nil {
		return fmt.Errorf("%s: invalid results_json", path)
	}
	if err = json.Unmarshal([]byte(wrapped.Result), target); err != nil {
		return fmt.Errorf("%s: invalid result", path)
	}
	return nil
}

func (a CLIAPI) post(ctx context.Context, url, token string, body, target any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return errors.New("CLI gateway connection failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errors.New("CLI gateway read failed")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CLI gateway HTTP %d", resp.StatusCode)
	}
	if err = json.Unmarshal(data, target); err != nil {
		return errors.New("CLI gateway returned invalid JSON")
	}
	return nil
}
