package inbox

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

type Bot struct {
	Kind         string   `toml:"-" json:"kind,omitempty"`
	ID           string   `toml:"id" json:"id"`
	Name         string   `toml:"name" json:"name"`
	Secret       string   `toml:"secret" json:"-"`
	Enabled      bool     `toml:"enabled" json:"enabled"`
	Receipt      bool     `toml:"receipt" json:"receipt"`
	AllowedUsers []string `toml:"allowed_users" json:"-"`
	AllowedChats []string `toml:"allowed_chats" json:"-"`
}

// 一个连接对应企业微信接管的微信客服应用；账号各自成为独立的收件来源。
type WeChatKF struct {
	ID             string      `toml:"id" json:"id"`
	CorpID         string      `toml:"corp_id" json:"-"`
	Secret         string      `toml:"secret" json:"-"`
	Token          string      `toml:"token" json:"-"`
	EncodingAESKey string      `toml:"encoding_aes_key" json:"-"`
	Enabled        bool        `toml:"enabled" json:"enabled"`
	Accounts       []KFAccount `toml:"accounts" json:"accounts"`
}

type KFAccount struct {
	OpenKfID         string   `toml:"open_kfid" json:"open_kfid"`
	Name             string   `toml:"name" json:"name"`
	Receipt          bool     `toml:"receipt" json:"receipt"`
	AllowedCustomers []string `toml:"allowed_customers" json:"-"`
}

// 来源 ID 绑定真实企业和客服账号，配置改名或轮换密钥不会改变历史消息的归属。
func KFSourceID(corpID, accountID string) string { return "kf:" + corpID + ":" + accountID }

func (c WeChatKF) Source(a KFAccount) Bot {
	return Bot{ID: KFSourceID(c.CorpID, a.OpenKfID), Name: a.Name, Kind: "wechat_kf", Enabled: c.Enabled, Receipt: a.Receipt, AllowedUsers: a.AllowedCustomers}
}

type Consumer struct {
	ID                  string   `toml:"id" json:"id"`
	URL                 string   `toml:"url" json:"-"`
	Secret              string   `toml:"secret" json:"-"`
	Token               string   `toml:"token" json:"-"`
	Bots                []string `toml:"bots" json:"bots"`
	AllowReply          bool     `toml:"allow_reply" json:"allow_reply"`
	AllowPrivateNetwork bool     `toml:"allow_private_network" json:"-"`
}

// 附件整体读入内存后解密，大小上限用于约束内存占用；放宽前需先改为流式下载与解密。
const (
	// MaxFileMBLimit 是单个附件的最大上限，也是 max_file_mb 的默认值（MB）。
	MaxFileMBLimit = 50
	// MaxMessageAttachmentBytes 是单条消息全部附件的合计上限。
	MaxMessageAttachmentBytes int64 = 100 << 20
)

type Config struct {
	DataDir    string     `toml:"data_dir"`
	Listen     string     `toml:"listen"`
	AdminToken string     `toml:"admin_token"`
	PublicURL  string     `toml:"public_url"`
	MaxFileMB  int64      `toml:"max_file_mb"`
	Bots       []Bot      `toml:"bots"`
	Consumers  []Consumer `toml:"consumers"`
	WeChatKF   []WeChatKF `toml:"wechat_kf"`
}

func LoadConfig(path string) (Config, error) {
	c := Config{DataDir: "./data", Listen: "127.0.0.1:8787", MaxFileMB: MaxFileMBLimit}
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return c, fmt.Errorf("load config: %w", err)
	}
	if value := os.Getenv("WECOM_INBOX_ADMIN_TOKEN"); value != "" {
		c.AdminToken = value
	}
	if !filepath.IsAbs(c.DataDir) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return c, err
		}
		c.DataDir = filepath.Join(filepath.Dir(abs), c.DataDir)
	}
	return c, c.Validate()
}

