package spawn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/pressure"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/quota"
	"github.com/arosl/nhm/internal/store"
)

var pressureNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func memoryAt(v float64) pressure.Snapshot {
	return pressure.Sample(context.Background(), pressureNow, fixedReadings{{Source: pressure.SourceMemory, Value: v, Unit: "ratio"}})
}

func criticalMemory() pressure.Snapshot { return memoryAt(0.95) }

type fixedReadings []pressure.Reading

func (f fixedReadings) Name() string { return "fixed" }
func (f fixedReadings) Read(context.Context) ([]pressure.Reading, error) {
	return f, nil
}

// fleetOf is a herdr fleet: two workspaces, a shell and the given agents.
func fleetOf(agents ...string) func(context.Context) (herdr.SessionSnapshot, error) {
	return func(context.Context) (herdr.SessionSnapshot, error) {
		snap := herdr.SessionSnapshot{Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: "w1"}, {WorkspaceID: "w2"}},
			Panes: []herdr.PaneInfo{{PaneID: "w1:p0", WorkspaceID: "w1"}}}
		for _, a := range agents {
			snap.Panes = append(snap.Panes, herdr.PaneInfo{PaneID: "w2:" + a, WorkspaceID: "w2", Agent: a})
		}
		return snap, nil
	}
}

// ntm's admission (nhm-a421) on add and --robot-spawn: the pressure
// defers or refuses a large spawn, the agent caps count the herdr fleet,
// --ignore-capacity admits with a warning, and spawn runs none.
func TestNTMAdmission(t *testing.T) {
	pacing := DefaultPacing()
	pacing.MaxConcurrentSpawns = 2
	at := func(v float64) func(context.Context) pressure.Snapshot {
		return func(context.Context) pressure.Snapshot { return memoryAt(v) }
	}
	now := time.Now()
	full := func() []quota.Observation {
		obs := now.Add(-time.Minute)
		return []quota.Observation{{Pool: "claude", Kind: "claude", ObservedAt: &obs, FiveHour: &quota.Window{UsedPct: pct(95)}}}
	}
	cases := []struct {
		name     string
		env      AdmissionEnv
		agents   []Want
		mod      func(*Request)
		decision string
		reason   string
		reasons  string
		warning  string
		hint     string
		check    func(t *testing.T, ad Admission)
	}{
		{name: "headroom", env: AdmissionEnv{Pacing: pacing, Pressure: at(0.30), Fleet: fleetOf("claude", "codex")},
			decision: Admit, reason: "headroom_available",
			check: func(t *testing.T, ad Admission) {
				p := ad.Pressure
				if p.RunningAgents != 2 || p.RunningSessions != 2 || p.CurrentPanes != 3 || p.SessionPanes != 0 ||
					p.RequestedPanes != 2 || p.AdditionalPanes != 2 || p.ProjectedPanes != 5 || !p.LargeSpawn ||
					p.LargeSpawnThreshold != 2 || p.PressureLevel != "low" || len(p.Sources) != 1 || !p.AgentInventoryAvailable {
					t.Errorf("admission = %+v", p)
				}
			}},
		{name: "high pressure defers a large spawn", env: AdmissionEnv{Pacing: pacing, Pressure: at(0.85)},
			decision: Defer, reason: "pressure_high",
			reasons: "spawn admission defer: pressure_high (reduce spawn size or wait for headroom: memory)",
			hint:    "reduce spawn size or wait for headroom: memory"},
		{name: "critical pressure refuses it", env: AdmissionEnv{Pacing: pacing, Pressure: at(0.95)},
			decision: Refuse, reason: "pressure_critical"},
		{name: "a small spawn passes", env: AdmissionEnv{Pacing: pacing, Pressure: at(0.95)}, agents: []Want{{Kind: "claude", Count: 1}},
			decision: Admit, reason: "headroom_available"},
		{name: "nhm's default: 16 agents are large", env: AdmissionEnv{Pacing: DefaultPacing(), Pressure: at(0.95)},
			agents: []Want{{Kind: "claude", Count: 15}}, decision: Admit, reason: "headroom_available"},
		{name: "ignored", env: AdmissionEnv{Pacing: pacing, Pressure: at(0.95)}, mod: func(r *Request) { r.IgnoreCapacity = true },
			decision: Admit, reason: "pressure_critical",
			warning: "spawn admission refuse: pressure_critical (reduce spawn size or wait for headroom: memory) (overridden by --ignore-capacity)"},
		{name: "spawn runs none", env: AdmissionEnv{Pacing: pacing, Pressure: at(0.95)}, mod: func(r *Request) { r.NTMAdmission = false },
			decision: Admit,
			check: func(t *testing.T, ad Admission) {
				if ad.Pressure != nil {
					t.Errorf("spawn ran ntm's admission: %+v", ad.Pressure)
				}
			}},
		{name: "pacing off reads no pressure", env: AdmissionEnv{Pacing: Pacing{}, Pressure: func(context.Context) pressure.Snapshot {
			t.Error("pressure read with pacing off")
			return criticalMemory()
		}}, decision: Admit, reason: "headroom_available"},
		{name: "agent cap", env: AdmissionEnv{Pacing: Pacing{Enabled: true, MaxConcurrentSpawns: 4, AgentCaps: AgentCaps{ClaudeMaxConcurrent: 2, CodexMaxConcurrent: 1}},
			Fleet: fleetOf("claude", "codex")}, decision: Refuse, reason: "agent_limit_exceeded",
			hint: "running 2 + requested 2 exceeds cap 3; reduce requested agents or raise spawn_pacing agent caps"},
		{name: "type limit", env: AdmissionEnv{Pacing: Pacing{Enabled: true, MaxConcurrentSpawns: 4, AgentTypeLimits: map[string]int{"antigravity": 1}},
			Fleet: fleetOf("agy")}, agents: []Want{{Kind: "agy", Count: 1}}, decision: Refuse, reason: "agent_type_limit_exceeded"},
		{name: "boards are no agents; a launching agent is one", env: AdmissionEnv{Pacing: Pacing{Enabled: true, MaxConcurrentSpawns: 4,
			AgentCaps: AgentCaps{ClaudeMaxConcurrent: 3}},
			Fleet: func(context.Context) (herdr.SessionSnapshot, error) {
				return herdr.SessionSnapshot{Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: "w1"}},
					Panes:  []herdr.PaneInfo{{PaneID: "w1:p1", WorkspaceID: "w1", Agent: "claude"}, {PaneID: "w1:p2", WorkspaceID: "w1", Agent: "claude"}},
					Agents: []herdr.AgentInfo{{PaneID: "w1:p1", Agent: "claude"}, {PaneID: "w1:p3", Agent: "claude"}}}, nil
			},
			Boards: func(context.Context, herdr.SessionSnapshot) (map[string]bool, error) {
				return map[string]bool{"w1:p2": true}, nil
			}},
			agents: []Want{{Kind: "claude", Count: 1}}, decision: Admit, reason: "headroom_available",
			check: func(t *testing.T, ad Admission) {
				if ad.Pressure.RunningAgents != 2 || ad.Pressure.AgentHeadroom != 0 {
					t.Errorf("running %d, headroom %d; want 2 (p1, p3; not the board p2), 0", ad.Pressure.RunningAgents, ad.Pressure.AgentHeadroom)
				}
			}},
		{name: "fleet unknown under a cap", env: AdmissionEnv{Pacing: Pacing{Enabled: true, MaxConcurrentSpawns: 4, AgentCaps: AgentCaps{ClaudeMaxConcurrent: 9}}},
			decision: Defer, reason: "agent_inventory_unavailable"},
		{name: "quota and pressure", env: AdmissionEnv{Pacing: pacing, Pressure: at(0.85), Quota: full},
			decision: Refuse, reasons: "spawn admission defer: pressure_high", hint: AdmissionHint},
		{name: "config warning", env: AdmissionEnv{Pacing: pacing, ConfigWarnings: []string{"[providers] capacity in c is ignored"}},
			decision: Admit, warning: "[providers] capacity in c is ignored"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agents := c.agents
			if agents == nil {
				agents = []Want{{Kind: "claude", Count: 2}}
			}
			r := req(agents...)
			r.NTMAdmission = true
			if c.mod != nil {
				c.mod(&r)
			}
			ad := Decide(context.Background(), c.env, r)
			if ad.Decision != c.decision || (c.reason != "" && ad.Reason != c.reason) ||
				!strings.Contains(strings.Join(ad.Reasons, "\n"), c.reasons) || !strings.Contains(strings.Join(ad.Warnings, "\n"), c.warning) ||
				(c.hint != "" && ad.Hint != c.hint) {
				t.Fatalf("= %s %q (hint %q), reasons %q, warnings %q", ad.Decision, ad.Reason, ad.Hint, ad.Reasons, ad.Warnings)
			}
			if c.check != nil {
				c.check(t, ad)
			}
		})
	}
}

