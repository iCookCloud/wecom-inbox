package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/iCookCloud/wecom-inbox/internal/inbox"
	"github.com/iCookCloud/wecom-inbox/internal/wecom"
	"github.com/iCookCloud/wecom-inbox/web"
)

var version = "dev"

func main() {
	syscall.Umask(0077)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println("WeCom Inbox " + version + "\nCommands: init, serve, check, log, import, backup, restore, version\nUse COMMAND -h for options.")
		return nil
	}
	if args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	cfgDefault := os.Getenv("WECOM_INBOX_CONFIG")
	if cfgDefault == "" {
		cfgDefault = os.Getenv("WECOM_GW_CONFIG")
	}
	if cfgDefault == "" {
		cfgDefault = "config.toml"
	}
	configPath := flags.String("c", cfgDefault, "config file")
	out := flags.String("out", "", "new output directory")
	file := flags.String("file", "", "legacy JSONL file")
	media := flags.String("media-root", ".", "allowed legacy media root")
	from := flags.String("from", "", "backup source / earliest timestamp")
	to := flags.String("to", "", "latest timestamp")
	bot := flags.String("bot", "", "bot ID")
	user := flags.String("user", "", "sender ID")
	kind := flags.String("type", "", "message type")
	query := flags.String("q", "", "search text or filename")
	cursor := flags.String("cursor", "", "pagination cursor")
	limit := flags.Int("limit", 50, "page size, max 200")
	asJSON := flags.Bool("json", false, "JSON output")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if args[0] == "init" {
		return initialize(*configPath)
	}
	if args[0] == "restore" {
		if *from == "" || *out == "" {
			return errors.New("restore requires --from and --out")
		}
		return inbox.RestoreBackup(*from, *out)
	}
	c, err := inbox.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if args[0] == "check" {
		fmt.Printf("Config valid: %d bot(s), %d consumer(s). No network requests made.\n", len(c.Bots), len(c.Consumers))
		return nil
	}
	lock, err := acquireLock(c.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	s, err := inbox.OpenStore(c.DataDir)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	switch args[0] {
	case "serve":
		return serve(c, *configPath, s)
	case "log":
		p, e := s.List(ctx, inbox.Filter{BotID: *bot, Query: *query, SenderID: *user, Type: *kind, From: *from, To: *to, Cursor: *cursor, Limit: *limit})
		if e != nil {
			return e
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(p)
		}
		for _, m := range p.Messages {
			fmt.Printf("%d\t%s\t%s\t%s\t%s\n", m.ID, m.ReceivedAt, m.BotID, m.Status, m.Content)
		}
		if p.NextCursor != "" {
			fmt.Println("Next cursor:", p.NextCursor)
		}
		return nil
	case "import":
		if *file == "" {
			return errors.New("import requires --file")
		}
		f, e := os.Open(*file)
		if e != nil {
			return e
		}
		defer f.Close()
		report, e := s.ImportJSONL(ctx, f, *media)
		json.NewEncoder(os.Stdout).Encode(report)
		if e != nil {
			return e
		}
		if len(report.InvalidLines) > 0 || report.FailedAttachments > 0 {
			return errors.New("import completed with problems; see line numbers and failed attachments")
		}
		return nil
	case "backup":
		if *out == "" {
			return errors.New("backup requires --out")
		}
		return s.Backup(ctx, *out)
	default:
		return errors.New("unknown command; use --help")
	}
}

func acquireLock(dir string) (*os.File, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("data directory is in use; stop serve before offline maintenance, or use the HTTP API")
	}
	return f, nil
}

func initialize(path string) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = fmt.Fprintf(f, `# Private configuration. Do not commit this file.
data_dir = "./data"
listen = "127.0.0.1:8787"
admin_token = "%s%s"
max_file_mb = 50

# Add your own bot; explicitly authorize users or chats.
# [[bots]]
# id = "YOUR_BOT_ID"
# name = "Business inbox"
# secret = "YOUR_BOT_SECRET"
# enabled = true
# receipt = true
# allowed_users = ["YOUR_WECOM_USER_ID"]
# allowed_chats = []
`, inbox.RandomID(), inbox.RandomID())
	if e == nil {
		e = f.Sync()
	}
	if e == nil {
		fmt.Println("Created private config. Open it locally to obtain your sign-in token and configure a Bot.")
	}
	return e
}

type configState struct {
	mu    sync.RWMutex
	value inbox.Config
}

func (c *configState) get() inbox.Config { c.mu.RLock(); defer c.mu.RUnlock(); return c.value }

func serve(initial inbox.Config, path string, s *inbox.Store) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config := &configState{value: initial}
	ingest := &inbox.Ingestor{Store: s, Config: config.get}
	dispatch := &inbox.Dispatcher{Store: s, Config: config.get}
	manager := wecom.NewManager(func(ctx context.Context, bot, req string, body []byte) error {
		_, _, e := ingest.Accept(ctx, bot, req, body)
		return e
	})
	manager.Reconcile(ctx, initial.Bots)
	defer manager.Stop()
	app := &inbox.Server{Store: s, Config: config.get, Assets: web.Assets, BotStates: func() map[string]string {
		out := map[string]string{}
		for _, b := range manager.Statuses() {
			out[b.ID] = b.State
		}
		return out
	}}
	server := &http.Server{Addr: initial.Listen, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	var wg sync.WaitGroup
	worker := func(name string, fn func(context.Context) (bool, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				_, e := fn(ctx)
				if e != nil && ctx.Err() == nil {
					slog.Error(name, "error", e)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(200 * time.Millisecond):
				}
			}
		}()
	}
	worker("archive worker", ingest.ProcessOne)
	worker("delivery worker", dispatch.DeliverOne)
	worker("reply worker", func(ctx context.Context) (bool, error) { return dispatch.ReplyOne(ctx, manager) })
	wg.Add(1)
	go func() {
		defer wg.Done()
		var hash [32]byte
		if b, e := os.ReadFile(path); e == nil {
			hash = sha256.Sum256(b)
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b, e := os.ReadFile(path)
				if e != nil {
					continue
				}
				next := sha256.Sum256(b)
				if next == hash {
					continue
				}
				hash = next
				c, e := inbox.LoadConfig(path)
				if e != nil {
					slog.Warn("invalid updated config; keeping previous configuration", "error", e)
					continue
				}
				if c.DataDir != initial.DataDir || c.Listen != initial.Listen {
					slog.Warn("data_dir/listen changes require restart")
					continue
				}
				config.mu.Lock()
				config.value = c
				config.mu.Unlock()
				manager.Reconcile(ctx, c.Bots)
				slog.Info("configuration reloaded")
			}
		}
	}()
	errCh := make(chan error, 1)
	go func() {
		slog.Info("WeCom Inbox ready", "address", initial.Listen, "version", version)
		errCh <- server.ListenAndServe()
	}()
	var result error
	select {
	case <-ctx.Done():
	case e := <-errCh:
		if !errors.Is(e, http.ErrServerClosed) {
			result = e
		}
		stop()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	stop()
	manager.Stop()
	wg.Wait()
	return result
}