var (
	connectionIDPattern  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	callbackTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9]{3,32}$`)
	platformIDPattern    = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
)

func (c Config) Validate() error {
	if len(c.AdminToken) < 32 {
		return errors.New("admin_token must contain at least 32 characters")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return errors.New("listen must be host:port")
	}
	if c.MaxFileMB < 1 || c.MaxFileMB > MaxFileMBLimit {
		return fmt.Errorf("max_file_mb must be 1..%d", MaxFileMBLimit)
	}
	if c.PublicURL != "" {
		u, e := url.Parse(c.PublicURL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
			return errors.New("public_url must be an HTTPS origin")
		}
	}
	seen := map[string]bool{}
	tokens := map[string]bool{c.AdminToken: true}
	for _, b := range c.Bots {
		if b.ID == "" || len(b.ID) > 200 || seen[b.ID] || strings.HasPrefix(b.ID, "kf:") {
			return errors.New("bot IDs must be unique and nonempty")
		}
		seen[b.ID] = true
		if b.Enabled && (b.Secret == "" || (len(b.AllowedUsers) == 0 && len(b.AllowedChats) == 0)) {
			return fmt.Errorf("bot %s requires a secret and an explicit user/chat allowlist", b.ID)
		}
	}
	connections := map[string]bool{}
	for _, k := range c.WeChatKF {
		if !connectionIDPattern.MatchString(k.ID) || connections[k.ID] {
			return errors.New("wechat_kf connection IDs must be unique URL-safe names")
		}
		connections[k.ID] = true
		if k.Enabled {
			key, err := base64.StdEncoding.DecodeString(k.EncodingAESKey + "=")
			if k.CorpID == "" || k.Secret == "" || !callbackTokenPattern.MatchString(k.Token) || len(k.EncodingAESKey) != 43 || err != nil || len(key) != 32 || len(k.Accounts) == 0 {
				return fmt.Errorf("wechat_kf %s requires corp_id, application secret, callback token, 43-character encoding_aes_key and accounts", k.ID)
			}
		}
		for _, a := range k.Accounts {
			id := KFSourceID(k.CorpID, a.OpenKfID)
			if !platformIDPattern.MatchString(k.CorpID) || !platformIDPattern.MatchString(a.OpenKfID) || seen[id] {
				return errors.New("wechat_kf accounts require unique valid corp_id/open_kfid pairs")
			}
			if k.Enabled && len(a.AllowedCustomers) == 0 {
				return errors.New("wechat_kf accounts require allowed_customers; use [\"*\"] only for an intentional public inbox")
			}
			seen[id] = true
		}
	}
	consumers := map[string]bool{}
	for _, v := range c.Consumers {
		if v.ID == "" || v.ID == "admin" || len(v.ID) > 100 || consumers[v.ID] || strings.ContainsAny(v.ID, "/\\\x00") {
			return errors.New("invalid or duplicate consumer ID")
		}
		consumers[v.ID] = true
		if len(v.Token) < 32 || tokens[v.Token] {
			return errors.New("consumer tokens must be unique and at least 32 characters")
		}
		tokens[v.Token] = true
		if len(v.Bots) == 0 {
			return fmt.Errorf("consumer %s requires bots", v.ID)
		}
		for _, b := range v.Bots {
			if b != "*" && !seen[b] {
				return fmt.Errorf("consumer %s references unknown bot", v.ID)
			}
		}
		if v.URL != "" {
			if len(v.Secret) < 32 {
				return errors.New("webhook secret must contain at least 32 characters")
			}
			if err := ValidateURL(v.URL, v.AllowPrivateNetwork); err != nil {
				return fmt.Errorf("consumer %s: %w", v.ID, err)
			}
		}
	}
	return nil
}

func (c Config) Bot(id string) (Bot, bool) {
	for _, b := range c.Bots {
		if b.ID == id {
			return b, true
		}
	}
	for _, k := range c.WeChatKF {
		for _, a := range k.Accounts {
			if b := k.Source(a); b.ID == id {
				return b, true
			}
		}
	}
	return Bot{}, false
}

func (c Config) Sources() []Bot {
	out := append([]Bot{}, c.Bots...)
	for _, k := range c.WeChatKF {
		for _, a := range k.Accounts {
			out = append(out, k.Source(a))
		}
	}
	return out
}
func (c Config) Consumer(id string) (Consumer, bool) {
	for _, v := range c.Consumers {
		if v.ID == id {
			return v, true
		}
	}
	return Consumer{}, false
}
