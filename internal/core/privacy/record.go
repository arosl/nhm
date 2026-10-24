package privacy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// A session's settings live in the instance's store, where every nhm
// process finds them. ntm records them as an option on the tmux session
// ("privacy,allow-persist"); nhm keys them by the session's herdr
// workspace id in the meta table, which outlives a herdr restart, while
// herdr's metadata tokens do not.
const metaPrefix = "privacy/"

// lookupTimeout bounds one store read of the Manager's lookup.
const lookupTimeout = 2 * time.Second

// Record stores the settings of the session in workspace ws. With neither
// set it removes them, so a new workspace never takes the settings of an
// old one with the same id.
func Record(ctx context.Context, db *store.DB, ws string, privacyMode, allowPersist bool) error {
	key := metaPrefix + ws
	var parts []string
	if privacyMode {
		parts = append(parts, "privacy")
	}
	if allowPersist {
		parts = append(parts, "allow-persist")
	}
	var err error
	if len(parts) == 0 {
		_, err = db.SQL().ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, key)
	} else {
		_, err = db.SQL().ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, strings.Join(parts, ","))
	}
	if err != nil {
		return fmt.Errorf("privacy: record %s: %w", ws, err)
	}
	return nil
}

// Recorded reads the settings Record stored for workspace ws; ok is false
// when it has none.
func Recorded(ctx context.Context, db *store.DB, ws string) (state SessionState, ok bool, err error) {
	v, err := db.Meta(ctx, metaPrefix+ws)
	if err != nil || v == "" {
		return SessionState{}, false, err
	}
	for _, part := range strings.Split(v, ",") {
		switch strings.TrimSpace(part) {
		case "privacy":
			state.PrivacyMode, ok = true, true
		case "allow-persist":
			state.AllowPersist, ok = true, true
		}
	}
	return state, ok, nil
}

// StoreLookup is the Manager's session lookup over db: the session is a
// workspace id. A store that cannot be read records nothing, as ntm's
// lookup treats a tmux that cannot be asked.
func StoreLookup(db *store.DB) func(string) (*SessionState, bool) {
	return func(ws string) (*SessionState, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		defer cancel()
		state, ok, err := Recorded(ctx, db, ws)
		if err != nil || !ok {
			return nil, false
		}
		return &state, true
	}
}
