package ui

import (
	"os"
	"path/filepath"
	"strings"
)

// cwdLabel returns the working directory as "~\path:branch" (the branch is
// read from .git/HEAD, and left out outside a repository).
func cwdLabel() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	label := wd
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(wd, home) {
		label = "~" + strings.TrimPrefix(wd, home)
	}
	for dir := wd; ; {
		if b, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD")); err == nil {
			head := strings.TrimSpace(string(b))
			if ref, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
				return label + ":" + ref
			}
			if len(head) > 7 {
				head = head[:7]
			}
			return label + ":" + head
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return label
		}
		dir = parent
	}
}
