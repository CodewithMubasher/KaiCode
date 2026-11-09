package harness

import (
	"context"
	"strings"
	"testing"
)

func TestResumeSendsRecapWithFirstMessage(t *testing.T) {
	var prompts []string
	var newChat []bool
	a, _ := newAgent(func(n int, tt Turn) string {
		prompts = append(prompts, tt.Prompt)
		newChat = append(newChat, tt.NewChat)
		return "ok"
	})
	a.Resume([]Entry{
		{Role: "user", Text: "build a parser"},
		{Role: "assistant", Text: "the parser is built"},
	})
	drain(t, a.Run(context.Background(), "now add tests"))

	if len(prompts) != 1 || !newChat[0] {
		t.Fatalf("want one prompt that opens a new chat, got %d (newChat=%v)", len(prompts), newChat)
	}
	for _, want := range []string{"build a parser", "the parser is built", "now add tests"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("first prompt after Resume is missing %q:\n%s", want, prompts[0])
		}
	}
	if h := a.History(); len(h) != 4 { // 2 resumed + the new user message + the reply
		t.Errorf("history should continue the resumed one, got %d entries", len(h))
	}
}

func TestSetProviderKeepsConversation(t *testing.T) {
	a, _ := newAgent(func(n int, tt Turn) string { return "first answer" })
	drain(t, a.Run(context.Background(), "hello kai"))

	var got Turn
	second := &MockProvider{Script: func(n int, tt Turn) string { got = tt; return "second answer" }, ChunkRunes: 5}
	a.SetProvider(second)
	drain(t, a.Run(context.Background(), "carry on"))

	if !got.NewChat {
		t.Error("a new provider must start a new chat")
	}
	if !strings.Contains(got.Prompt, "hello kai") || !strings.Contains(got.Prompt, "first answer") {
		t.Errorf("the new provider should get a recap of the earlier conversation:\n%s", got.Prompt)
	}
}
