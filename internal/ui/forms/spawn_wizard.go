// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/tui/dashboard/panels/spawn_wizard.go at commit 9d5ed977; see
// LICENSE in this directory and PROVENANCE.md.
// Local changes:
//   - mechanical: package forms; nhm's theme import path; ntm's
//     panels.Keybinding (panel.go) copied in as SpawnKeybinding.
//   - behaviour: the kinds and counts come from nhm's kind registry, one
//     count (and a model, where the kind takes one) per kind, 0 to 32,
//     instead of ntm's fixed cc/cod/gmi/agy; the quick and minimal
//     methods keep ntm's mix, for the kinds the registry has.
//   - behaviour: new steps after the counts: options (the session of a
//     new one, tab label, group, kickoff, memory: the gum wizard's
//     questions), then the review (the action's dry run: admission and
//     the plan), the confirm, and the result. The wizard runs its action
//     (spawn.add or spawn.new) through the action table and its guard;
//     ntm's dashboard ran `ntm add` itself. Going back drops the review;
//     the spawn runs admission again; a refused review has no confirm.
//   - behaviour: esc while the spawn runs only closes the wizard: the
//     spawn runs on as a job (plan-v4 section 4).
//   - view: five steps, with " → " between them so they fit the modal.
//   - behaviour: a method's preset fills the counts once; going back to
//     the counts keeps the operator's edits (ntm refilled them).
// See evidence spawn_wizard-closure.md (nhm-qbi.4).

package forms

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// SpawnWizardStep tracks the current step in the multi-step wizard.
type SpawnWizardStep int

const (
	SpawnStepMethod SpawnWizardStep = iota
	SpawnStepCounts
	SpawnStepOptions
	SpawnStepReview
	SpawnStepResult
)

// SpawnWizardResult holds the configuration produced by the wizard.
type SpawnWizardResult struct {
	// Request is the spawn the review showed.
	Request actions.SpawnRequest
	// Outcome is the core's answer once the spawn ran.
	Outcome *actions.Result
	Err     error
}

// SpawnWizardDoneMsg is sent when the wizard completes or is cancelled.
type SpawnWizardDoneMsg struct {
	Result    SpawnWizardResult
	Cancelled bool
}

// SpawnKeybinding is one key the wizard shows in its help (ntm's
// panels.Keybinding).
type SpawnKeybinding struct {
	Key         key.Binding // The key binding
	Description string      // Human-readable description
	Action      string      // Action identifier for dispatch
}

// Kickoff and memory choices (the gum wizard's).
const (
	kickoffDefault = "default"
	kickoffOwn     = "own"
	kickoffNone    = "none"
	memoryCM       = "cm"
	memoryBoth     = "both"
	memoryNone     = "none"
)

// SpawnWizard is an in-surface modal for configuring agent spawning.
// It uses huh forms with multi-step navigation: method -> counts ->
// options -> review -> result.
type SpawnWizard struct {
	width       int
	height      int
	theme       theme.Theme
	step        SpawnWizardStep
	sessionName string

	run   Runner
	ctx   ui.Context
	add   bool
	kinds []kinds.Adapter

	// Step 1: Method selection
	methodForm   *huh.Form
	configMethod string

	// Step 2: Agent counts, one per kind; a model where the kind takes one
	countsForm *huh.Form
	countStrs  []string
	modelStrs  []string
	// filled is the method the counts were last filled for: going back
	// to the counts keeps the operator's edits.
	filled string

	// Step 3: Options
	optionsForm *huh.Form
	session     string
	label       string
	group       string
	kickoff     string
	prompt      string
	memory      string

	// Step 4: Review and confirmation
	request     uint64
	reviewing   bool
	review      *actions.Result
	reviewErr   error
	reviewKey   string
	confirmForm *huh.Form
	confirmed   bool

	// Step 5: the spawn's answer
	running bool
	outcome *actions.Result
	runErr  error

	// Error state
	err error
}