// A robot spawn that ntm's admission refuses creates nothing and answers
// in ntm's words (RESOURCE_BUSY, its hint, its admission fields);
// --ignore-capacity starts it, with the warning (nhm-1pq).
func TestRobotSpawnIgnoresCapacity(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Minute)
	w.env.Admission = AdmissionEnv{Pacing: Pacing{Enabled: true, MaxConcurrentSpawns: 1},
		Pressure: func(context.Context) pressure.Snapshot { return criticalMemory() },
		Quota: func() []quota.Observation {
			return []quota.Observation{{Pool: "claude", Kind: "claude", ObservedAt: &at, FiveHour: &quota.Window{UsedPct: pct(20)}}}
		}}
	r := req(Want{Kind: "claude", Count: 1})
	r.NoKickoff, r.RequireObservations, r.NTMAdmission, r.OperationID = true, true, true, "op-cap-1"

	res, err := Spawn(ctx, w.env, r)
	out := SpawnOutput(res, err, "json", time.Now())
	hint := "reduce spawn size or wait for headroom: memory"
	if !errors.Is(err, ErrRefused) || err.Error() != "spawn admission refuse: pressure_critical" || len(w.h.calls) != 0 ||
		out.Success || out.ErrorCode != "RESOURCE_BUSY" || out.Error != "spawn admission refuse: pressure_critical" || out.Hint != hint {
		t.Fatalf("refused robot spawn = %v, calls %v, out %+v", err, w.h.calls, out.RobotResponse)
	}
	if a := out.Admission; a.Decision != Refuse || a.Reason != "pressure_critical" || a.Hint != hint || a.PressureLevel != "critical" ||
		!a.LargeSpawn || a.RequestedAgents != 1 || a.Serialized || len(a.Sources) != 1 || a.Sources[0].Level != "critical" {
		t.Fatalf("admission = %+v", a)
	}

	r.IgnoreCapacity = true
	res, err = Spawn(ctx, w.env, r)
	out = SpawnOutput(res, err, "json", time.Now())
	if err != nil || !out.Success || out.Admission.Decision != Admit || len(out.Agents) != 1 || !out.Agents[0].Ready {
		t.Fatalf("robot spawn with --ignore-capacity = %v, out %+v", err, out)
	}
	if got := strings.Join(out.Admission.Warnings, "\n"); !strings.Contains(got, "spawn admission refuse: pressure_critical") ||
		!strings.Contains(got, "(overridden by --ignore-capacity)") {
		t.Fatalf("warnings = %q", got)
	}
}

