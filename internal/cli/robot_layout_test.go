package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/robot"
)

// A successful robot answer is indented, as ntm's JSONRenderer writes it;
// an error envelope is one line, as ntm's encodeRobotFailureJSON writes it
// (nhm-2p6.29).
func TestRobotWriteLayout(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	var ok bytes.Buffer
	if code := robotWrite(&ok, robot.NewResponse(true, "json", now), robotOK); code != robotOK ||
		!strings.HasPrefix(ok.String(), "{\n  \"success\": true,") {
		t.Errorf("success: exit %d, %q", code, ok.String())
	}
	for _, exit := range []int{robotError, robotNotImplemented} {
		var fail bytes.Buffer
		if code := robotFail(&fail, robot.ErrCodeInvalidFlag, "bad", "a hint", "json", exit, now); code != exit ||
			!strings.HasPrefix(fail.String(), `{"success":false,`) || strings.Count(fail.String(), "\n") != 1 {
			t.Errorf("exit %d: got %d, %q", exit, code, fail.String())
		}
	}
	// A robot call's own error, through the root.
	code, out, _ := run("--robot-version", "--robot-format=xml")
	if code != robotError || !strings.HasPrefix(out, `{"success":false,`) || strings.Count(out, "\n") != 1 {
		t.Errorf("--robot-format=xml: exit %d, %q", code, out)
	}
}
