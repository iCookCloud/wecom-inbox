package wechatkf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/iCookCloud/wecom-inbox/internal/inbox"
)

const apiOrigin = "https://qyapi.weixin.qq.com"

type APIError struct{ Code int }

func (e *APIError) Error() string {
	return fmt.Sprintf("WeChat customer-service API error %d; check account permissions, session state and platform limits", e.Code)
}
func expiredToken(err error) bool {
	var e *APIError
	return errors.As(err, &e) && (e.Code == 40014 || e.Code == 42001)
}

// API 地址固定；测试通过 HTTP transport 拦截官方地址，不把可配置 URL 当作凭据出口。
type Client struct {
	Config  inbox.WeChatKF
	HTTP    *http.Client
	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewClient(c inbox.WeChatKF) *Client {
	return &Client{Config: c, HTTP: inbox.SafeClient(false, 90*time.Second)}
}

func decodeAPI(data []byte, out any) error {
	var envelope struct {
		Errcode *int `json:"errcode"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Errcode == nil {
		return errors.New("invalid customer-service API response")
	}
	if *envelope.Errcode != 0 {
		return &APIError{Code: *envelope.Errcode}
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return errors.New("invalid customer-service API response")
	}
	return nil
}

func (c *Client) request(ctx context.Context, path string, query url.Values, body any) ([]byte, http.Header, error) {
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reader = bytes.NewReader(b)
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, apiOrigin+path+"?"+query.Encode(), reader)
	if err != nil {
		return nil, nil, errors.New("cannot construct customer-service request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	// net/http 错误可能包含 access_token 或 secret，不向日志及网页转发原错误。
	if err != nil {
		return nil, nil, errors.New("customer-service API connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("customer-service API HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil || len(data) > 8<<20 {
		return nil, nil, errors.New("customer-service API response exceeds limit or is incomplete")
	}
	return data, resp.Header, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	data, _, err := c.request(ctx, "/cgi-bin/gettoken", url.Values{"corpid": {c.Config.CorpID}, "corpsecret": {c.Config.Secret}}, nil)
	if err != nil {
		return "", err
	}
	var result struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if err = decodeAPI(data, &result); err != nil {
		return "", err
	}
	if result.Token == "" || result.Expires <= 0 {
		return "", errors.New("customer-service access token missing")
	}
	c.token = result.Token
	c.expires = time.Now().Add(time.Duration(max(1, result.Expires-60)) * time.Second)
	return c.token, nil
}

func (c *Client) invalidate(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == token {
		c.token = ""
	}
}

func (c *Client) Call(ctx context.Context, path string, body, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		data, _, err := c.request(ctx, path, url.Values{"access_token": {token}}, body)
		if err != nil {
			if path == "/cgi-bin/kf/send_msg" {
				return inbox.ErrUncertain
			}
			return err
		}
		err = decodeAPI(data, out)
		if expiredToken(err) && attempt == 0 {
			c.invalidate(token)
			continue
		}
		if err != nil && path == "/cgi-bin/kf/send_msg" {
			var api *APIError
			if !errors.As(err, &api) {
				return inbox.ErrUncertain
			}
		}
		return err
	}
	return errors.New("customer-service token refresh failed")
}

// 媒体 ID 留在加密待处理记录中；每次下载时才取当前 access_token。
func (c *Client) Download(ctx context.Context, source inbox.MediaSource, maxBytes int64) ([]byte, string, error) {
	if source.MediaID == "" {
		return nil, "", errors.New("customer-service media_id missing")
	}
	for attempt := 0; attempt < 3; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return nil, "", err
		}
		u := apiOrigin + "/cgi-bin/media/get?" + url.Values{"access_token": {token}, "media_id": {source.MediaID}}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, "", errors.New("cannot construct media request")
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if attempt < 2 && ctx.Err() == nil {
				continue
			}
			return nil, "", errors.New("customer-service media connection failed")
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
		resp.Body.Close()
		if int64(len(data)) > maxBytes {
			return nil, "", inbox.ErrAttachmentTooLarge
		}
		if readErr != nil || resp.StatusCode >= 500 {
			if attempt < 2 && ctx.Err() == nil {
				continue
			}
			return nil, "", errors.New("customer-service media download incomplete")
		}
		if resp.StatusCode != 200 {
			return nil, "", fmt.Errorf("customer-service media HTTP %d", resp.StatusCode)
		}
		_, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
		// 错误响应未保证 Content-Type；有文件名的 JSON 原件则照常保留。
		if params["filename"] == "" {
			var envelope struct {
				Code *int `json:"errcode"`
			}
			if json.Unmarshal(data, &envelope) == nil && envelope.Code != nil {
				err = &APIError{Code: *envelope.Code}
				if expiredToken(err) && attempt < 2 {
					c.invalidate(token)
					continue
				}
				return nil, "", err
			}
		}
		name := source.Name
		if params["filename"] != "" {
			name = params["filename"]
			if decoded, e := url.QueryUnescape(name); e == nil {
				name = decoded
			}
		}
		return data, inbox.SafeName(name), nil
	}
	return nil, "", errors.New("customer-service media download failed")
}

type AccountInfo struct {
	ID   string `json:"open_kfid"`
	Name string `json:"name"`
}

func (c *Client) Accounts(ctx context.Context) ([]AccountInfo, error) {
	accounts := []AccountInfo{}
	for offset := 0; offset < 10000; offset += 100 {
		var result struct {
			Accounts []AccountInfo `json:"account_list"`
		}
		if err := c.Call(ctx, "/cgi-bin/kf/account/list", map[string]any{"offset": offset, "limit": 100}, &result); err != nil {
			return nil, err
		}
		accounts = append(accounts, result.Accounts...)
		if len(result.Accounts) < 100 {
			return accounts, nil
		}
	}
	return nil, errors.New("customer-service account listing exceeds 10000 accounts")
}

func (c *Client) ContactLink(ctx context.Context, account string) (string, error) {
	var result struct {
		URL string `json:"url"`
	}
	if err := c.Call(ctx, "/cgi-bin/kf/add_contact_way", map[string]string{"open_kfid": account, "scene": "wecom_inbox"}, &result); err != nil {
		return "", err
	}
	u, err := url.Parse(result.URL)
	if err != nil || u.Scheme != "https" || u.Host != "work.weixin.qq.com" || u.User != nil {
		return "", errors.New("invalid customer-service contact link")
	}
	return result.URL, nil
}
