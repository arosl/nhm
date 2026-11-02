// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/pressure/provider_test.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes (nhm-a421): this
// header; ntm's parser tests folded into tables; nhm's tests of the
// SystemProvider on a /proc and cgroup tree of the test's own (cgroup.go).
package pressure

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestParseMemoryRatio(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want float64
		ok   bool
	}{
		{"standard", "MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 250 kB\n", 0.75, true},
		{"no MemAvailable", "MemTotal: 1000 kB\nMemFree: 100 kB\n", 0, false},
		{"empty", "", 0, false},
		{"total zero", "MemTotal: 0 kB\nMemAvailable: 0 kB\n", 0, false},
		{"available zero", "MemTotal: 1000 kB\nMemAvailable: 0 kB\n", 1, true},
		{"negative available", "MemTotal: 1000 kB\nMemAvailable: -5 kB\n", 1, true},
		{"malformed lines", "garbage\nMemTotal: x kB\nMemTotal: 1000 kB\nMemAvailable: 500 kB\n", 0.5, true},
	} {
		got, ok := parseMemoryRatio(c.raw)
		if ok != c.ok || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: = %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestParseProcessTotalFromLoadavg(t *testing.T) {
	if got, ok := parseProcessTotalFromLoadavg("0.10 0.20 0.30 2/1234 999\n"); !ok || got != 1234 {
		t.Fatalf("= %d %v", got, ok)
	}
	for _, raw := range []string{"", "0.1 0.2 0.3", "0.1 0.2 0.3 1234 9", "0.1 0.2 0.3 1/x 9", "0.1 0.2 0.3 1/-4 9"} {
		if _, ok := parseProcessTotalFromLoadavg(raw); ok {
			t.Errorf("%q parsed", raw)
		}
	}
	if r, ok := processCountRatio(9000, 4096); !ok || r != 1 {
		t.Errorf("ratio = %v %v, want 1 (clamped)", r, ok)
	}
	if _, ok := processCountRatio(1, 0); ok {
		t.Error("a zero limit gave a ratio")
	}
	p := &SystemProvider{CPUs: func() int { return 2 }}
	if got := p.defaultProcessCountLimit(); got != minProcessCountLimit {
		t.Errorf("limit on 2 CPUs = %d, want %d", got, minProcessCountLimit)
	}
	p.CPUs = func() int { return 32 }
	if got := p.defaultProcessCountLimit(); got != 32*processCountPerCPU {
		t.Errorf("limit on 32 CPUs = %d", got)
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// host is a /proc as an LXC container with lxcfs shows it on the host of
// 2026-11-02: the host's MemTotal (184 GiB) and its task count (7770).
func host(t *testing.T) string {
	proc := t.TempDir()
	write(t, filepath.Join(proc, "meminfo"), "MemTotal: 193077976 kB\nMemFree: 126429488 kB\nMemAvailable: 174992024 kB\n")
	write(t, filepath.Join(proc, "loadavg"), "20.78 21.09 20.63 36/7770 2838440\n")
	write(t, filepath.Join(proc, "stat"), "cpu 100 0 100 800 0 0 0 0 0 0\n")
	write(t, filepath.Join(proc, "self", "cgroup"), "0::/user.slice/user-1000.slice/session-c22.scope\n")
	return proc
}

// container is the container's cgroup namespace: a 64 GiB memory.high
// almost full of page cache, 5605 tasks of 30000, and nhm's session
// scope below it with no limits.
func container(t *testing.T, current, activeFile, inactiveFile, slab uint64) string {
	root := t.TempDir()
	write(t, filepath.Join(root, "cgroup.controllers"), "cpu memory pids\n")
	write(t, filepath.Join(root, "memory.max"), "max\n")
	write(t, filepath.Join(root, "memory.high"), "68719476736\n")
	write(t, filepath.Join(root, "memory.current"), itoa(current))
	write(t, filepath.Join(root, "memory.stat"), "anon 16957079552\nfile 46580711424\nactive_file "+itoa(activeFile)+
		"inactive_file "+itoa(inactiveFile)+"slab_reclaimable "+itoa(slab)+"shmem 9969664\n")
	write(t, filepath.Join(root, "pids.current"), "5605\n")
	write(t, filepath.Join(root, "pids.max"), "30000\n")
	scope := filepath.Join(root, "user.slice", "user-1000.slice", "session-c22.scope")
	write(t, filepath.Join(scope, "memory.max"), "max\n")
	write(t, filepath.Join(scope, "memory.high"), "max\n")
	write(t, filepath.Join(scope, "memory.current"), "1000\n")
	write(t, filepath.Join(scope, "pids.max"), "max\n")
	write(t, filepath.Join(scope, "pids.current"), "40\n")
	return root
}

func itoa(v uint64) string {
	b := []byte{}
	for {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
		if v == 0 {
			return string(b) + "\n"
		}
	}
}

func readings(t *testing.T, p *SystemProvider) map[Source]float64 {
	t.Helper()
	p.CPUSampleInterval = 1
	rs, err := p.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[Source]float64{}
	for _, r := range rs {
		out[r.Source] = r.Value
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

// In an LXC container: memory is the cgroup's non-reclaimable use over
// its memory.high, not the host's meminfo and not the page cache; the
// task count is the container's (pids.current), not the host's
// /proc/loadavg count.
func TestSystemProviderInAContainer(t *testing.T) {
	cpus := func() int { return 32 }
	// 2026-11-02: 63.3 GiB of a 64 GiB memory.high, 43.4 GiB of it page
	// cache: ACFS's guard refused here; ntm's meminfo ratio is 0.094.
	p := &SystemProvider{Proc: host(t), Cgroup: container(t, 67940024320, 1402445824, 45168840704, 3381250088), CPUs: cpus}
	got := readings(t, p)
	if !near(got[SourceMemory], 17987487704.0/68719476736.0) || !near(got[SourceProcCount], 5605.0/8192.0) ||
		!near(got[SourceLoad], 20.78/32) {
		t.Fatalf("readings = %v", got)
	}
	s := Sample(context.Background(), testNow, p)
	if s.Overall != LevelNormal {
		t.Fatalf("overall = %s (%v), want normal", s.Overall, s.Levels)
	}
	// The same cgroup with 59 GiB of non-reclaimable use: high.
	p.Cgroup = container(t, 67940024320, 2000000000, 2000000000, 2000000000)
	if got := readings(t, p); !near(got[SourceMemory], 61940024320.0/68719476736.0) {
		t.Fatalf("memory = %v", got[SourceMemory])
	}
	if s := Sample(context.Background(), testNow, p); s.Levels[SourceMemory] != LevelHigh {
		t.Fatalf("memory level = %s", s.Levels[SourceMemory])
	}
}

// On a host (no cgroup namespace: the root has no pids.current and no
// limits) and without a cgroup v2 mount, the readings are ntm's.
func TestSystemProviderOnAHost(t *testing.T) {
	cpus := func() int { return 32 }
	root := t.TempDir()
	write(t, filepath.Join(root, "cgroup.controllers"), "cpu memory pids\n")
	for name, p := range map[string]*SystemProvider{
		"cgroup v2 root": {Proc: host(t), Cgroup: root, CPUs: cpus},
		"no cgroup v2":   {Proc: host(t), Cgroup: t.TempDir(), CPUs: cpus},
	} {
		got := readings(t, p)
		if !near(got[SourceMemory], 1-174992024.0/193077976.0) || !near(got[SourceProcCount], 7770.0/8192.0) {
			t.Errorf("%s: readings = %v", name, got)
		}
	}
	// A limited slice on a host: its pids and memory ratios count.
	slice := filepath.Join(root, "user.slice", "user-1000.slice", "session-c22.scope")
	write(t, filepath.Join(slice, "pids.max"), "8000\n")
	write(t, filepath.Join(slice, "pids.current"), "7900\n")
	write(t, filepath.Join(slice, "memory.max"), "1000000\n")
	write(t, filepath.Join(slice, "memory.current"), "990000\n")
	write(t, filepath.Join(slice, "memory.stat"), "active_file 0\ninactive_file 0\nslab_reclaimable 0\n")
	got := readings(t, &SystemProvider{Proc: host(t), Cgroup: root, CPUs: cpus})
	if !near(got[SourceProcCount], 7900.0/8000.0) || !near(got[SourceMemory], 0.99) {
		t.Fatalf("limited slice: readings = %v", got)
	}
}

// A cgroup outside the mount's namespace ("/../x") counts from the
// mount's root, never from a same-named cgroup below it.
func TestCgroupOutsideTheMount(t *testing.T) {
	root := container(t, 67940024320, 1402445824, 45168840704, 3381250088)
	write(t, filepath.Join(root, "x", "memory.max"), "1000\n")
	write(t, filepath.Join(root, "x", "memory.current"), "999\n")
	proc := host(t)
	write(t, filepath.Join(proc, "self", "cgroup"), "0::/../x\n")
	dirs := cgroupTree{root: root, self: filepath.Join(proc, "self", "cgroup")}.dirs()
	if len(dirs) != 1 || dirs[0] != root {
		t.Fatalf("dirs = %v, want the root only", dirs)
	}
	got := readings(t, &SystemProvider{Proc: proc, Cgroup: root, CPUs: func() int { return 32 }})
	if !near(got[SourceMemory], 17987487704.0/68719476736.0) {
		t.Fatalf("memory = %v", got[SourceMemory])
	}
}

// The live host: every reading is a ratio.
func TestSystemProviderLive(t *testing.T) {
	rs, err := NewSystemProvider().Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		t.Logf("%s = %.3f", r.Source, r.Value)
		if r.Source != SourceLoad && (r.Value < 0 || r.Value > 1) {
			t.Errorf("%s = %v, not a ratio", r.Source, r.Value)
		}
	}
}
