// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/pressure/provider.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes (nhm-a421): this header; the
// Provider interface lives in pressure.go; the /proc and cgroup roots and
// the CPU count are fields, so tests read a tree of their own; the memory
// reading is the larger of ntm's /proc/meminfo ratio and each limited
// cgroup's non-reclaimable use over its limit (cgroup.go); the process
// count is the container's own (its cgroup's pids.current) when nhm runs
// in a cgroup namespace, where /proc/loadavg counts the host's tasks, and
// the reading is the larger of ntm's ratio and each limited cgroup's
// pids.current over pids.max.
package pressure

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// SystemProvider reads cheap host pressure signals from /proc and the
// cgroup tree when available. Missing files simply produce no readings,
// so callers keep their deterministic low-pressure default.
type SystemProvider struct {
	CPUSampleInterval time.Duration
	// Proc is the procfs root ("/proc" when empty).
	Proc string
	// Cgroup is the cgroup v2 mount ("/sys/fs/cgroup" when empty).
	Cgroup string
	// CPUs is the CPU count (runtime.NumCPU when nil).
	CPUs func() int
}

const (
	processCountPerCPU   = 256
	minProcessCountLimit = 4096
)

// NewSystemProvider creates the default host-pressure provider.
func NewSystemProvider() *SystemProvider {
	return &SystemProvider{CPUSampleInterval: 50 * time.Millisecond}
}

// Name returns the provider name.
func (p *SystemProvider) Name() string { return "system" }

func (p *SystemProvider) proc(name string) string {
	root := p.Proc
	if root == "" {
		root = "/proc"
	}
	return filepath.Join(root, name)
}

func (p *SystemProvider) cpus() int {
	if p.CPUs != nil {
		return p.CPUs()
	}
	return runtime.NumCPU()
}

func (p *SystemProvider) cgroups() cgroupTree {
	root := p.Cgroup
	if root == "" {
		root = "/sys/fs/cgroup"
	}
	return cgroupTree{root: root, self: p.proc("self/cgroup")}
}

// Read returns current CPU, load, memory and process-count readings when
// the host exposes the needed counters.
func (p *SystemProvider) Read(ctx context.Context) ([]Reading, error) {
	var readings []Reading
	if v, ok := p.readLoadRatio(); ok {
		readings = append(readings, Reading{Source: SourceLoad, Value: v, Unit: "ratio"})
	}
	if v, ok := p.readMemoryRatio(); ok {
		readings = append(readings, Reading{Source: SourceMemory, Value: v, Unit: "ratio"})
	}
	if v, ok := p.readProcessCountRatio(); ok {
		readings = append(readings, Reading{Source: SourceProcCount, Value: v, Unit: "ratio"})
	}
	if v, ok := p.sampleCPURatio(ctx, p.CPUSampleInterval); ok {
		readings = append(readings, Reading{Source: SourceCPU, Value: v, Unit: "ratio"})
	}
	return readings, nil
}

func (p *SystemProvider) readLoadRatio() (float64, bool) {
	raw, err := os.ReadFile(p.proc("loadavg"))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	cpus := p.cpus()
	if cpus <= 0 {
		return 0, false
	}
	return load1 / float64(cpus), true
}

// readProcessCountRatio is ntm's ratio of tasks to the CPU-based limit,
// with the container's own task count in a cgroup namespace, and the
// larger of it and each limited cgroup's pids ratio.
func (p *SystemProvider) readProcessCountRatio() (float64, bool) {
	tree := p.cgroups()
	total, ok := tree.namespacePids()
	if !ok {
		raw, err := os.ReadFile(p.proc("loadavg"))
		if err == nil {
			total, ok = parseProcessTotalFromLoadavg(string(raw))
		}
	}
	ratio, have := 0.0, false
	if ok {
		ratio, have = processCountRatio(total, p.defaultProcessCountLimit())
	}
	if v, limited := tree.pidsRatio(); limited && (!have || v > ratio) {
		ratio, have = v, true
	}
	return ratio, have
}

func parseProcessTotalFromLoadavg(raw string) (int, bool) {
	fields := strings.Fields(raw)
	if len(fields) < 4 {
		return 0, false
	}
	parts := strings.Split(fields[3], "/")
	if len(parts) != 2 {
		return 0, false
	}
	total, err := strconv.Atoi(parts[1])
	if err != nil || total < 0 {
		return 0, false
	}
	return total, true
}

func processCountRatio(total, limit int) (float64, bool) {
	if total < 0 || limit <= 0 {
		return 0, false
	}
	ratio := float64(total) / float64(limit)
	if ratio > 1 {
		ratio = 1
	}
	return ratio, true
}

func (p *SystemProvider) defaultProcessCountLimit() int {
	cpus := p.cpus()
	if cpus < 1 {
		cpus = 1
	}
	limit := cpus * processCountPerCPU
	if limit < minProcessCountLimit {
		return minProcessCountLimit
	}
	return limit
}

// readMemoryRatio is the larger of ntm's /proc/meminfo ratio and each
// limited cgroup's ratio: in an LXC container /proc/meminfo shows the
// host, not the container's limit.
func (p *SystemProvider) readMemoryRatio() (float64, bool) {
	ratio, have := 0.0, false
	if raw, err := os.ReadFile(p.proc("meminfo")); err == nil {
		ratio, have = parseMemoryRatio(string(raw))
	}
	if v, limited := p.cgroups().memoryRatio(); limited && (!have || v > ratio) {
		ratio, have = v, true
	}
	return ratio, have
}

// parseMemoryRatio is the pure /proc/meminfo parser.
//
// Returns (used, true) only when BOTH MemTotal and MemAvailable were
// observed and total>0. A missing MemAvailable line (old kernels,
// minimal containers) returns (0, false), not used=1.0.
func parseMemoryRatio(raw string) (float64, bool) {
	var totalKB, availableKB float64
	var haveAvailable bool
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			totalKB = value
		case "MemAvailable":
			availableKB = value
			haveAvailable = true
		}
	}
	if totalKB <= 0 || !haveAvailable {
		return 0, false
	}
	return clamp01(1 - (availableKB / totalKB)), true
}

func (p *SystemProvider) sampleCPURatio(ctx context.Context, interval time.Duration) (float64, bool) {
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	totalA, idleA, ok := p.readCPUStat()
	if !ok {
		return 0, false
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, false
	case <-timer.C:
	}
	totalB, idleB, ok := p.readCPUStat()
	if !ok {
		return 0, false
	}
	if totalB <= totalA || idleB < idleA {
		return 0, false
	}
	totalDelta := totalB - totalA
	idleDelta := idleB - idleA
	return clamp01(1 - (float64(idleDelta) / float64(totalDelta))), true
}

func (p *SystemProvider) readCPUStat() (uint64, uint64, bool) {
	raw, err := os.ReadFile(p.proc("stat"))
	if err != nil {
		return 0, 0, false
	}
	lines := strings.SplitN(string(raw), "\n", 2)
	fields := strings.Fields(lines[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var total uint64
	var idle uint64
	for i, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		total += value
		if i == 3 || i == 4 {
			idle += value
		}
	}
	return total, idle, true
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
