package harness

import (
	"context"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"hello world", 4},    // 2 + 2 words of four characters
		{"a b", 2},            // single letters still cost a token each
		{"你好世界", 4},           // non-ASCII runes cost one token each
		{"func main() {}", 6}, // keyword, ident and three punctuation marks
	}
	for _, c := range cases {
		if got := EstimateTokens(c.in); got != c.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if EstimateTokens("hello world") <= EstimateTokens("hello") {
		t.Error("more text must not count fewer tokens")
	}
}

// The provider-level gauge must include the first prompt (instructions and
// tool specs travel in it) and everything the model writes, not the UI labels.
func TestUsageCountsTheWholeProviderContext(t *testing.T) {
	const reply = "hello there, this is the answer"
	a, _ := newAgent(func(n int, tn Turn) string { return reply })

	evs := drain(t, a.Run(context.Background(), "do it"))

	saw := false
	var last int
	for _, e := range evs {
		if e.Kind == EvUsage {
			saw = true
			last = e.Tokens
		}
	}
	if !saw {
		t.Fatal("no usage events")
	}
	// First prompt carries "sys" plus the tool spec, the user text and the
	// reply; all of that has to be in the final count.
	if last < EstimateTokens("sys"+reply+"do it") {
		t.Errorf("tokens = %d, want at least the prompt and the reply", last)
	}
	if last == 0 {
		t.Error("tokens = 0 after a full turn")
	}
}
