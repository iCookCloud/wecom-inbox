package inbox

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Bot struct {
	ID           string   `toml:"id" json:"id"`
	Name         string   `toml:"name" json:"name"`
	Secret       string   `toml:"secret" json:"-"`
	Enabled      bool     `toml:"enabled" json:"enabled"`
	Receipt      bool     `toml:"receipt" json:"receipt"`
	AllowedUsers []string `toml:"allowed_users" json:"-"`
	AllowedChats []string `toml:"allowed_chats" json:"-"`
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

type Config struct {
	DataDir    string     `toml:"data_dir"`
	Listen     string     `toml:"listen"`
	AdminToken string     `toml:"admin_token"`
	PublicURL  string     `toml:"public_url"`
	MaxFileMB  int64      `toml:"max_file_mb"`
	Bots       []Bot      `toml:"bots"`
	Consumers  []Consumer `toml:"consumers"`
}

func LoadConfig(path string) (Config, error) {
	c := Config{DataDir: "./data", Listen: "127.0.0.1:8787", MaxFileMB: 50}
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

func (c Config) Validate() error {
	if len(c.AdminToken) < 32 {
		return errors.New("admin_token must contain at least 32 characters")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return errors.New("listen must be host:port")
	}
	if c.MaxFileMB < 1 || c.MaxFileMB > 50 {
		return errors.New("max_file_mb must be 1..50")
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
		if b.ID == "" || len(b.ID) > 200 || seen[b.ID] {
			return errors.New("bot IDs must be unique and nonempty")
		}
		seen[b.ID] = true
		if b.Enabled && (b.Secret == "" || (len(b.AllowedUsers) == 0 && len(b.AllowedChats) == 0)) {
			return fmt.Errorf("bot %s requires a secret and an explicit user/chat allowlist", b.ID)
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
	return Bot{}, false
}
func (c Config) Consumer(id string) (Consumer, bool) {
	for _, v := range c.Consumers {
		if v.ID == id {
			return v, true
		}
	}
	return Consumer{}, false
}
