package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

// maxSearchDepth bounds the walk for a transcript under a kind's dir.
const maxSearchDepth = 6

// maxCompanionBytes bounds the companion files copied with a transcript.
const maxCompanionBytes = 256 << 20

// ErrNoTranscript means nhm could not find the session's transcript.
var ErrNoTranscript = errors.New("no transcript found")

// Locate finds a kind's transcript for a native session: a path herdr
// reported, or the newest .jsonl under the kind's transcript dir whose
// name holds the session id.
func Locate(home string, a kinds.Adapter, ns *bindings.NativeSession) (string, error) {
	if ns == nil || ns.Value == "" {
		return "", fmt.Errorf("%w: the binding has no native session", ErrNoTranscript)
	}
	if ns.Kind == "path" && filepath.IsAbs(ns.Value) {
		if fi, err := os.Stat(ns.Value); err == nil && fi.Mode().IsRegular() {
			return ns.Value, nil
		}
	}
	if a.Transcript.Dir == "" {
		return "", fmt.Errorf("%w: nhm does not know where %s keeps transcripts", ErrNoTranscript, a.Name)
	}
	root := filepath.Join(home, a.Transcript.Dir)
	best, bestTime := "", time.Time{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			if rel != "." && strings.Count(rel, string(filepath.Separator))+1 > maxSearchDepth {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".jsonl") || !strings.Contains(name, ns.Value) || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(bestTime) {
			best, bestTime = p, info.ModTime()
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoTranscript, err)
	}
	if best == "" {
		return "", fmt.Errorf("%w for session %s under %s", ErrNoTranscript, ns.Value, root)
	}
	return best, nil
}

// Preserved is a transcript copy and its metadata (plan-v8 2.13's
// artifact rules).
type Preserved struct {
	Path          string    `json:"path"`
	Meta          string    `json:"meta"`
	Original      string    `json:"original_path"`
	Provider      string    `json:"provider"`
	Format        string    `json:"format,omitempty"`
	NativeSession string    `json:"native_session"`
	Project       string    `json:"project"`
	MailName      string    `json:"mail_name"`
	BindingID     string    `json:"binding_id"`
	Generation    int64     `json:"generation"`
	Reason        string    `json:"reason"`
	CapturedAt    time.Time `json:"captured_at"`
	Bytes         int64     `json:"bytes"`
	SHA256        string    `json:"sha256"`
	Companions    []string  `json:"companions,omitempty"`
}

// Preserve copies a transcript into <instance>/transcripts/<binding>/,
// with its companion dir (the dir beside it named after the session), a
// .meta.json and a transcripts row. A second capture of the same reason
// replaces the first: the final capture after the agent exits wins,
// because providers write on exit.
func Preserve(ctx context.Context, db *store.DB, instanceDir string, b bindings.Binding, original, reason string, now time.Time) (Preserved, error) {
	a := kinds.Lookup(b.Kind)
	p := Preserved{Original: original, Provider: b.Kind, Format: a.Transcript.Format, Project: b.MailProjectKey,
		MailName: b.MailName, BindingID: b.ID, Generation: b.Generation, Reason: reason, CapturedAt: now.UTC()}
	if b.NativeSession != nil {
		p.NativeSession = b.NativeSession.Value
	}
	dir := filepath.Join(instanceDir, "transcripts", b.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return p, err
	}
	base := fmt.Sprintf("g%d-%s-%s", b.Generation, reason, filepath.Base(original))
	p.Path, p.Meta = filepath.Join(dir, base), filepath.Join(dir, base+".meta.json")
	n, sum, err := copyFile(original, p.Path)
	if err != nil {
		return p, err
	}
	p.Bytes, p.SHA256 = n, sum
	if p.NativeSession != "" {
		companion := filepath.Join(filepath.Dir(original), p.NativeSession)
		if fi, err := os.Stat(companion); err == nil && fi.IsDir() {
			p.Companions, _ = copyTree(companion, p.Path+".companion", maxCompanionBytes)
		}
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	if err := store.WriteFileAtomic(p.Meta, append(data, '\n'), 0o600); err != nil {
		return p, err
	}
	_, err = db.SQL().ExecContext(ctx, `INSERT INTO transcripts (binding_id, generation, path, native_session, reason, preserved_at)
		VALUES (?, ?, ?, ?, ?, ?)`, b.ID, b.Generation, p.Path, nullStr(p.NativeSession), reason, store.FormatTime(now))
	if err != nil {
		return p, fmt.Errorf("lifecycle: transcripts: %w", err)
	}
	return p, nil
}

func copyFile(src, dst string) (int64, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// copyTree copies regular files under src to dst, up to limit bytes. It
// returns the copied relative paths.
func copyTree(src, dst string, limit int64) ([]string, error) {
	var copied []string
	var total int64
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || total+info.Size() > limit {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if _, _, err := copyFile(p, target); err != nil {
			return nil
		}
		total += info.Size()
		copied = append(copied, rel)
		return nil
	})
	return copied, err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
