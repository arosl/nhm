// Package owner is the switch of a herdr instance between ACFS's Bash and
// nhm (plan-v8 section 1 and section 5 step 6): `nhm admin takeover`
// imports what the Bash keeps and makes nhm the owner; `nhm admin
// handback` gives it back, and refuses when the state cannot go back
// without loss. Both run under the exclusive owner lock (core.SwitchOwner),
// so no mutator of either side runs while they work.
package owner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// The Bash's state format: `acfs agents export` prints it and `acfs agents
// import` reads it (scripts/lib/herdr_agents.sh in ACFS).
const (
	StateFormat  = "acfs-agents-state"
	StateVersion = 1
)

// State is an acfs-agents-state document. A token is never in it, only
// the file that holds it.
type State struct {
	Format     string      `json:"format"`
	Version    int         `json:"version"`
	ExportedAt string      `json:"exported_at"`
	AcfsHome   string      `json:"acfs_home,omitempty"`
	Tokens     []TokenRef  `json:"tokens"`
	Wake       []WakeEntry `json:"wake"`
	Keep       []string    `json:"keep"`
}

// TokenRef is where the Bash keeps an identity's registration token.
// ProjectKey is nil when no record names the project; ProjectHash then
// identifies it.
type TokenRef struct {
	ProjectKey  *string `json:"project_key"`
	ProjectHash string  `json:"project_hash"`
	Name        string  `json:"name"`
	File        string  `json:"file"`
}

// WakeEntry is the Bash's wake state of one agent.
type WakeEntry struct {
	ProjectKey  *string   `json:"project_key"`
	ProjectHash string    `json:"project_hash"`
	Name        string    `json:"name"`
	State       WakeState `json:"state"`
}

// WakeState: the Agent Mail delivery cursor the agent was last woken at
// (mail after it wakes the agent), when that was (Unix seconds), and the
// overdue acks it was already woken for.
type WakeState struct {
	Cursor   *int64  `json:"cursor"`
	LastWake *int64  `json:"last_wake"`
	Overdue  []int64 `json:"overdue"`
}

var (
	tokenName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	wakeName  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	hash16    = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// ProjectHash is the Bash's key for a project: the first 16 hex digits of
// the SHA-256 of its path.
func ProjectHash(project string) string {
	sum := sha256.Sum256([]byte(project))
	return hex.EncodeToString(sum[:])[:16]
}

// ParseState reads and checks an acfs-agents-state document. Every entry
// must be whole: a state with a wrong entry is refused, never imported in
// part.
func ParseState(data []byte) (State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("owner: the Bash's state is not JSON: %w", err)
	}
	if s.Format != StateFormat || s.Version != StateVersion {
		return State{}, fmt.Errorf("owner: the Bash's state is %q version %d, not %s version %d", s.Format, s.Version, StateFormat, StateVersion)
	}
	if s.Tokens == nil || s.Wake == nil || s.Keep == nil {
		return State{}, errors.New("owner: the Bash's state lacks tokens, wake or keep")
	}
	var errs []error
	project := func(key *string, h string) error {
		switch {
		case key != nil && *key != "":
			return nil
		case key == nil && hash16.MatchString(h):
			return nil
		}
		return errors.New("needs project_key or project_hash")
	}
	for i, t := range s.Tokens {
		if err := project(t.ProjectKey, t.ProjectHash); err != nil || !tokenName.MatchString(t.Name) || t.File == "" {
			errs = append(errs, fmt.Errorf("tokens[%d] (%s): needs a project, an Agent Mail name and a file", i, t.Name))
		}
	}
	for i, w := range s.Wake {
		if err := project(w.ProjectKey, w.ProjectHash); err != nil || !wakeName.MatchString(w.Name) ||
			w.State.Cursor == nil || w.State.LastWake == nil || w.State.Overdue == nil {
			errs = append(errs, fmt.Errorf("wake[%d] (%s): needs a project, an Agent Mail name and a wake state", i, w.Name))
		}
	}
	if len(errs) > 0 {
		return State{}, fmt.Errorf("owner: the Bash's state: %w", errors.Join(errs...))
	}
	return s, nil
}

// projectOf is an entry's project key, or "" when only its hash is known.
func projectOf(key *string) string {
	if key == nil {
		return ""
	}
	return *key
}
