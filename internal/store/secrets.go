package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrNoSecret means no token is stored for a binding.
var ErrNoSecret = errors.New("store: no secret for binding")

var bindingIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func secretPath(instanceDir, bindingID string) (string, error) {
	if !bindingIDPattern.MatchString(bindingID) {
		return "", fmt.Errorf("store: invalid binding id %q", bindingID)
	}
	return filepath.Join(instanceDir, SecretsDir, bindingID), nil
}

// WriteSecret stores a binding's registration token in secrets/<binding_id>
// with mode 0600; the directory is 0700. Tokens never go into the
// database, argv, receipts or logs.
func WriteSecret(instanceDir, bindingID, token string) error {
	path, err := secretPath(instanceDir, bindingID)
	if err != nil {
		return err
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("store: a token must be one non-empty line")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return WriteFileAtomic(path, []byte(token+"\n"), 0o600)
}

// ReadSecret returns a binding's token.
func ReadSecret(instanceDir, bindingID string) (string, error) {
	path, err := secretPath(instanceDir, bindingID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("%w %s", ErrNoSecret, bindingID)
	}
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// DeleteSecret removes a binding's token; a missing token is not an error.
func DeleteSecret(instanceDir, bindingID string) error {
	path, err := secretPath(instanceDir, bindingID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}
