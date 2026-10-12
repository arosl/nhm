// Package agentmail is nhm's side of MCP Agent Mail.
package agentmail

// Agent Mail's per-pane identity file (its crate mcp-agent-mail-core,
// pane_identity.rs): the one answer to "who is in this pane", read by
// resolve_pane_identity and by the stack tools' pane identity (ACFS
// acfs-7mu.4). nhm writes it for every binding it makes or changes
// (plan-v8 2.7), keyed by the herdr pane id.
//
//	$XDG_CONFIG_HOME/agent-mail/identity/<project_hash>/<pane_key>
//
// project_hash is the first 12 hex digits of the SHA-1 of the project
// key's canonical path. pane_key is the pane id without a leading "%",
// with ":" as "-" and any other character outside [A-Za-z0-9_-] as "_":
// herdr's "w1:p2" is "w1-p2". The content is one line of JSON:
//
//	{"name":"IcyKnoll","pane_id":"w1:p2","written_at":"2026-10-12T04:00:00Z"}
//
// Agent Mail checks a record's liveness against tmux only when it also
// carries tmux facts (session_name, pane_pid, socket_path); nhm writes
// none, so Agent Mail takes the record as written and nhm keeps it true.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/store"
)

const (
	identityDir    = "agent-mail/identity"
	projectHashLen = 12
)

// IdentityRecord is the content of an identity file. Agent Mail's tmux
// fields are read so a record is never misread, and nhm never writes them.
type IdentityRecord struct {
	Name        string `json:"name"`
	PaneID      string `json:"pane_id,omitempty"`
	WrittenAt   string `json:"written_at,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	PanePID     *int   `json:"pane_pid,omitempty"`
	SocketPath  string `json:"socket_path,omitempty"`
}

// ErrSymlink means a directory or the file on the identity path is a
// symlink; Agent Mail refuses those too.
var ErrSymlink = errors.New("agentmail: identity path holds a symlink")

// ConfigHome is $XDG_CONFIG_HOME, or ~/.config, as Agent Mail finds it.
func ConfigHome() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("agentmail: no home directory and no XDG_CONFIG_HOME")
	}
	return filepath.Join(home, ".config"), nil
}

// ProjectHash is Agent Mail's hash of a project key: SHA-1 of the
// canonical absolute path (or of the key as given when it is relative or
// does not exist), first 12 hex digits.
func ProjectHash(projectKey string) string {
	key := projectKey
	if filepath.IsAbs(key) {
		if real, err := filepath.EvalSymlinks(key); err == nil {
			key = real
		}
	}
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:])[:projectHashLen]
}

// PaneKey is the file name Agent Mail uses for a pane id.
func PaneKey(paneID string) string {
	var b strings.Builder
	for _, r := range strings.TrimPrefix(paneID, "%") {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ':':
			b.WriteByte('-')
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// IdentityPath is the identity file of a pane in a project, under
// configHome (ConfigHome when empty).
func IdentityPath(configHome, projectKey, paneID string) (string, error) {
	if configHome == "" {
		var err error
		if configHome, err = ConfigHome(); err != nil {
			return "", err
		}
	}
	return filepath.Join(configHome, identityDir, ProjectHash(projectKey), PaneKey(paneID)), nil
}

// ReadIdentity reads a pane's identity file. ok is false when there is
// none. A legacy file holds a bare name.
func ReadIdentity(configHome, projectKey, paneID string) (rec IdentityRecord, ok bool, err error) {
	path, err := IdentityPath(configHome, projectKey, paneID)
	if err != nil {
		return IdentityRecord{}, false, err
	}
	if err := checkNoSymlinks(configHome, path); err != nil {
		return IdentityRecord{}, false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return IdentityRecord{}, false, nil
	}
	if err != nil {
		return IdentityRecord{}, false, fmt.Errorf("agentmail: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return IdentityRecord{}, false, nil
	}
	if !strings.HasPrefix(text, "{") {
		return IdentityRecord{Name: text}, true, nil
	}
	if err := json.Unmarshal([]byte(text), &rec); err != nil {
		return IdentityRecord{}, false, fmt.Errorf("agentmail: %s: %w", path, err)
	}
	rec.Name = strings.TrimSpace(rec.Name)
	if rec.Name == "" {
		return IdentityRecord{}, false, nil
	}
	return rec, true, nil
}

// WriteIdentity writes a pane's identity file: name and pane id, no tmux
// facts. It returns the path.
func WriteIdentity(configHome, projectKey, paneID, name string, now time.Time) (string, error) {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n") {
		return "", fmt.Errorf("agentmail: invalid agent name %q", name)
	}
	if configHome == "" {
		var err error
		if configHome, err = ConfigHome(); err != nil {
			return "", err
		}
	}
	path, err := IdentityPath(configHome, projectKey, paneID)
	if err != nil {
		return "", err
	}
	if err := mkdirNoFollow(configHome, filepath.Dir(path)); err != nil {
		return "", err
	}
	if err := checkNoSymlinks(configHome, path); err != nil {
		return "", err
	}
	line, err := json.Marshal(IdentityRecord{Name: name, PaneID: paneID, WrittenAt: now.UTC().Format(time.RFC3339)})
	if err != nil {
		return "", fmt.Errorf("agentmail: %w", err)
	}
	if err := store.WriteFileAtomic(path, append(line, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("agentmail: %w", err)
	}
	return path, nil
}

// RemoveIdentity removes a pane's identity file when it names name, so a
// retired or displaced binding does not leave its name on the pane. It
// reports whether it removed the file.
func RemoveIdentity(configHome, projectKey, paneID, name string) (bool, error) {
	rec, ok, err := ReadIdentity(configHome, projectKey, paneID)
	if err != nil || !ok || !strings.EqualFold(rec.Name, name) {
		return false, err
	}
	path, err := IdentityPath(configHome, projectKey, paneID)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("agentmail: %w", err)
	}
	return true, nil
}

// mkdirNoFollow creates dir below configHome one level at a time (0700),
// and refuses a level that is a symlink or not a directory, so nothing is
// created through a link.
func mkdirNoFollow(configHome, dir string) error {
	rel, err := filepath.Rel(configHome, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("agentmail: %s is not below %s", dir, configHome)
	}
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		return fmt.Errorf("agentmail: %w", err)
	}
	p := configHome
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			if err := os.Mkdir(p, 0o700); err != nil && !os.IsExist(err) {
				return fmt.Errorf("agentmail: %w", err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("agentmail: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, p)
		}
		if !info.IsDir() {
			return fmt.Errorf("agentmail: %s is not a directory", p)
		}
	}
	return nil
}

// checkNoSymlinks refuses a symlink in the identity directories below
// configHome and on the file itself.
func checkNoSymlinks(configHome, path string) error {
	if configHome == "" {
		var err error
		if configHome, err = ConfigHome(); err != nil {
			return err
		}
	}
	rel, err := filepath.Rel(configHome, path)
	if err != nil {
		return fmt.Errorf("agentmail: %w", err)
	}
	p := configHome
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("agentmail: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, p)
		}
	}
	return nil
}