// add's deferral is ntm's words: "agent addition defer: <reason>; <hint>";
// the fleet that herdr shows counts against the caps.
func TestAddAdmissionCountsTheFleet(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	first := req(Want{Kind: "claude", Count: 2})
	first.NoKickoff = true
	if _, err := Spawn(ctx, w.env, first); err != nil {
		t.Fatal(err)
	}
	w.env.Admission = AdmissionEnv{Pacing: Pacing{Enabled: true, MaxConcurrentSpawns: 2},
		Pressure: func(context.Context) pressure.Snapshot { return memoryAt(0.85) }}
	add := req(Want{Kind: "claude", Count: 2})
	add.Add, add.NTMAdmission, add.NoKickoff = true, true, true
	res, err := Spawn(ctx, w.env, add)
	if !errors.Is(err, ErrRefused) || err.Error() != "agent addition defer: pressure_high; reduce spawn size or wait for headroom: memory" {
		t.Fatalf("add = %v", err)
	}
	if p := res.Admission.Pressure; p.RunningAgents != 2 || p.SessionPanes != 2 || p.RequestedPanes != 4 {
		t.Fatalf("admission = %+v", p)
	}
	w.env.Admission = AdmissionEnv{Pacing: Pacing{Enabled: true, MaxConcurrentSpawns: 4, AgentTypeLimits: map[string]int{"claude": 3}}}
	if _, err := Spawn(ctx, w.env, add); err == nil || !strings.Contains(err.Error(), "agent addition refuse: agent_type_limit_exceeded; claude: running 2 + requested 2") {
		t.Fatalf("add over the type limit = %v", err)
	}
}

