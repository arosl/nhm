package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The herdr fit probe (nhm doctor --herdr-fit, nhm-qbi.11, plan-v4
// section 8): nhm's plugin pane "fit" runs `nhm ui fit-probe`, a small
// bubbletea program. It appends one JSON line per event to the file that
// NHM_FIT_OUT names (doctor makes the file and passes the variable in the
// pane's env): start, each size, each key, exit. It quits on q or ctrl+c,
// and after fitProbeTTL in any case, so a stray probe never stays.

const (
	fitOutEnv   = "NHM_FIT_OUT"
	fitNonceEnv = "NHM_FIT_NONCE"
	fitProbeTTL = 30 * time.Second
)

// fitEvent is one line the probe writes.
type fitEvent struct {
	Nonce     string `json:"nonce"`
	Event     string `json:"event"` // start, size, key, exit
	PID       int    `json:"pid,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Key       string `json:"key,omitempty"`
	Term      string `json:"term,omitempty"`
	ColorTerm string `json:"colorterm,omitempty"`
	Profile   string `json:"profile,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type fitProbeTimeout struct{}

type fitProbe struct {
	write  func(fitEvent)
	ttl    time.Duration
	width  int
	height int
	keys   []string
}

func (m fitProbe) Init() tea.Cmd {
	return tea.Tick(m.ttl, func(time.Time) tea.Msg { return fitProbeTimeout{} })
}

func (m fitProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.write(fitEvent{Event: "size", Width: msg.Width, Height: msg.Height})
	case tea.KeyMsg:
		k := msg.String()
		m.write(fitEvent{Event: "key", Key: k})
		m.keys = append(m.keys, k)
		if len(m.keys) > 5 {
			m.keys = m.keys[len(m.keys)-5:]
		}
		if k == "q" || k == "ctrl+c" {
			m.write(fitEvent{Event: "exit", Reason: k})
			return m, tea.Quit
		}
	case fitProbeTimeout:
		m.write(fitEvent{Event: "exit", Reason: "ttl"})
		return m, tea.Quit
	}
	return m, nil
}

func (m fitProbe) View() string {
	return fmt.Sprintf("nhm doctor --herdr-fit: a test of this herdr placement.\nIt closes by itself.\n\nsize %dx%d  keys %s\n",
		m.width, m.height, strings.Join(m.keys, " "))
}

// profileName names lipgloss's colour profile for the probe's start line.
func profileName(p termenv.Profile) string {
	switch p {
	case termenv.TrueColor:
		return "truecolor"
	case termenv.ANSI256:
		return "ansi256"
	case termenv.ANSI:
		return "ansi16"
	}
	return "mono"
}

// openFitOut opens the probe's event file for appending. Doctor makes it;
// the probe never creates a file from its environment, and refuses
// anything but a regular file. The Lstat keeps it from opening a FIFO;
// O_NOFOLLOW and the Stat of the open file refuse what was swapped in
// after the Lstat.
func openFitOut(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return f, nil
}

func runFitProbe(stderr io.Writer) int {
	out, nonce := os.Getenv(fitOutEnv), os.Getenv(fitNonceEnv)
	if out == "" || nonce == "" {
		fmt.Fprintln(stderr, "nhm ui fit-probe: nhm doctor --herdr-fit opens it in a herdr pane; it does not run by hand")
		return exitRefused
	}
	f, err := openFitOut(out)
	if err != nil {
		fmt.Fprintf(stderr, "nhm ui fit-probe: %v\n", err)
		return exitError
	}
	defer f.Close()
	var werr error
	write := func(e fitEvent) {
		e.Nonce = nonce
		data, err := json.Marshal(e)
		if err == nil {
			_, err = f.Write(append(data, '\n'))
		}
		werr = errors.Join(werr, err)
	}
	write(fitEvent{Event: "start", PID: os.Getpid(), Term: os.Getenv("TERM"), ColorTerm: os.Getenv("COLORTERM"),
		Profile: profileName(lipgloss.ColorProfile())})
	if _, err := tea.NewProgram(fitProbe{write: write, ttl: fitProbeTTL}, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintf(stderr, "nhm ui fit-probe: %v\n", err)
		return exitError
	}
	if werr != nil {
		fmt.Fprintf(stderr, "nhm ui fit-probe: %v\n", werr)
		return exitError
	}
	return exitOK
}