// NewSpawnWizard creates a new spawn wizard for run's action in context
// c: spawn.add adds to c's workspace, spawn.new asks for a new session.
// ks are the kinds it offers (kinds.All()).
func NewSpawnWizard(run Runner, c ui.Context, ks []kinds.Adapter, width, height int) *SpawnWizard {
	sw := &SpawnWizard{
		width:       width,
		height:      height,
		theme:       theme.Current(),
		step:        SpawnStepMethod,
		sessionName: firstOf(c.WorkspaceLabel, c.WorkspaceID),
		run:         run,
		ctx:         c,
		add:         run.Action.ID == "spawn.add",
		kinds:       ks,
		countStrs:   make([]string, len(ks)),
		modelStrs:   make([]string, len(ks)),
		kickoff:     kickoffDefault,
		memory:      memoryCM,
	}
	if !sw.add {
		sw.sessionName = "a new session"
	}
	sw.initMethodForm()
	return sw
}

// SetSize updates the wizard dimensions.
func (sw *SpawnWizard) SetSize(width, height int) {
	sw.width = width
	sw.height = height
}

// Size returns the wizard's current width and height.
func (sw *SpawnWizard) Size() (width, height int) {
	return sw.width, sw.height
}

// Step is the wizard's step.
func (sw *SpawnWizard) Step() SpawnWizardStep { return sw.step }

// Review is the dry run's answer, once it is in.
func (sw *SpawnWizard) Review() (*actions.Result, error) { return sw.review, sw.reviewErr }

// Outcome is the spawn's answer, once it is in.
func (sw *SpawnWizard) Outcome() (*actions.Result, error) { return sw.outcome, sw.runErr }

// Init implements tea.Model.
func (sw *SpawnWizard) Init() tea.Cmd {
	return sw.methodForm.Init()
}

type spawnReviewed struct {
	request uint64
	res     actions.Result
	err     error
}

type spawnRan struct {
	request uint64
	res     actions.Result
	err     error
}

// Update implements tea.Model.
func (sw *SpawnWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			// Cancel the wizard; a spawn that runs runs on.
			return sw, func() tea.Msg {
				return SpawnWizardDoneMsg{Cancelled: sw.outcome == nil && !sw.running, Result: sw.result()}
			}
		case "shift+tab":
			// Go back one step; the review is dropped.
			if sw.step > SpawnStepMethod && sw.step < SpawnStepResult && !sw.running {
				sw.step--
				sw.dropReview()
				sw.initCurrentForm()
				return sw, sw.currentFormInit()
			}
		}
	case spawnReviewed:
		if msg.request != sw.request || sw.step != SpawnStepReview {
			return sw, nil
		}
		sw.reviewing, sw.review, sw.reviewErr = false, &msg.res, msg.err
		if msg.err == nil && msg.res.Status == "admitted" {
			sw.initConfirmForm()
			return sw, sw.confirmForm.Init()
		}
		return sw, nil
	case spawnRan:
		if msg.request != sw.request || !sw.running {
			return sw, nil
		}
		sw.running, sw.outcome, sw.runErr = false, &msg.res, msg.err
		return sw, nil
	}

	// Update current form
	var cmd tea.Cmd
	switch sw.step {
	case SpawnStepMethod:
		form, c := sw.methodForm.Update(msg)
		if f, ok := form.(*huh.Form); ok {
			sw.methodForm = f
		}
		cmd = c
		if sw.methodForm.State == huh.StateCompleted {
			sw.step = SpawnStepCounts
			sw.initCountsForm()
			return sw, sw.countsForm.Init()
		}
	case SpawnStepCounts:
		form, c := sw.countsForm.Update(msg)
		if f, ok := form.(*huh.Form); ok {
			sw.countsForm = f
		}
		cmd = c
		if sw.countsForm.State == huh.StateCompleted {
			// Validate counts
			if sw.total() == 0 {
				sw.err = fmt.Errorf("at least one agent is required")
				sw.initCountsForm() // Reset form
				return sw, sw.countsForm.Init()
			}
			sw.err = nil
			sw.step = SpawnStepOptions
			sw.initOptionsForm()
			return sw, sw.optionsForm.Init()
		}
	case SpawnStepOptions:
		form, c := sw.optionsForm.Update(msg)
		if f, ok := form.(*huh.Form); ok {
			sw.optionsForm = f
		}
		cmd = c
		if sw.optionsForm.State == huh.StateCompleted {
			if !sw.add {
				if err := actions.ValidLabel(sw.session); err != nil {
					sw.err = err
					sw.initOptionsForm()
					return sw, sw.optionsForm.Init()
				}
			}
			if sw.kickoff == kickoffOwn && strings.TrimSpace(sw.prompt) == "" {
				sw.err = fmt.Errorf("an empty kickoff prompt: write one, or pick another kickoff")
				sw.initOptionsForm()
				return sw, sw.optionsForm.Init()
			}
			sw.err = nil
			sw.step = SpawnStepReview
			return sw, sw.startReview()
		}
	case SpawnStepReview:
		if sw.confirmForm == nil {
			return sw, nil
		}
		form, c := sw.confirmForm.Update(msg)
		if f, ok := form.(*huh.Form); ok {
			sw.confirmForm = f
		}
		cmd = c
		if sw.confirmForm.State == huh.StateCompleted {
			if !sw.confirmed {
				return sw, func() tea.Msg { return SpawnWizardDoneMsg{Cancelled: true} }
			}
			return sw, sw.submit()
		}
	}

	return sw, cmd
}

