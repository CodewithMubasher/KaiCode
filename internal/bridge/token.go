package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// TokenPath is where the shared secret lives.
func TokenPath(dir string) string { return filepath.Join(dir, "bridge-token") }

// LoadOrCreateToken returns the shared secret the extension must present,
// creating it (mode 0600) on first use.
func LoadOrCreateToken(dir string) (string, error) {
	path := TokenPath(dir)
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	t, err := randomHex(24)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