// Under an agent cap a spawn counts and starts under the instance's
// spawn admission lock: a lock that another spawn holds defers it (ntm's
// spawn_admission_busy), and the lock is free once the agents started.
func TestSpawnAdmissionFence(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.env.Admission = AdmissionEnv{Pacing: Pacing{Enabled: true, MaxConcurrentSpawns: 4, AgentCaps: AgentCaps{ClaudeMaxConcurrent: 5}}}
	r := req(Want{Kind: "claude", Count: 1})
	r.NoKickoff, r.NTMAdmission = true, true
	hold, err := store.LockSpawnAdmission(ctx, w.env.InstanceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func(d time.Duration) { store.SpawnAdmissionWait = d }(store.SpawnAdmissionWait)
	store.SpawnAdmissionWait = 50 * time.Millisecond
	res, err := Spawn(ctx, w.env, r)
	busy := "spawn admission unavailable: another spawn owns fleet admission"
	if !errors.Is(err, ErrRefused) || err.Error() != busy || len(w.h.calls) != 0 ||
		res.Admission.Decision != Defer || res.Admission.Reason != "spawn_admission_busy" ||
		res.Admission.Hint != "Another local spawn is still launching; retry after it finishes" {
		t.Fatalf("busy = %v (%+v), calls %v", err, res.Admission, w.h.calls)
	}
	// ntm's words: the robot error names the lock, add gives its error.
	if out := SpawnOutput(res, err, "json", time.Now()); out.ErrorCode != "RESOURCE_BUSY" || out.Error != busy ||
		out.Hint != res.Admission.Hint || out.Admission.Reason != "spawn_admission_busy" {
		t.Fatalf("robot busy = %+v", out.RobotResponse)
	}
	hold()
	res, err = Spawn(ctx, w.env, r)
	if err != nil || !res.Admission.Pressure.Serialized || res.Admission.Pressure.MaxAgents != 5 {
		t.Fatalf("fenced spawn = %v, %+v", err, res.Admission.Pressure)
	}
	again, err := store.LockSpawnAdmission(ctx, w.env.InstanceDir)
	if err != nil {
		t.Fatalf("the lock outlived the spawn: %v", err)
	}
	again()
	// A dry run and --ignore-capacity take no lock; an add that finds it
	// busy gives the lock's error, as ntm's add does.
	hold, _ = store.LockSpawnAdmission(ctx, w.env.InstanceDir)
	defer hold()
	add := r
	add.Add = true
	if _, err := Spawn(ctx, w.env, add); !errors.Is(err, ErrRefused) || err.Error() != "another spawn owns fleet admission" {
		t.Fatalf("add busy = %v", err)
	}
	for _, mod := range []func(*Request){func(r *Request) { r.DryRun = true }, func(r *Request) { r.IgnoreCapacity = true }} {
		rr := r
		mod(&rr)
		if res, err := Spawn(ctx, w.env, rr); err != nil || (res.Admission.Pressure != nil && res.Admission.Pressure.Serialized) {
			t.Fatalf("unfenced spawn = %v, %+v", err, res.Admission)
		}
	}
}

// An error before admission carries no admission hint; a cancelled
// spawn is ntm's TIMEOUT.
func TestRobotSpawnEarlyErrors(t *testing.T) {
	res := Result{Session: "s", Targets: []Target{}}
	out := SpawnOutput(res, errors.New("herdr: no socket"), "json", time.Now())
	if out.ErrorCode != "INTERNAL_ERROR" || out.Admission.Hint != "" || out.Hint != "" {
		t.Fatalf("early error = %+v, admission %+v", out.RobotResponse, out.Admission)
	}
	out = SpawnOutput(res, context.Canceled, "json", time.Now())
	if out.ErrorCode != "TIMEOUT" || out.Hint != "Retry the command after cancellation" {
		t.Fatalf("cancelled = %+v", out.RobotResponse)
	}
}

// ntm's [spawn_pacing] checks, with nhm's pi.
func TestValidatePacing(t *testing.T) {
	for _, c := range []struct {
		p    Pacing
		want string
	}{
		{DefaultPacing(), ""},
		{Pacing{}, ""},
		{Pacing{Enabled: true}, "max_concurrent_spawns must be at least 1, got 0"},
		{Pacing{Enabled: true, MaxConcurrentSpawns: 1, AgentCaps: AgentCaps{OmpMaxConcurrent: -1}}, "agent_caps: omp_max_concurrent must be non-negative, got -1"},
		{Pacing{Enabled: true, MaxConcurrentSpawns: 1, AgentTypeLimits: map[string]int{"cursor": 1}}, `agent_type_limits: unknown agent type "cursor"`},
		{Pacing{Enabled: true, MaxConcurrentSpawns: 1, AgentTypeLimits: map[string]int{"pi": -2}}, "agent_type_limits: pi must be non-negative, got -2"},
		{Pacing{Enabled: true, MaxConcurrentSpawns: 1, AgentTypeLimits: map[string]int{"pi": 2, "opencode": 1}}, ""},
	} {
		err := ValidatePacing(c.p)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v = %v, want %q", c.p, err, c.want)
		}
	}
	if d := DefaultPacing(); !d.Enabled || d.MaxConcurrentSpawns != 16 || d.agentLimit() != 0 || d.needsFence(false) {
		t.Errorf("default = %+v", d)
	}
}
