// Package owner is the switch of a herdr instance from ACFS's Bash to nhm
// (plan-v8 section 1 and section 5 step 6): `nhm admin takeover` imports
// what the Bash kept and makes nhm the owner. It runs under the exclusive
// owner lock (core.SwitchOwner), so no mutator of either side runs while
// it works. ACFS has deleted its Bash agent tools (ACFS acfs-9hkh.33), so
// takeover reads the Bash's files itself, and nothing hands the instance
// back (nhm-te0).
package owner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// State is what ACFS's Bash kept: where each registration token is, and
// each agent's wake state. A token is never in it, only the file that
// holds it.
type State struct {
	Tokens []TokenRef
	Wake   []WakeEntry
}

// TokenRef is where the Bash keeps an identity's registration token.
// ProjectKey is nil when no record names the project; ProjectHash then
// identifies it.
type TokenRef struct {
	ProjectKey  *string
	ProjectHash string
	Name        string
	File        string
}

// WakeEntry is the Bash's wake state of one agent.
type WakeEntry struct {
	ProjectKey  *string
	ProjectHash string
	Name        string
	State       WakeState
}

// WakeState: the Agent Mail delivery cursor the agent was last woken at
// (mail after it wakes the agent), when that was (Unix seconds), and the
// overdue acks it was already woken for. It is the Bash's wake file.
type WakeState struct {
	Cursor   *int64  `json:"cursor"`
	LastWake *int64  `json:"last_wake"`
	Overdue  []int64 `json:"overdue"`
}

var (
	tokenName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	wakeName  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
)

// ProjectHash is the Bash's key for a project: the first 16 hex digits of
// the SHA-256 of its path.
func ProjectHash(project string) string {
	sum := sha256.Sum256([]byte(project))
	return hex.EncodeToString(sum[:])[:16]
}

// ReadState reads the Bash's state under acfsHome as `acfs agents export`
// read it (scripts/lib/herdr_agents.sh in ACFS, before acfs-9hkh.33):
//
//	state/tokens/<hash>/<Name>       a token; .project there names the project
//	state/wake/<hash>/<Name>.json    a wake state; project there names it
//
// <hash> is the project's ProjectHash. A token directory without .project
// takes the project of the wake directory of the same hash. A file whose
// name the Bash would not have written is not its. A missing state
// directory keeps nothing (a fresh install); one that cannot be read
// fails, so no token is missed. A wake file that is not a wake state is
// left out, with a note, as the Bash's wake ignored it.
func ReadState(acfsHome string) (State, []string, error) {
	st := State{Tokens: []TokenRef{}, Wake: []WakeEntry{}}
	var notes []string
	if acfsHome == "" {
		return st, nil, errors.New("owner: no ACFS home ($ACFS_HOME, or ~/.acfs)")
	}
	state := filepath.Join(acfsHome, "state")
	dirs, err := subdirs(filepath.Join(state, "tokens"))
	if err != nil {
		return st, nil, err
	}
	for _, dir := range dirs {
		h := filepath.Base(dir)
		project := firstLine(filepath.Join(dir, ".project"))
		if project == nil {
			project = firstLine(filepath.Join(state, "wake", h, "project"))
		}
		files, err := regularFiles(dir)
		if err != nil {
			return st, nil, err
		}
		for _, name := range files {
			if tokenName.MatchString(name) {
				st.Tokens = append(st.Tokens, TokenRef{ProjectKey: project, ProjectHash: h, Name: name, File: filepath.Join(dir, name)})
			}
		}
	}
	if dirs, err = subdirs(filepath.Join(state, "wake")); err != nil {
		return st, nil, err
	}
	for _, dir := range dirs {
		project := firstLine(filepath.Join(dir, "project"))
		files, err := regularFiles(dir)
		if err != nil {
			return st, nil, err
		}
		for _, file := range files {
			name, ok := strings.CutSuffix(file, ".json")
			if !ok || !wakeName.MatchString(name) {
				continue
			}
			path := filepath.Join(dir, file)
			var ws WakeState
			data, err := os.ReadFile(path)
			if err == nil {
				err = json.Unmarshal(data, &ws)
			}
			if err != nil || ws.Cursor == nil || ws.LastWake == nil || ws.Overdue == nil {
				notes = append(notes, fmt.Sprintf("%s is not a wake state; left out", path))
				continue
			}
			st.Wake = append(st.Wake, WakeEntry{ProjectKey: project, ProjectHash: filepath.Base(dir), Name: name, State: ws})
		}
	}
	return st, notes, nil
}

// subdirs lists the directories in dir that the Bash's glob (dir/*/)
// finds, by name; a missing dir has none.
func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("owner: the Bash's state: %w", err)
	}
	var out []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if info, err := os.Stat(path); !strings.HasPrefix(e.Name(), ".") && err == nil && info.IsDir() {
			out = append(out, path)
		}
	}
	return out, nil
}

// regularFiles lists the regular files in dir (through symlinks) that the
// Bash's glob (dir/*) finds, by name.
func regularFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("owner: the Bash's state: %w", err)
	}
	var out []string
	for _, e := range entries {
		if info, err := os.Stat(filepath.Join(dir, e.Name())); !strings.HasPrefix(e.Name(), ".") && err == nil && info.Mode().IsRegular() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// firstLine is the first line of a file, or nil when the file cannot be
// read or that line is empty (the Bash's herdr_agents_export_project).
func firstLine(path string) *string {
	data, err := os.ReadFile(path)
	line, _, _ := strings.Cut(string(data), "\n")
	if err != nil || line == "" {
		return nil
	}
	return &line
}

// projectOf is an entry's project key, or "" when only its hash is known.
func projectOf(key *string) string {
	if key == nil {
		return ""
	}
	return *key
}
