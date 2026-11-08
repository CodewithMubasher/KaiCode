package harness

import (
	"strings"
	"testing"
)

func TestRecapStopsAtLastAssistantAndKeepsOrder(t *testing.T) {
	h := []Entry{
		{Role: "user", Text: "first"},
		{Role: "assistant", Text: "answer one"},
		{Role: "user", Text: "second"},
		{Role: "assistant", Text: "calling"},
		{Role: "tool", Text: "pending result"},
	}
	got := recap(h, 10000)
	want := "User: first\n\nAssistant: answer one\n\nUser: second\n\nAssistant: calling"
	if got != want {
		t.Errorf("recap = %q", got)
	}
}

func TestRecapKeepsNewestWithinBudget(t *testing.T) {
	h := []Entry{
		{Role: "user", Text: strings.Repeat("a", 200)},
		{Role: "assistant", Text: "recent"},
	}
	got := recap(h, 50)
	if !strings.Contains(got, "recent") || strings.Contains(got, "aaaa") {
		t.Errorf("recap = %q", got)
	}
}

func TestRecapEmptyBeforeAnyAnswer(t *testing.T) {
	if got := recap([]Entry{{Role: "user", Text: "hi"}}, 1000); got != "" {
		t.Errorf("recap = %q", got)
	}
}
