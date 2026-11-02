package pressure

// nhm's own (nhm-a421): ntm reads /proc only. In an LXC container
// /proc/meminfo shows the host's memory (lxcfs shows the container's only
// under a memory.max) and /proc/loadavg counts the host's tasks, so nhm
// also reads the cgroup v2 tree: its own cgroup and each parent up to the
// mount's root.

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cgroupTree is a cgroup v2 mount and the file that names nhm's own
// cgroup in it (/proc/self/cgroup).
type cgroupTree struct {
	root string
	self string
}

// dirs is nhm's cgroup and each parent up to the mount's root, leaf
// first; nil without a cgroup v2 mount.
func (t cgroupTree) dirs() []string {
	if _, err := os.Stat(filepath.Join(t.root, "cgroup.controllers")); err != nil {
		return nil
	}
	rel := "/"
	if raw, err := os.ReadFile(t.self); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if p, ok := strings.CutPrefix(line, "0::"); ok {
				rel = p
				break
			}
		}
	}
	// A path that leaves the mount (a cgroup outside this namespace
	// reads as "/../x") counts from the mount's root.
	if strings.HasPrefix(rel, "/..") {
		rel = "/"
	}
	rel = filepath.Clean("/" + rel)
	var out []string
	for dir := filepath.Join(t.root, rel); ; dir = filepath.Dir(dir) {
		out = append(out, dir)
		if dir == t.root || len(dir) <= len(t.root) {
			return out
		}
	}
}

// memoryRatio is the largest ratio, over the cgroups with a memory
// limit, of the cgroup's non-reclaimable use to its limit. The limit is
// the lower of memory.max and memory.high (the kernel throttles at
// memory.high). The use is memory.current less the page cache
// (active_file, inactive_file) and the reclaimable slab, as
// MemAvailable counts them free: page cache is not pressure.
func (t cgroupTree) memoryRatio() (float64, bool) {
	ratio, have := 0.0, false
	for _, dir := range t.dirs() {
		hard, hardOK := readCgroupLimit(dir, "memory.max")
		high, highOK := readCgroupLimit(dir, "memory.high")
		limit, ok := lowerLimit(hard, hardOK, high, highOK)
		if !ok || limit == 0 {
			continue
		}
		current, ok := readCgroupUint(dir, "memory.current")
		if !ok {
			continue
		}
		stat := readCgroupStat(dir, "memory.stat")
		reclaimable := stat["active_file"] + stat["inactive_file"] + stat["slab_reclaimable"]
		used := 0.0
		if current > reclaimable {
			used = float64(current - reclaimable)
		}
		if v := clamp01(used / float64(limit)); !have || v > ratio {
			ratio, have = v, true
		}
	}
	return ratio, have
}

// pidsRatio is the largest pids.current/pids.max over the cgroups with
// a task limit.
func (t cgroupTree) pidsRatio() (float64, bool) {
	ratio, have := 0.0, false
	for _, dir := range t.dirs() {
		limit, ok := readCgroupLimit(dir, "pids.max")
		if !ok || limit == 0 {
			continue
		}
		current, ok := readCgroupUint(dir, "pids.current")
		if !ok {
			continue
		}
		if v := clamp01(float64(current) / float64(limit)); !have || v > ratio {
			ratio, have = v, true
		}
	}
	return ratio, have
}

// namespacePids is the task count of the mount's root when that root is
// a container's cgroup (a cgroup namespace): the host's root cgroup has
// no pids.current.
func (t cgroupTree) namespacePids() (int, bool) {
	if t.dirs() == nil {
		return 0, false
	}
	v, ok := readCgroupUint(t.root, "pids.current")
	if !ok || v > uint64(int(^uint(0)>>1)) {
		return 0, false
	}
	return int(v), true
}

// readCgroupLimit reads a limit file: a number, or "max" (no limit: not
// ok).
func readCgroupLimit(dir, name string) (uint64, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(raw))
	if s == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

func lowerLimit(a uint64, aok bool, b uint64, bok bool) (uint64, bool) {
	switch {
	case aok && bok:
		return min(a, b), true
	case aok:
		return a, true
	case bok:
		return b, true
	}
	return 0, false
}

func readCgroupUint(dir, name string) (uint64, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return v, err == nil
}

// readCgroupStat reads a flat keyed file ("key value" lines).
func readCgroupStat(dir, name string) map[string]uint64 {
	out := map[string]uint64{}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if v, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
			out[fields[0]] = v
		}
	}
	return out
}
