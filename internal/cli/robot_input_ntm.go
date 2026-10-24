// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/root.go at commit 9d5ed977: loadRobotMessageInput; see
// LICENSE in this directory and PROVENANCE.md. Local changes
// (nhm-2p6.4.2): this header; package and imports; "-" reads nhm's stdin
// variable, which tests replace.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// loadRobotMessageInput resolves a robot message from msg, or from the file
// msgFile ("-" is stdin): not both, at most 10MB, and not blank.
func loadRobotMessageInput(msg, msgFile, msgFlag, fileFlag string) (string, error) {
	if msg != "" && msgFile != "" {
		return "", fmt.Errorf("use either %s or %s, not both", msgFlag, fileFlag)
	}
	if msgFile == "" {
		return msg, nil
	}

	// "--msg-file=-" reads the message from stdin (ntm-kd2s): command-safety
	// filters (dcg PreToolUse) match the full command string and cannot tell
	// executable text from message content, so a prompt that merely MENTIONS
	// a destructive command blocked the whole dispatch. Passing the payload
	// via stdin or a file keeps it out of the scanned command line entirely.
	var r io.Reader
	var f *os.File
	if msgFile == "-" {
		r = stdin
	} else {
		var err error
		f, err = os.Open(msgFile)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", fileFlag, err)
		}
		r = f
	}

	// Read up to 10MB + 1 byte to detect truncation
	limit := int64(10 * 1024 * 1024)
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		if f != nil {
			_ = f.Close()
		}
		return "", fmt.Errorf("read %s: %w", fileFlag, err)
	}
	if f != nil {
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close %s: %w", fileFlag, err)
		}
	}

	if int64(len(data)) > limit {
		return "", fmt.Errorf("%s content too large (max 10MB)", fileFlag)
	}

	if len(data) == 0 || strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("%s content is empty", fileFlag)
	}
	return string(data), nil
}
