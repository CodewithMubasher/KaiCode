package harness

import (
	"strings"

	"kai/internal/tools"
)

const (
	recapBudget    = 12 * 1024 // bytes of history replayed into a fresh chat
	recapEntryMax  = 1500      // bytes kept of any single entry
	recapUserLabel = "User"
)

// recap turns Kai's own record of the conversation into text a fresh chat can
// be given. It keeps the most recent entries that fit the budget and stops at
// the last assistant message: anything after it is the message still waiting
// to be sent, which travels separately.
func recap(h []Entry, budget int) string {
	end := len(h)
	for end > 0 && h[end-1].Role != "assistant" {
		end--
	}
	var parts []string
	used := 0
	for i := end - 1; i >= 0; i-- {
		label := map[string]string{"user": recapUserLabel, "assistant": "Assistant", "tool": "Tool result"}[h[i].Role]
		if label == "" {
			continue
		}
		line := label + ": " + tools.Truncate(strings.TrimSpace(h[i].Text), recapEntryMax)
		if used+len(line) > budget && len(parts) > 0 {
			break
		}
		parts = append(parts, line)
		used += len(line)
	}
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return strings.Join(parts, "\n\n")
}