// SpawnRequest is the request the wizard's fields make.
func (sw *SpawnWizard) SpawnRequest() actions.SpawnRequest {
	r := actions.SpawnRequest{Add: sw.add, Label: strings.TrimSpace(sw.label), Group: strings.TrimSpace(sw.group)}
	if sw.add {
		r.Session = sw.ctx.WorkspaceID
	} else {
		r.Session = strings.TrimSpace(sw.session)
	}
	for i, k := range sw.kinds {
		if n := parseCount(sw.countStrs[i]); n > 0 {
			r.Agents = append(r.Agents, spawn.Want{Kind: k.Name, Count: n, Model: strings.TrimSpace(sw.modelStrs[i])})
		}
	}
	switch sw.kickoff {
	case kickoffNone:
		r.NoKickoff = true
	case kickoffOwn:
		r.Prompt = sw.prompt
	}
	if !r.NoKickoff {
		r.Memory = sw.memory == memoryCM || sw.memory == memoryBoth
		r.CASS = sw.memory == memoryBoth
	}
	return r
}

func (sw *SpawnWizard) actionRequest() (actions.Request, error) {
	param, err := actions.SpawnParam(sw.SpawnRequest())
	if err != nil {
		return actions.Request{}, err
	}
	return actions.Request{Context: sw.ctx, Params: map[string]string{actions.ParamSpawn: param}}, nil
}

// startReview runs the action's dry run: admission and the plan, under
// the review's operation id.
func (sw *SpawnWizard) startReview() tea.Cmd {
	sw.dropReview()
	req, err := sw.actionRequest()
	var opID string
	if err == nil {
		sw.reviewKey, opID, err = sw.run.review(req)
	}
	if err != nil {
		sw.reviewErr = err
		return nil
	}
	req.OperationID = opID
	sw.reviewing, sw.request = true, nextRequest()
	rid := sw.request
	cmd := sw.run.do(rid, withParam(req, actions.ParamDryRun, "1"))
	return func() tea.Msg {
		m := cmd().(done)
		return spawnReviewed{request: m.request, res: m.res, err: m.err}
	}
}

// submit starts the reviewed spawn, once.
func (sw *SpawnWizard) submit() tea.Cmd {
	if sw.running || sw.outcome != nil {
		return nil
	}
	opID, err := sw.run.Guard.Submit(sw.reviewKey)
	if err != nil {
		sw.runErr = err
		sw.step = SpawnStepResult
		return nil
	}
	req, err := sw.actionRequest()
	if err != nil {
		sw.runErr = err
		sw.step = SpawnStepResult
		return nil
	}
	req.OperationID = opID
	sw.step, sw.running, sw.request = SpawnStepResult, true, nextRequest()
	cmd := sw.run.do(sw.request, req)
	return func() tea.Msg {
		m := cmd().(done)
		return spawnRan{request: m.request, res: m.res, err: m.err}
	}
}

