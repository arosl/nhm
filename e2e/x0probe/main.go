// Command x0probe is the X0 herdr fit probe (nhm-qbi.1, plan-v4 section 8).
// It is not shipped. It checks that a bubbletea program with an embedded
// huh form, an asynchronous operation and a result screen works in each
// herdr placement (popup, overlay, tab, split): keys, resize, paste and the
// color profile. Every key and resize goes to the -log file, one line each.
//
//	go run ./e2e/x0probe -log /tmp/x0probe.log
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

type phase int

const (
	phaseForm phase = iota
	phaseRunning
	phaseResult
)

func (p phase) String() string {
	switch p {
	case phaseForm:
		return "form"
	case phaseRunning:
		return "running"
	default:
		return "result"
	}
}

// values holds the form's fields. huh writes through these pointers, so
// they live outside the model value that bubbletea copies.
type values struct {
	label string
	text  string
	run   bool
}

// opDoneMsg ends the fake operation.
type opDoneMsg struct {
	started time.Time
	label   string
	lines   int
}

type model struct {
	phase   phase
	form    *huh.Form
	vals    *values
	delay   time.Duration
	log     func(string)
	profile string
	width   int
	height  int
	keys    []string // the last few keys, newest last
	result  string
	runs    int
	now     func() time.Time
}

func newModel(delay time.Duration, logf func(string), profile string) model {
	m := model{delay: delay, log: logf, profile: profile, now: time.Now}
	m.resetForm()
	return m
}

func (m *model) resetForm() {
	m.vals = &values{run: true}
	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Label").Value(&m.vals.label),
			huh.NewText().Title("Text (paste here; alt+enter for a new line)").
				CharLimit(0).Lines(4).Value(&m.vals.text),
			huh.NewConfirm().Title("Run the fake operation?").Value(&m.vals.run),
		),
	).WithShowHelp(true)
	m.phase = phaseForm
}

func (m model) Init() tea.Cmd {
	m.log(fmt.Sprintf("start profile=%s TERM=%q COLORTERM=%q", m.profile, os.Getenv("TERM"), os.Getenv("COLORTERM")))
	return m.form.Init()
}

// describeKey renders a key for the log: its name, and for a paste its
// size, so a long paste is one line.
func describeKey(k tea.KeyMsg) string {
	if k.Paste {
		s := string(k.Runes)
		return fmt.Sprintf("paste runes=%d lines=%d", len(k.Runes), strings.Count(s, "\n")+1)
	}
	return fmt.Sprintf("key %q type=%d alt=%t", k.String(), int(k.Type), k.Alt)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.log(fmt.Sprintf("resize %dx%d phase=%s", msg.Width, msg.Height, m.phase))
	case tea.KeyMsg:
		d := describeKey(msg)
		m.log(d + " phase=" + m.phase.String())
		m.keys = append(m.keys, d)
		if len(m.keys) > 5 {
			m.keys = m.keys[len(m.keys)-5:]
		}
		if msg.Type == tea.KeyCtrlC {
			m.log("quit ctrl+c")
			return m, tea.Quit
		}
	case opDoneMsg:
		m.log(fmt.Sprintf("op done after %s", m.now().Sub(msg.started).Round(time.Millisecond)))
		m.phase = phaseResult
		m.result = fmt.Sprintf("Operation %d done: label %q, %d text line(s).", m.runs, msg.label, msg.lines)
		return m, nil
	}

	switch m.phase {
	case phaseForm:
		fm, cmd := m.form.Update(msg)
		if f, ok := fm.(*huh.Form); ok {
			m.form = f
		}
		switch m.form.State {
		case huh.StateCompleted:
			if !m.vals.run {
				m.phase = phaseResult
				m.result = "Not run (you said no)."
				m.log("form completed, run=false")
				return m, nil
			}
			m.runs++
			m.phase = phaseRunning
			m.log(fmt.Sprintf("form completed, op %d starts", m.runs))
			return m, m.startOp()
		case huh.StateAborted:
			// huh aborts on its quit key; show it as a result, do not exit.
			m.phase = phaseResult
			m.result = "Form aborted."
			m.log("form aborted")
			return m, nil
		}
		return m, cmd
	case phaseResult:
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "enter":
				m.log("result dismissed")
				m.resetForm()
				return m, m.form.Init()
			case "q":
				m.log("quit q")
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

// startOp runs the fake operation: it completes after m.delay, off the
// update loop.
func (m model) startOp() tea.Cmd {
	done := opDoneMsg{started: m.now(), label: m.vals.label, lines: strings.Count(m.vals.text, "\n") + 1}
	if m.vals.text == "" {
		done.lines = 0
	}
	return tea.Tick(m.delay, func(time.Time) tea.Msg { return done })
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF79C6"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
)

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("nhm X0 probe") + "  ")
	b.WriteString(dimStyle.Render(fmt.Sprintf("size %dx%d  profile %s  phase %s", m.width, m.height, m.profile, m.phase)))
	b.WriteString("\n")
	b.WriteString(swatch() + "\n\n")
	switch m.phase {
	case phaseForm:
		b.WriteString(m.form.View())
	case phaseRunning:
		b.WriteString(fmt.Sprintf("Fake operation %d running (%s)…\n", m.runs, m.delay))
	case phaseResult:
		b.WriteString(okStyle.Render(m.result) + "\n")
		b.WriteString(dimStyle.Render("enter: new form  q: quit  ctrl+c: quit") + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("last keys:") + "\n")
	for _, k := range m.keys {
		b.WriteString(dimStyle.Render("  "+k) + "\n")
	}
	return b.String()
}

// swatch shows a truecolor gradient, the 16 ANSI colors and a 256-color
// ramp; lipgloss degrades each to the detected profile.
func swatch() string {
	var b strings.Builder
	for i := 0; i < 16; i++ {
		c := fmt.Sprintf("#%02x%02x%02x", 255-i*16, i*16, 128)
		b.WriteString(lipgloss.NewStyle().Background(lipgloss.Color(c)).Render(" "))
	}
	b.WriteString(" ")
	for i := 0; i < 16; i++ {
		b.WriteString(lipgloss.NewStyle().Background(lipgloss.Color(fmt.Sprint(i))).Render(" "))
	}
	b.WriteString(" ")
	for i := 232; i < 256; i += 2 {
		b.WriteString(lipgloss.NewStyle().Background(lipgloss.Color(fmt.Sprint(i))).Render(" "))
	}
	return b.String()
}

func profileName(p termenv.Profile) string {
	switch p {
	case termenv.TrueColor:
		return "truecolor"
	case termenv.ANSI256:
		return "ansi256"
	case termenv.ANSI:
		return "ansi16"
	default:
		return "ascii (mono)"
	}
}

// fileLogger returns a log function that writes one UTC-stamped line per
// call to w.
func fileLogger(w io.Writer, now func() time.Time) func(string) {
	return func(s string) {
		fmt.Fprintf(w, "%s %s\n", now().UTC().Format("2006-01-02T15:04:05.000Z"), s)
	}
}

func main() {
	logPath := flag.String("log", "", "append every key and resize to this file (required)")
	delay := flag.Duration("delay", 3*time.Second, "how long the fake operation takes")
	flag.Parse()
	if *logPath == "" {
		fmt.Fprintln(os.Stderr, "usage: x0probe -log FILE [-delay 3s]")
		os.Exit(2)
	}
	f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "x0probe:", err)
		os.Exit(1)
	}
	defer f.Close()
	m := newModel(*delay, fileLogger(f, time.Now), profileName(lipgloss.ColorProfile()))
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "x0probe:", err)
		os.Exit(1)
	}
}
