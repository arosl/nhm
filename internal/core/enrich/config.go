package enrich

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the enrichment part of ~/.config/nhm/config.toml:
//
//	[memory]
//	cm = "off"                 # raw sends: off, best-effort or required
//	[cass]
//	context = "off"            # raw sends: off, best-effort or required
//	check = true               # the duplicate check of operator sends
//	check_days = 7             # ntm's default; the matrix's 30 is a value here
//	limit = 5                  # CASS hits per query
//	[enrichment]
//	budget_ms = 5000           # one total budget for a batch of queries
//	block_bytes = 4096         # per framed block
//	item_bytes = 1024          # per item
//	[learning]
//	implicit_outcome = "closed-assigned"   # or "off"
//
// Kickoffs and handoffs default to best-effort CM (plan-v8 2.13); that is
// the caller's default, not a key here.
type Config struct {
	Memory struct {
		CM Mode `toml:"cm"`
	} `toml:"memory"`
	CASS struct {
		Context   Mode   `toml:"context"`
		Check     *bool  `toml:"check"`
		CheckDays int    `toml:"check_days"`
		Limit     int    `toml:"limit"`
		Binary    string `toml:"binary"`
	} `toml:"cass"`
	CMBinary   string `toml:"cm_binary"`
	Enrichment struct {
		BudgetMS   int `toml:"budget_ms"`
		BlockBytes int `toml:"block_bytes"`
		ItemBytes  int `toml:"item_bytes"`
	} `toml:"enrichment"`
	Learning struct {
		ImplicitOutcome string `toml:"implicit_outcome"`
	} `toml:"learning"`
}

// Defaults.
const (
	DefaultBudget     = 5 * time.Second
	DefaultBlockBytes = 4096
	DefaultItemBytes  = 1024
	DefaultCheckDays  = 7
	DefaultCASSLimit  = 5
	// LearningClosedAssigned turns on the implicit outcome of a bead
	// that closes while assigned to the agent that got the rules.
	LearningClosedAssigned = "closed-assigned"
)

// ConfigPath is $XDG_CONFIG_HOME/nhm/config.toml.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nhm", "config.toml"), nil
}

// LoadConfig reads the config file; a missing file is the defaults. Other
// keys of the file belong to other parts of nhm and are ignored here.
func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c.normalized()
	}
	if err != nil {
		return c, err
	}
	if _, err := toml.Decode(string(data), &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c.normalized()
}

func (c Config) normalized() (Config, error) {
	var err error
	if c.Memory.CM, err = ParseMode(string(c.Memory.CM)); err != nil {
		return c, fmt.Errorf("[memory] cm: %w", err)
	}
	if c.CASS.Context, err = ParseMode(string(c.CASS.Context)); err != nil {
		return c, fmt.Errorf("[cass] context: %w", err)
	}
	switch c.Learning.ImplicitOutcome {
	case "":
		c.Learning.ImplicitOutcome = "off"
	case "off", LearningClosedAssigned:
	default:
		return c, fmt.Errorf("[learning] implicit_outcome is off or %s, not %q", LearningClosedAssigned, c.Learning.ImplicitOutcome)
	}
	if c.CASS.CheckDays <= 0 {
		c.CASS.CheckDays = DefaultCheckDays
	}
	if c.CASS.Limit <= 0 {
		c.CASS.Limit = DefaultCASSLimit
	}
	if c.Enrichment.BudgetMS <= 0 {
		c.Enrichment.BudgetMS = int(DefaultBudget / time.Millisecond)
	}
	if c.Enrichment.BlockBytes <= 0 {
		c.Enrichment.BlockBytes = DefaultBlockBytes
	}
	if c.Enrichment.ItemBytes <= 0 {
		c.Enrichment.ItemBytes = DefaultItemBytes
	}
	return c, nil
}

// CheckOn is whether operator sends run the duplicate check (default on,
// as in ntm).
func (c Config) CheckOn() bool { return c.CASS.Check == nil || *c.CASS.Check }

// Budgets are the config's budgets.
func (c Config) Budgets() Budgets {
	return Budgets{Total: time.Duration(c.Enrichment.BudgetMS) * time.Millisecond,
		BlockBytes: c.Enrichment.BlockBytes, ItemBytes: c.Enrichment.ItemBytes, CASSLimit: c.CASS.Limit}
}