func (sw *SpawnWizard) dropReview() {
	sw.request, sw.reviewing, sw.review, sw.reviewErr, sw.reviewKey = 0, false, nil, nil, ""
	sw.confirmForm, sw.confirmed = nil, false
}

func (sw *SpawnWizard) result() SpawnWizardResult {
	return SpawnWizardResult{Request: sw.SpawnRequest(), Outcome: sw.outcome, Err: sw.runErr}
}

func (sw *SpawnWizard) total() int {
	n := 0
	for _, s := range sw.countStrs {
		n += parseCount(s)
	}
	return n
}

// View renders the wizard overlay.
func (sw *SpawnWizard) View() string {
	t := sw.theme

	// Calculate modal dimensions (clamped to fit within parent)
	modalWidth := min(70, max(40, sw.width-20))
	if sw.width > 0 && modalWidth > sw.width-2 {
		modalWidth = max(10, sw.width-2)
	}

	modalHeight := min(20, max(12, sw.height-10))
	if sw.height > 0 && modalHeight > sw.height-2 {
		modalHeight = max(6, sw.height-2)
	}

	// Build content
	var content strings.Builder

	// Title
	titleStyle := lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true)
	content.WriteString(titleStyle.Render("Spawn Wizard") + "\n")
	content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render(
		fmt.Sprintf("Session: %s", clean(sw.sessionName))) + "\n\n")

	// Step indicator
	stepStyle := lipgloss.NewStyle().Foreground(t.Overlay)
	steps := []string{"Method", "Counts", "Options", "Review", "Result"}
	var stepIndicators []string
	for i, s := range steps {
		if SpawnWizardStep(i) == sw.step {
			stepIndicators = append(stepIndicators, lipgloss.NewStyle().
				Foreground(t.Primary).Bold(true).Render("● "+s))
		} else if SpawnWizardStep(i) < sw.step {
			stepIndicators = append(stepIndicators, lipgloss.NewStyle().
				Foreground(t.Success).Render("✓ "+s))
		} else {
			stepIndicators = append(stepIndicators, stepStyle.Render("○ "+s))
		}
	}
	content.WriteString(strings.Join(stepIndicators, " → ") + "\n\n")

	// Error message if any
	if sw.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(t.Error).
			Bold(true)
		content.WriteString(errorStyle.Render("⚠ "+clean(sw.err.Error())) + "\n\n")
	}

	// Current form
	switch sw.step {
	case SpawnStepMethod:
		content.WriteString(sw.methodForm.View())
	case SpawnStepCounts:
		content.WriteString(sw.countsForm.View())
	case SpawnStepOptions:
		content.WriteString(sw.optionsForm.View())
	case SpawnStepReview:
		content.WriteString(sw.reviewText())
		if sw.confirmForm != nil {
			content.WriteString("\n" + sw.confirmForm.View())
		}
	case SpawnStepResult:
		content.WriteString(sw.resultText())
	}

	// Hint
	content.WriteString("\n")
	hintStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
	switch {
	case sw.step == SpawnStepResult && sw.running:
		content.WriteString(hintStyle.Render("Esc: close; the spawn runs on (recent operations)"))
	case sw.step == SpawnStepResult:
		content.WriteString(hintStyle.Render("Esc: close"))
	case sw.step > SpawnStepMethod:
		content.WriteString(hintStyle.Render("Shift+Tab: previous step • Esc: cancel"))
	default:
		content.WriteString(hintStyle.Render("Esc: cancel"))
	}

	// Modal box
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Background(t.Base).
		Padding(1, 2).
		Width(modalWidth).
		Height(modalHeight)

	return boxStyle.Render(content.String())
}

// reviewText is the dry run's answer: the plan and admission, in the
// core's words.
func (sw *SpawnWizard) reviewText() string {
	if sw.reviewing {
		return "Checking admission (a dry run; nothing starts)…\n"
	}
	var b strings.Builder
	if sw.review != nil {
		writeResult(&b, *sw.review)
	}
	if sw.reviewErr != nil {
		fmt.Fprintf(&b, "refused: %s\n", clean(sw.reviewErr.Error()))
	}
	if sw.confirmForm == nil {
		b.WriteString("\nNothing can start: Shift+Tab to change the request, Esc to cancel.\n")
	}
	return b.String()
}

