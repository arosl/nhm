package cli

import (
	"reflect"
	"testing"
)

// sessionNames is the names of a status or snapshot answer's sessions.
func sessionNames(t *testing.T, raw map[string]any) []string {
	t.Helper()
	var names []string
	for _, s := range raw["sessions"].([]any) {
		names = append(names, s.(map[string]any)["name"].(string))
	}
	return names
}

// --robot-limit and --robot-offset page sessions[] of --robot-status and
// --robot-snapshot as ntm does: after the summary is built, with ntm's
// pagination block and _agent_hints; --limit counts only without
// --robot-limit, and --offset is --robot-offset; no paging flag leaves
// the answer as it is (nhm-2p6.2.4).
func TestRobotStatusAndSnapshotPagination(t *testing.T) {
	s := swarmInstance(t)
	sock := "--socket=" + s.SocketPath
	for _, call := range []string{"--robot-status", "--robot-snapshot"} {
		env, code := robotRun(t, call, "--no-mail", sock)
		if all := sessionNames(t, env.Raw); code != 0 || len(all) != 2 || env.Raw["pagination"] != nil || env.Raw["_agent_hints"] != nil {
			t.Fatalf("%s unpaged = %d %v %v", call, code, all, env.Raw["pagination"])
		}
		summary := env.Raw["summary"]
		for _, c := range []struct {
			args        []string
			names       []string
			page, hints map[string]any
			summarySame bool
		}{
			{[]string{"--robot-limit=1"}, []string{"swarm"},
				map[string]any{"limit": 1.0, "offset": 0.0, "count": 1.0, "total": 2.0, "has_more": true, "next_cursor": 1.0},
				map[string]any{"next_offset": 1.0, "pages_remaining": 1.0}, true},
			{[]string{"--robot-offset=1"}, []string{"other"},
				map[string]any{"limit": 1.0, "offset": 1.0, "count": 1.0, "total": 2.0, "has_more": false},
				map[string]any{"next_offset": 2.0, "pages_remaining": 0.0}, true},
			{[]string{"--limit=1", "--offset=1"}, []string{"other"},
				map[string]any{"limit": 1.0, "offset": 1.0, "count": 1.0, "total": 2.0, "has_more": false},
				map[string]any{"next_offset": 2.0, "pages_remaining": 0.0}, true},
			{[]string{"--robot-limit=2", "--limit=1"}, []string{"swarm", "other"},
				map[string]any{"limit": 2.0, "offset": 0.0, "count": 2.0, "total": 2.0, "has_more": false},
				map[string]any{"next_offset": 2.0, "pages_remaining": 0.0}, true},
			{[]string{"--robot-offset=5"}, nil,
				map[string]any{"limit": 0.0, "offset": 2.0, "count": 0.0, "total": 2.0, "has_more": false},
				nil, true},
		} {
			args := append([]string{call, "--no-mail", sock}, c.args...)
			env, code := robotRun(t, args...)
			if code != 0 {
				t.Fatalf("%v = %d %+v", args, code, env)
			}
			if names := sessionNames(t, env.Raw); len(names) != len(c.names) || (len(names) > 0 && names[0] != c.names[0]) {
				t.Errorf("%v: sessions %v, want %v", args, names, c.names)
			}
			if !sameMap(env.Raw["pagination"], c.page) {
				t.Errorf("%v: pagination %v, want %v", args, env.Raw["pagination"], c.page)
			}
			if c.hints == nil && env.Raw["_agent_hints"] != nil || c.hints != nil && !sameMap(env.Raw["_agent_hints"], c.hints) {
				t.Errorf("%v: _agent_hints %v, want %v", args, env.Raw["_agent_hints"], c.hints)
			}
			if c.summarySame && !reflect.DeepEqual(env.Raw["summary"], summary) {
				t.Errorf("%v: the summary changed with the page: %v", args, env.Raw["summary"])
			}
		}
	}
}

// A negative limit or offset is ntm's INVALID_FLAG, before herdr is read;
// a call that does not page takes the flags and ignores them, as in ntm.
func TestRobotPaginationRefusesNegative(t *testing.T) {
	s := swarmInstance(t)
	sock := "--socket=" + s.SocketPath
	for _, args := range [][]string{{"--robot-status", "--robot-limit=-1"}, {"--robot-snapshot", "--offset=-2"}, {"--robot-status", "--limit=-1"}} {
		env, code := robotRun(t, append(args, sock)...)
		if code != robotError || env.ErrorCode != "INVALID_FLAG" || env.Error != "pagination values must be >= 0" ||
			env.Hint != "Pagination values must be zero or greater" {
			t.Errorf("%v = %d %+v", args, code, env)
		}
	}
	if _, code := robotRun(t, "--robot-tail=swarm", "--robot-limit=-1", sock); code != 0 {
		t.Fatalf("--robot-tail with --robot-limit=-1 = %d", code)
	}
	// A robot flag that the flag set does not define is NOT_IMPLEMENTED
	// even after a global modifier; a non-robot unknown flag is INVALID_FLAG.
	if env, code := robotRun(t, "--robot-status", "--robot-verbosity=terse", "--robot-route=x", sock); code != robotNotImplemented ||
		env.Error != "--robot-route is not in this nhm" {
		t.Fatalf("--robot-route after a modifier = %d %+v", code, env)
	}
	if env, code := robotRun(t, "--robot-status", "--robot-verbosity=terse", "--bogus", sock); code != robotError || env.ErrorCode != "INVALID_FLAG" {
		t.Fatalf("--bogus after a modifier = %d %+v", code, env)
	}
}

// sameMap reports whether got, a decoded JSON object, has exactly want's
// keys and values.
func sameMap(got any, want map[string]any) bool {
	m, ok := got.(map[string]any)
	if !ok || len(m) != len(want) {
		return false
	}
	for k, v := range want {
		if m[k] != v {
			return false
		}
	}
	return true
}
