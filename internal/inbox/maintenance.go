package inbox

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ImportReport struct {
	Imported          int   `json:"imported"`
	Duplicates        int   `json:"duplicates"`
	FailedAttachments int   `json:"failed_attachments"`
	InvalidLines      []int `json:"invalid_lines"`
}

// 导入仅复制指定根目录内的历史文件，不读取 raw 中的 URL，也不产生订阅或回复。
func (s *Store) ImportJSONL(ctx context.Context, input io.Reader, mediaRoot string) (ImportReport, error) {
	report := ImportReport{InvalidLines: []int{}}
	root, err := os.OpenRoot(mediaRoot)
	if err != nil {
		return report, err
	}
	defer root.Close()
	absoluteRoot, err := filepath.Abs(mediaRoot)
	if err != nil {
		return report, err
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	line := 0
	for scanner.Scan() {
		line++
		data := scanner.Bytes()
		if len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		var old struct {
			TS       string `json:"ts"`
			BotID    string `json:"bot_id"`
			MsgID    string `json:"msgid"`
			ChatType string `json:"chat_type"`
			ChatID   string `json:"chat_id"`
			UserID   string `json:"from_userid"`
			Type     string `json:"msgtype"`
			Content  string `json:"content"`
			FilePath string `json:"file_path"`
			FileName string `json:"file_name"`
		}
		if json.Unmarshal(data, &old) != nil || old.BotID == "" {
			report.InvalidLines = append(report.InvalidLines, line)
			continue
		}
		at, e := time.Parse(time.RFC3339Nano, old.TS)
		if e != nil {
			report.InvalidLines = append(report.InvalidLines, line)
			continue
		}
		if old.MsgID == "" {
			h := sha256.Sum256(data)
			old.MsgID = "legacy-" + hex.EncodeToString(h[:])
		}
		in := Incoming{Source: "legacy", Message: Message{BotID: old.BotID, PlatformID: old.MsgID, ChatType: old.ChatType, ChatID: old.ChatID, SenderID: old.UserID, Type: old.Type, Content: old.Content, ReceivedAt: at.UTC().Format(timestampLayout)}}
		paths := []string{}
		if old.FilePath != "" {
			paths = strings.Split(old.FilePath, ";")
		}
		for _, p := range paths {
			name := filepath.Base(p)
			if len(paths) == 1 && old.FileName != "" {
				name = old.FileName
			}
			in.Media = append(in.Media, MediaSource{Name: SafeName(name), Kind: "imported"})
		}
		id, created, e := s.Put(ctx, in)
		if e != nil {
			return report, e
		}
		m, e := s.Get(ctx, id)
		if e != nil {
			return report, e
		}
		if !created && m.Status != "importing" {
			report.Duplicates++
			for _, a := range m.Attachments {
				if a.Status != "ready" {
					report.FailedAttachments++
				}
			}
			continue
		}
		if len(m.Attachments) != len(paths) {
			return report, errors.New("legacy attachment manifest differs from original import")
		}
		m, e = s.Get(ctx, id)
		if e != nil {
			return report, e
		}
		for n, p := range paths {
			a := m.Attachments[n]
			if a.Status == "ready" {
				f, e := s.OpenAttachment(a)
				if e != nil {
					return report, e
				}
				f.Close()
				continue
			}
			recovered, recoverErr := s.RecoverAttachment(ctx, a)
			if recoverErr != nil {
				return report, recoverErr
			}
			if recovered {
				continue
			}
			relative := p
			if filepath.IsAbs(p) {
				relative, e = filepath.Rel(absoluteRoot, p)
				if e != nil {
					relative = "../invalid"
				}
			}
			f, openErr := root.Open(relative)
			var contents []byte
			if openErr == nil {
				info, statErr := f.Stat()
				if statErr != nil || !info.Mode().IsRegular() {
					openErr = errors.New("source is not a regular file")
				} else {
					contents, openErr = io.ReadAll(io.LimitReader(f, (MaxFileMBLimit<<20)+1))
					if len(contents) > MaxFileMBLimit<<20 {
						openErr = errors.New("source exceeds size limit")
					}
				}
				f.Close()
			}
			if openErr != nil {
				a.Status = "failed"
				a.Error = "legacy attachment missing, too large or outside media root"
				if e = s.SaveAttachment(ctx, a); e != nil {
					return report, e
				}
				report.FailedAttachments++
			} else if e = s.WriteAttachment(ctx, a, contents, in.Media[n].Name); e != nil {
				return report, e
			}
		}
		if e = s.Finalize(ctx, id, Config{}, ""); e != nil {
			return report, e
		}
		report.Imported++
	}
	return report, scanner.Err()
}

type backupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type backupManifest struct {
	Version   int          `json:"version"`
	CreatedAt string       `json:"created_at"`
	Files     []backupFile `json:"files"`
}

func copyChecked(source *os.Root, path, destination string) (backupFile, error) {
	result := backupFile{Path: path}
	in, e := source.Open(path)
	if e != nil {
		return result, e
	}
	defer in.Close()
	info, e := in.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return result, errors.New("backup source must be a regular file")
	}
	if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
		return result, e
	}
	out, e := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return result, e
	}
	defer out.Close()
	h := sha256.New()
	result.Size, e = io.Copy(io.MultiWriter(out, h), in)
	if e != nil {
		return result, e
	}
	if e = out.Sync(); e != nil {
		return result, e
	}
	result.SHA256 = hex.EncodeToString(h.Sum(nil))
	return result, nil
}