func (sw *SpawnWizard) resultText() string {
	if sw.running {
		return "Starting the agents (admission runs again)…\n"
	}
	var b strings.Builder
	if sw.outcome != nil {
		writeResult(&b, *sw.outcome)
	}
	if sw.runErr != nil {
		fmt.Fprintf(&b, "error: %s\n", clean(sw.runErr.Error()))
	}
	return b.String()
}

// ResultText is an action's answer in the core's words: its status, its
// lines, each target's status and reason, its warnings; terminal controls
// removed.
func ResultText(r actions.Result) string {
	var b strings.Builder
	writeResult(&b, r)
	return b.String()
}

// writeResult writes an action's answer in the core's words.
func writeResult(b *strings.Builder, r actions.Result) {
	if r.Status != "" {
		fmt.Fprintf(b, "%s\n", clean(r.Status))
	}
	for _, l := range r.Lines {
		fmt.Fprintf(b, "%s\n", clean(l))
	}
	for _, t := range r.Targets {
		line := "  " + clean(t.Name) + ": " + clean(t.Status)
		if t.Reason != "" {
			line += ": " + clean(t.Reason)
		}
		b.WriteString(line + "\n")
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(b, "warning: %s\n", clean(w))
	}
}

// Keybindings returns the wizard keybindings for help display.
func (sw *SpawnWizard) Keybindings() []SpawnKeybinding {
	return []SpawnKeybinding{
		{
			Key:         key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select/confirm")),
			Description: "Select option or confirm",
			Action:      "select",
		},
		{
			Key:         key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back")),
			Description: "Go to previous step",
			Action:      "back",
		},
		{
			Key:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
			Description: "Cancel wizard",
			Action:      "cancel",
		},
	}
}

// presets are ntm's quick and minimal mixes, by kind name.
var presets = map[string]map[string]int{
	"quick":   {"claude": 2, "codex": 1, "agy": 1},
	"minimal": {"claude": 1},
}

// presetLabel names a preset's mix for the kinds the registry has; ""
// when it has none of them.
func (sw *SpawnWizard) presetLabel(name string) string {
	var parts []string
	for _, k := range sw.kinds {
		if n := presets[name][k.Name]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k.Name))
		}
	}
	return strings.Join(parts, ", ")
}

func (sw *SpawnWizard) initMethodForm() {
	opts := []huh.Option[string]{huh.NewOption("Manual — pick agent types and counts", "manual")}
	if mix := sw.presetLabel("quick"); mix != "" {
		opts = append(opts, huh.NewOption("Quick — balanced mix ("+mix+")", "quick"))
	}
	if mix := sw.presetLabel("minimal"); mix != "" {
		opts = append(opts, huh.NewOption("Minimal — "+mix, "minimal"))
	}
	sw.methodForm = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Configuration method").
				Description("How would you like to configure agents?").
				Options(opts...).
				Value(&sw.configMethod),
		),
	).WithTheme(theme.HuhTheme()).WithWidth(sw.modalContentWidth())
}

func (sw *SpawnWizard) initCountsForm() {
	// Pre-fill based on method, once per method
	switch sw.configMethod {
	case "quick", "minimal":
		if sw.filled != sw.configMethod {
			for i, k := range sw.kinds {
				sw.countStrs[i] = strconv.Itoa(presets[sw.configMethod][k.Name])
			}
		}
	default:
		// Manual - keep existing values or defaults
		for i := range sw.kinds {
			if sw.countStrs[i] == "" {
				sw.countStrs[i] = "0"
			}
		}
	}

	var fields []huh.Field
	for i, k := range sw.kinds {
		title := k.Name + " agents"
		if k.NTMType != "" && k.NTMType != k.Name {
			title += " (" + k.NTMType + ")"
		}
		fields = append(fields, huh.NewInput().
			Title(title).
			Description("Number of "+k.Name+" agents, 0 to 32").
			Placeholder("0").
			Value(&sw.countStrs[i]).
			Validate(validateAgentCount))
		if k.ModelFlag != "" {
			fields = append(fields, huh.NewInput().
				Title(k.Name+" model").
				Description("Empty: the CLI's default").
				Value(&sw.modelStrs[i]).
				Validate(validateModel))
		}
	}
	sw.filled = sw.configMethod
	sw.countsForm = huh.NewForm(
		huh.NewGroup(fields...),
	).WithTheme(theme.HuhTheme()).WithWidth(sw.modalContentWidth())
}

