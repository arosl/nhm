// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/tui/icons/env_detection_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header only.

package icons

import (
	"testing"
)

// ---------------------------------------------------------------------------
// HasNerdFonts — at 15.8%, exercise all env branches
// ---------------------------------------------------------------------------

func TestHasNerdFonts_ExplicitEnable(t *testing.T) {
	t.Setenv("NTM_USE_ICONS", "1")
	t.Setenv("NERD_FONTS", "")
	if !HasNerdFonts() {
		t.Error("expected true with NTM_USE_ICONS=1")
	}
}

func TestHasNerdFonts_NerdFontsEnv(t *testing.T) {
	t.Setenv("NTM_USE_ICONS", "")
	t.Setenv("NERD_FONTS", "1")
	if !HasNerdFonts() {
		t.Error("expected true with NERD_FONTS=1")
	}
}

func TestHasNerdFonts_ExplicitDisable(t *testing.T) {
	t.Setenv("NTM_USE_ICONS", "0")
	t.Setenv("NERD_FONTS", "")
	if HasNerdFonts() {
		t.Error("expected false with NTM_USE_ICONS=0")
	}
}

func TestHasNerdFonts_NerdFontsDisable(t *testing.T) {
	t.Setenv("NTM_USE_ICONS", "")
	t.Setenv("NERD_FONTS", "0")
	if HasNerdFonts() {
		t.Error("expected false with NERD_FONTS=0")
	}
}

func TestHasNerdFonts_TerminalPrograms(t *testing.T) {
	for _, prog := range []string{"iTerm.app", "WezTerm", "Alacritty", "kitty", "Hyper"} {
		t.Run(prog, func(t *testing.T) {
			t.Setenv("NTM_USE_ICONS", "")
			t.Setenv("NERD_FONTS", "")
			t.Setenv("TERM_PROGRAM", prog)
			t.Setenv("KITTY_WINDOW_ID", "")
			t.Setenv("WEZTERM_PANE", "")
			if !HasNerdFonts() {
				t.Errorf("expected true for TERM_PROGRAM=%s", prog)
			}
		})
	}
}

func TestHasNerdFonts_KittyWindowID(t *testing.T) {
	t.Setenv("NTM_USE_ICONS", "")
	t.Setenv("NERD_FONTS", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "42")
	if !HasNerdFonts() {
		t.Error("expected true with KITTY_WINDOW_ID set")
	}
}

func TestHasNerdFonts_WezTermPane(t *testing.T) {
	t.Setenv("NTM_USE_ICONS", "")
	t.Setenv("NERD_FONTS", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("WEZTERM_PANE", "0")
	if !HasNerdFonts() {
		t.Error("expected true with WEZTERM_PANE set")
	}
}

func TestHasNerdFonts_VSCode(t *testing.T) {
	t.Setenv("NTM_USE_ICONS", "")
	t.Setenv("NERD_FONTS", "")
	t.Setenv("TERM_PROGRAM", "vscode")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("WEZTERM_PANE", "")
	if !HasNerdFonts() {
		t.Error("expected true for TERM_PROGRAM=vscode")
	}
}

func TestHasNerdFonts_256Color(t *testing.T) {
	t.Setenv("NTM_USE_ICONS", "")
	t.Setenv("NERD_FONTS", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("WEZTERM_PANE", "")
	t.Setenv("TERM", "xterm-256color")
	// Exercise the 256color branch — result depends on whether ~/.p10k.zsh exists.
	_ = HasNerdFonts()
}

func TestHasNerdFonts_PlainTerminal(t *testing.T) {
	t.Setenv("NTM_USE_ICONS", "")
	t.Setenv("NERD_FONTS", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("WEZTERM_PANE", "")
	t.Setenv("TERM", "dumb")
	// Exercise the default branch — result depends on ~/.p10k.zsh presence.
	_ = HasNerdFonts()
}

// ---------------------------------------------------------------------------
// HasUnicode — at 50%, exercise env branches
// ---------------------------------------------------------------------------

func TestHasUnicode_LangUTF(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("TERM", "dumb")
	if !HasUnicode() {
		t.Error("expected true with UTF-8 LANG")
	}
}

func TestHasUnicode_LCAllUTF(t *testing.T) {
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "en_US.utf8")
	t.Setenv("TERM", "dumb")
	if !HasUnicode() {
		t.Error("expected true with UTF-8 LC_ALL")
	}
}

func TestHasUnicode_XtermTerm(t *testing.T) {
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("TERM", "xterm")
	if !HasUnicode() {
		t.Error("expected true for xterm")
	}
}

func TestHasUnicode_256Color(t *testing.T) {
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("TERM", "xterm-256color")
	if !HasUnicode() {
		t.Error("expected true for 256color")
	}
}

func TestHasUnicode_ScreenTerm(t *testing.T) {
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("TERM", "screen")
	if !HasUnicode() {
		t.Error("expected true for screen")
	}
}

func TestHasUnicode_TmuxTerm(t *testing.T) {
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("TERM", "tmux-256color")
	if !HasUnicode() {
		t.Error("expected true for tmux terminal")
	}
}

func TestHasUnicode_DefaultTrue(t *testing.T) {
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("TERM", "dumb")
	// Even with dumb terminal, default should be true in modern era.
	if !HasUnicode() {
		t.Error("expected true for default (modern era)")
	}
}

// ---------------------------------------------------------------------------
// Detect — exercise auto detection branch with Unicode env
// ---------------------------------------------------------------------------

func TestDetect_AutoWithUnicode(t *testing.T) {
	t.Setenv("NTM_ICONS", "auto")
	t.Setenv("NTM_USE_ICONS", "")
	t.Setenv("NERD_FONTS", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("WEZTERM_PANE", "")
	t.Setenv("TERM", "dumb")
	t.Setenv("LANG", "en_US.UTF-8")

	icons := Detect()
	// Should get unicode set (not nerd fonts, not ascii).
	if icons.Claude == "" {
		t.Error("expected non-empty Claude icon from Detect")
	}
}