func snapshotAttachments(dir string) ([]Attachment, error) {
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "inbox.db")}
	db, e := sql.Open("sqlite", u.String()+"?mode=ro")
	if e != nil {
		return nil, e
	}
	defer db.Close()
	rows, e := db.Query("SELECT id,message_id,name,kind,size,sha256,status,error,storage_key FROM attachments")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		if e = rows.Scan(&a.ID, &a.MessageID, &a.Name, &a.Kind, &a.Size, &a.SHA256, &a.Status, &a.Error, &a.StorageKey); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func verifyArchive(dir string) error {
	items, e := snapshotAttachments(dir)
	if e != nil {
		return e
	}
	s := Store{Dir: dir}
	for _, a := range items {
		if a.Status == "ready" {
			f, e := s.OpenAttachment(a)
			if e != nil {
				return fmt.Errorf("archive integrity failure for attachment %s", a.ID)
			}
			f.Close()
		}
	}
	return nil
}

func (s *Store) Backup(ctx context.Context, destination string) error {
	if e := os.Mkdir(destination, 0700); e != nil {
		return fmt.Errorf("backup requires a new directory: %w", e)
	}
	if e := s.Snapshot(ctx, filepath.Join(destination, "inbox.db")); e != nil {
		return e
	}
	if e := os.Chmod(filepath.Join(destination, "inbox.db"), 0600); e != nil {
		return e
	}
	root, e := os.OpenRoot(s.Dir)
	if e != nil {
		return e
	}
	defer root.Close()
	manifest := backupManifest{Version: 1, CreatedAt: now(), Files: []backupFile{}}
	key, e := copyChecked(root, "encryption.key", filepath.Join(destination, "encryption.key"))
	if e != nil {
		return e
	}
	manifest.Files = append(manifest.Files, key)
	f, e := os.Open(filepath.Join(destination, "inbox.db"))
	if e != nil {
		return e
	}
	h := sha256.New()
	size, e := io.Copy(h, f)
	f.Close()
	if e != nil {
		return e
	}
	manifest.Files = append(manifest.Files, backupFile{Path: "inbox.db", Size: size, SHA256: hex.EncodeToString(h.Sum(nil))})
	items, e := snapshotAttachments(destination)
	if e != nil {
		return e
	}
	for _, a := range items {
		key := a.ID + ".blob"
		if a.Status == "ready" {
			entry, e := copyChecked(root, filepath.Join("files", a.StorageKey), filepath.Join(destination, "files", a.StorageKey))
			if e != nil {
				return e
			}
			if entry.Size != a.Size || entry.SHA256 != a.SHA256 {
				return fmt.Errorf("archived attachment %s failed integrity validation", a.ID)
			}
			manifest.Files = append(manifest.Files, entry)
		}
		for _, suffix := range []string{"", ".pending", ".manifest.json"} {
			if suffix == "" && a.Status == "ready" {
				continue
			}
			path := filepath.Join("files", key+suffix)
			_, e := root.Stat(path)
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				return e
			}
			entry, e := copyChecked(root, path, filepath.Join(destination, path))
			if e != nil {
				return e
			}
			manifest.Files = append(manifest.Files, entry)
		}
	}
	if e = verifyArchive(destination); e != nil {
		return e
	}
	p, e := json.MarshalIndent(manifest, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(destination, "manifest.json"), p, 0600)
}

func RestoreBackup(source, destination string) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.Open("manifest.json")
	if err != nil {
		return err
	}
	var manifest backupManifest
	err = json.NewDecoder(io.LimitReader(f, 8<<20)).Decode(&manifest)
	f.Close()
	if err != nil || manifest.Version != 1 || len(manifest.Files) < 2 {
		return errors.New("invalid backup manifest")
	}
	seen := map[string]bool{}
	for _, v := range manifest.Files {
		p := filepath.ToSlash(v.Path)
		if (p != "encryption.key" && p != "inbox.db" && !(strings.HasPrefix(p, "files/") && !strings.Contains(strings.TrimPrefix(p, "files/"), "/"))) || !filepath.IsLocal(v.Path) || seen[p] {
			return errors.New("invalid backup path")
		}
		seen[p] = true
	}
	if !seen["encryption.key"] || !seen["inbox.db"] {
		return errors.New("backup must include database and encryption key")
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return errors.New("restore requires a new destination directory")
	}
	for _, expected := range manifest.Files {
		actual, e := copyChecked(root, expected.Path, filepath.Join(destination, expected.Path))
		if e != nil {
			return e
		}
		if actual.Size != expected.Size || actual.SHA256 != expected.SHA256 {
			return fmt.Errorf("backup checksum mismatch for %s; incomplete destination must not be used", expected.Path)
		}
	}
	return verifyArchive(destination)
}