func (sw *SpawnWizard) initOptionsForm() {
	var first []huh.Field
	if !sw.add {
		first = append(first, huh.NewInput().
			Title("Session").
			Description("The new workspace's label").
			Value(&sw.session))
	}
	first = append(first,
		huh.NewInput().Title("Tab label").Description("Empty: each agent's name").Value(&sw.label),
		huh.NewInput().Title("Group").Description("Empty: none").Value(&sw.group))
	sw.optionsForm = huh.NewForm(
		huh.NewGroup(first...),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Kickoff prompt").
				Options(
					huh.NewOption("ACFS's default_new_agent prompt", kickoffDefault),
					huh.NewOption("Write my own", kickoffOwn),
					huh.NewOption("No kickoff", kickoffNone),
				).
				Value(&sw.kickoff),
			huh.NewSelect[string]().
				Title("Memory in the kickoff").
				Options(
					huh.NewOption("CM rules (best-effort)", memoryCM),
					huh.NewOption("CM rules and CASS context", memoryBoth),
					huh.NewOption("Neither", memoryNone),
				).
				Value(&sw.memory),
		),
		huh.NewGroup(
			huh.NewText().Title("Kickoff prompt").Lines(5).Value(&sw.prompt),
		).WithHideFunc(func() bool { return sw.kickoff != kickoffOwn }),
	).WithTheme(theme.HuhTheme()).WithWidth(sw.modalContentWidth())
}

func (sw *SpawnWizard) initConfirmForm() {
	total := sw.total()
	summary := fmt.Sprintf("Start %d agent(s) in %s? This starts paid model work; admission runs again.",
		total, clean(sw.sessionName))
	sw.confirmForm = huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Confirm spawn").
				Description(summary).
				Affirmative("Spawn").
				Negative("Cancel").
				Value(&sw.confirmed),
		),
	).WithTheme(theme.HuhTheme()).WithWidth(sw.modalContentWidth())
}

func (sw *SpawnWizard) initCurrentForm() {
	switch sw.step {
	case SpawnStepMethod:
		sw.initMethodForm()
	case SpawnStepCounts:
		sw.initCountsForm()
	case SpawnStepOptions:
		sw.initOptionsForm()
	}
}

func (sw *SpawnWizard) currentFormInit() tea.Cmd {
	switch sw.step {
	case SpawnStepMethod:
		return sw.methodForm.Init()
	case SpawnStepCounts:
		return sw.countsForm.Init()
	case SpawnStepOptions:
		return sw.optionsForm.Init()
	}
	return nil
}

func (sw *SpawnWizard) modalContentWidth() int {
	w := sw.width - 26 // Account for padding and borders
	if w > 60 {
		w = 60
	}
	if w < 30 {
		w = 30
	}
	return w
}

// validateAgentCount ensures the input is a valid non-negative integer.
func validateAgentCount(s string) error {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("must be a number")
	}
	if n < 0 {
		return fmt.Errorf("must be non-negative")
	}
	if n > 32 {
		return fmt.Errorf("max 32 per type")
	}
	return nil
}

// validateModel accepts an empty model or one nhm puts into an argv.
func validateModel(s string) error {
	if s = strings.TrimSpace(s); s != "" && !spawn.ValidModel(s) {
		return fmt.Errorf("not a model name")
	}
	return nil
}

// parseCount safely parses a count string, returning 0 on error.
func parseCount(s string) int {
	if s == "" {
		return 0
	}
	n, _ := strconv.Atoi(s)
	if n < 0 {
		return 0
	}
	return n
}

// firstOf is the first value that is not empty.
func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
