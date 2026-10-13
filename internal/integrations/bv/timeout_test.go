// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/bv/timeout_test.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: this header; imports. Kept:
// resetConfiguredTimeout, TestCommandTimeoutPrecedence. Left out:
// TestGetInsightsHonorsEnvTimeoutWithSlowBV and
// TestBVChildInheritsCallerBVEnvVars, which run bv through GetInsights
// (not taken).

package bv

import (
	"testing"
	"time"
)

// resetConfiguredTimeout snapshots and restores the process-global configured
// timeout so precedence tests cannot leak into other tests in the package.
func resetConfiguredTimeout(t *testing.T) {
	t.Helper()
	prev := configuredTimeoutSeconds.Load()
	t.Cleanup(func() { configuredTimeoutSeconds.Store(prev) })
	configuredTimeoutSeconds.Store(0)
}

func TestCommandTimeoutPrecedence(t *testing.T) {
	resetConfiguredTimeout(t)
	t.Setenv(BVTimeoutEnvVar, "")

	// Default: nothing configured, no env.
	if got := CommandTimeout(); got != DefaultTimeout {
		t.Fatalf("default CommandTimeout() = %v, want %v", got, DefaultTimeout)
	}

	// Config value applies.
	ConfigureCommandTimeout(7)
	if got := CommandTimeout(); got != 7*time.Second {
		t.Fatalf("configured CommandTimeout() = %v, want 7s", got)
	}

	// Non-positive config values are ignored.
	ConfigureCommandTimeout(0)
	ConfigureCommandTimeout(-5)
	if got := CommandTimeout(); got != 7*time.Second {
		t.Fatalf("CommandTimeout() after invalid ConfigureCommandTimeout = %v, want 7s", got)
	}

	// Env wins over config.
	t.Setenv(BVTimeoutEnvVar, "3")
	if got := CommandTimeout(); got != 3*time.Second {
		t.Fatalf("env-overridden CommandTimeout() = %v, want 3s", got)
	}

	// Invalid env values fall back to the configured value.
	for _, raw := range []string{"abc", "0", "-2", "2.5"} {
		t.Setenv(BVTimeoutEnvVar, raw)
		if got := CommandTimeout(); got != 7*time.Second {
			t.Fatalf("CommandTimeout() with NTM_BV_TIMEOUT=%q = %v, want configured 7s", raw, got)
		}
	}
}
