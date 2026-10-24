package wechatkf

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iCookCloud/wecom-inbox/internal/inbox"
)

type platformMessage struct {
	ID       string `json:"msgid"`
	Account  string `json:"open_kfid"`
	Customer string `json:"external_userid"`
	Sent     int64  `json:"send_time"`
	Origin   int    `json:"origin"`
	Servicer string `json:"servicer_userid"`
	Type     string `json:"msgtype"`
	Event    struct {
		Type     string `json:"event_type"`
		Account  string `json:"open_kfid"`
		FailedID string `json:"fail_msgid"`
		Failure  int    `json:"fail_type"`
	} `json:"event"`
}

// 接口已经给出结构化消息；合并记录递归展开，但绝不访问卡片或正文中的任意 URL。
func extractMessage(raw json.RawMessage, depth int) (string, []inbox.MediaSource, error) {
	if depth > 3 {
		return "", nil, errors.New("merged message nesting exceeds limit")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return "", nil, errors.New("invalid message JSON")
	}
	var kind string
	if json.Unmarshal(obj["msgtype"], &kind) != nil {
		return "", nil, errors.New("message type missing")
	}
	switch kind {
	case "text":
		var p struct {
			Content *string `json:"content"`
		}
		if json.Unmarshal(obj[kind], &p) != nil || p.Content == nil || *p.Content == "" {
			return "", nil, errors.New("invalid text message")
		}
		return *p.Content, nil, nil
	case "file", "image", "voice", "video":
		var p struct {
			ID   string `json:"media_id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(obj[kind], &p) != nil || p.ID == "" || len(p.ID) > 512 {
			return "", nil, errors.New("media_id missing or invalid")
		}
		if p.Name == "" {
			p.Name = kind
		}
		return "", []inbox.MediaSource{{MediaID: p.ID, Name: inbox.SafeName(p.Name), Kind: kind}}, nil
	case "link", "location", "business_card", "miniprogram", "msgmenu", "channels_shop_product", "channels_shop_order", "channels":
		part := obj[kind]
		if len(part) == 0 || string(part) == "null" {
			return "", nil, errors.New("message details unavailable")
		}
		var out any
		if json.Unmarshal(part, &out) != nil {
			return "", nil, errors.New("invalid message details")
		}
		text, _ := json.MarshalIndent(out, "", "  ")
		return string(text), nil, nil
	case "merged_msg":
		var p struct {
			Title string `json:"title"`
			Items []struct {
				Sender  string `json:"sender_name"`
				Sent    int64  `json:"send_time"`
				Content string `json:"msg_content"`
			} `json:"item"`
		}
		if json.Unmarshal(obj[kind], &p) != nil || len(p.Items) == 0 || len(p.Items) > 100 {
			return "", nil, errors.New("merged message empty, invalid or exceeds limit")
		}
		texts := []string{p.Title}
		media := []inbox.MediaSource{}
		for _, item := range p.Items {
			t, m, err := extractMessage(json.RawMessage(item.Content), depth+1)
			if err != nil {
				return "", nil, err
			}
			texts = append(texts, fmt.Sprintf("%s [%s]\n%s", item.Sender, time.Unix(item.Sent, 0).UTC().Format(time.RFC3339), t))
			media = append(media, m...)
			if len(media) > 20 {
				return "", nil, errors.New("merged message attachments exceed limit")
			}
		}
		return strings.Join(texts, "\n\n"), media, nil
	default:
		return "", nil, errors.New("unsupported customer-service message type: " + kind)
	}
}
