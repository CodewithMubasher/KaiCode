package dsml

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"kai/internal/tools"
)

const sample = "I'll read it first.\n" +
	"<|DSML|tool_calls>\n" +
	"<|DSML|invoke name=\"read_file\">\n" +
	"<|DSML|parameter name=\"path\" string=\"true\">go.mod</|DSML|parameter>\n" +
	"</|DSML|invoke>\n" +
	"</|DSML|tool_calls><|end▁of▁sentence|>"

func TestParseWholeReply(t *testing.T) {
	text, calls, warns := Parse(sample)
	if text != "I'll read it first.\n" {
		t.Errorf("text = %q", text)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v", warns)
	}
	want := []tools.Call{{ID: "call_1", Name: "read_file", Args: map[string]any{"path": "go.mod"}}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %#v, want %#v", calls, want)
	}
}

// Whatever way the stream is cut into chunks, the result must be the same.
func TestParseAnyChunking(t *testing.T) {
	wantText, wantCalls, _ := Parse(sample)
	runes := []rune(sample)
	for size := 1; size <= 9; size++ {
		p := NewParser()
		var text strings.Builder
		for i := 0; i < len(runes); i += size {
			end := i + size
			if end > len(runes) {
				end = len(runes)
			}
			text.WriteString(p.Feed(string(runes[i:end])))
		}
		text.WriteString(p.Finish())
		if text.String() != wantText {
			t.Errorf("size %d: text = %q, want %q", size, text.String(), wantText)
		}
		if !reflect.DeepEqual(p.Calls(), wantCalls) {
			t.Errorf("size %d: calls = %#v", size, p.Calls())
		}
	}
}

func TestParseFunctionCallsAndFullWidthBars(t *testing.T) {
	in := "<｜DSML｜function_calls>" +
		"<｜DSML｜invoke name=\"read_file\">" +
		"<｜DSML｜parameter name=\"path\" string=\"true\">a.txt</｜DSML｜parameter>" +
		"</｜DSML｜invoke>" +
		"</｜DSML｜function_calls>"
	_, calls, warns := Parse(in)
	if len(calls) != 1 || calls[0].Args["path"] != "a.txt" || len(warns) != 0 {
		t.Fatalf("calls = %#v warns = %v", calls, warns)
	}
}

func TestParseTypedParameters(t *testing.T) {
	in := "<|DSML|tool_calls><|DSML|invoke name=\"x\">" +
		"<|DSML|parameter name=\"count\" string=\"false\">5</|DSML|parameter>" +
		"<|DSML|parameter name=\"flag\" string=\"false\">true</|DSML|parameter>" +
		"<|DSML|parameter name=\"list\" string=\"false\">[1, 2]</|DSML|parameter>" +
		"<|DSML|parameter name=\"text\" string=\"true\">5</|DSML|parameter>" +
		"</|DSML|invoke></|DSML|tool_calls>"
	_, calls, _ := Parse(in)
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	a := calls[0].Args
	if a["count"] != float64(5) || a["flag"] != true || a["text"] != "5" {
		t.Errorf("args = %#v", a)
	}
	if l, ok := a["list"].([]any); !ok || len(l) != 2 {
		t.Errorf("list = %#v", a["list"])
	}
}

func TestParseTwoCallsKeepOrder(t *testing.T) {
	in := "<|DSML|tool_calls>" +
		"<|DSML|invoke name=\"a\"></|DSML|invoke>" +
		"<|DSML|invoke name=\"b\"></|DSML|invoke>" +
		"</|DSML|tool_calls>"
	_, calls, _ := Parse(in)
	if len(calls) != 2 || calls[0].Name != "a" || calls[1].Name != "b" || calls[0].ID == calls[1].ID {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestParseTruncatedBlockKeepsCompleteCalls(t *testing.T) {
	in := "ok\n<|DSML|tool_calls>" +
		"<|DSML|invoke name=\"a\"></|DSML|invoke>" +
		"<|DSML|invoke name=\"b\"><|DSML|parameter name=\"p\" string=\"true\">hal"
	text, calls, warns := Parse(in)
	if text != "ok\n" {
		t.Errorf("text = %q", text)
	}
	if len(calls) != 1 || calls[0].Name != "a" {
		t.Errorf("calls = %#v", calls)
	}
	if len(warns) == 0 {
		t.Error("expected a warning for the unclosed block")
	}
}

func TestParseMalformedBlockWarns(t *testing.T) {
	_, calls, warns := Parse("<|DSML|tool_calls>garbage</|DSML|tool_calls>")
	if len(calls) != 0 || len(warns) == 0 {
		t.Fatalf("calls = %#v warns = %v", calls, warns)
	}
}

func TestPlainTextPassesThrough(t *testing.T) {
	for _, in := range []string{
		"just text",
		"a < b and c > d",
		"use <b>bold</b> here",
		"ends with a bracket <",
		"ends with a half marker <|DSM",
	} {
		text, calls, _ := Parse(in)
		if text != in || len(calls) != 0 {
			t.Errorf("Parse(%q) = %q, %d calls", in, text, len(calls))
		}
	}
}

func TestHalfMarkerIsHeldBack(t *testing.T) {
	p := NewParser()
	if got := p.Feed("hello <|DS"); got != "hello " {
		t.Errorf("first chunk released %q", got)
	}
	if got := p.Feed("ML|tool_calls></|DSML|tool_calls>"); got != "" {
		t.Errorf("second chunk released %q", got)
	}
}

func TestEncodeResults(t *testing.T) {
	got := EncodeResults([]tools.Result{
		{Name: "a", Output: "one"},
		{Name: "b", Output: "bad", IsError: true},
	})
	want := "<tool_result>\none\n</tool_result>\n<tool_result>\nError: bad\n</tool_result>"
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestFirstPromptHasInstructionsToolsAndRequest(t *testing.T) {
	got := FirstPrompt("Be brief.", []tools.Spec{{Name: "read_file", Description: "d"}}, "do it")
	for _, want := range []string{"Be brief.", "<|DSML|tool_calls>", `"name":"read_file"`, "do it"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}

// The model's actual reply that leaked into the TUI: a BOM, blank lines, double
// full-width bars, a space after the bars, and the wrapper named just "calls".
const leaked = "\ufeff\n\n<｜｜DSML｜｜ calls>\n" +
	"<｜｜DSML｜｜ invoke name=\"shell\">\n" +
	"<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">go version</｜｜DSML｜｜ parameter>\n" +
	"</｜｜DSML｜｜ invoke>\n" +
	"</｜｜DSML｜｜ calls>"

func TestParseTheLeakedReply(t *testing.T) {
	text, calls, warns := Parse(leaked)
	if len(calls) != 1 || calls[0].Name != "shell" || calls[0].Args["command"] != "go version" {
		t.Fatalf("calls = %#v warns = %v", calls, warns)
	}
	if len(warns) != 0 {
		t.Errorf("warns = %v", warns)
	}
	if strings.TrimSpace(text) != "" {
		t.Errorf("markup leaked into the visible text: %q", text)
	}
}

func TestParseTheLeakedReplyAnyChunking(t *testing.T) {
	check := func(name, text string, chunks []string) {
		t.Helper()
		p := NewParser()
		var visible string
		for _, c := range chunks {
			visible += p.Feed(c)
		}
		visible += p.Finish()
		if len(p.Calls()) != 1 || p.Calls()[0].Args["command"] != "go version" {
			t.Fatalf("%s: calls = %#v", name, p.Calls())
		}
		if strings.TrimSpace(visible) != "" {
			t.Fatalf("%s leaked %q", name, visible)
		}
	}

	// Whole characters per chunk, as the stream delivers them.
	runes := []rune(leaked)
	for size := 1; size <= len(runes); size++ {
		var chunks []string
		for i := 0; i < len(runes); i += size {
			chunks = append(chunks, string(runes[i:min(i+size, len(runes))]))
		}
		check(fmt.Sprintf("%d runes per chunk", size), leaked, chunks)
	}

	// Even single bytes, splitting the multi-byte bars, must not break the tag.
	body := strings.TrimPrefix(leaked, "\ufeff")
	for size := 1; size <= len(body); size++ {
		var chunks []string
		for i := 0; i < len(body); i += size {
			chunks = append(chunks, body[i:min(i+size, len(body))])
		}
		check(fmt.Sprintf("%d bytes per chunk", size), body, chunks)
	}
}

func TestParseMarkupVariants(t *testing.T) {
	inner := `<|DSML|invoke name="shell"><|DSML|parameter name="command" string="true">ls</|DSML|parameter></|DSML|invoke>`
	cases := map[string]string{
		"calls wrapper":        `<|DSML|calls>` + inner + `</|DSML|calls>`,
		"tool call singular":   `<|DSML|tool_call>` + inner + `</|DSML|tool_call>`,
		"space in wrapper":     `<|DSML|tool calls>` + inner + `</|DSML|tool calls>`,
		"double ascii bars":    `<||DSML||tool_calls>` + inner + `</||DSML||tool_calls>`,
		"spaces after bars":    `<| DSML | tool_calls >` + inner + `</| DSML | tool_calls >`,
		"upper case":           `<|DSML|TOOL_CALLS>` + inner + `</|DSML|TOOL_CALLS>`,
		"no wrapper at all":    inner,
		"no wrapper, 2 bars":   strings.NewReplacer("|", "｜｜").Replace(inner),
		"text before, no wrap": "Checking.\n" + inner,
	}
	for name, in := range cases {
		text, calls, _ := Parse(in)
		if len(calls) != 1 || calls[0].Name != "shell" || calls[0].Args["command"] != "ls" {
			t.Errorf("%s: calls = %#v", name, calls)
		}
		if strings.Contains(text, "DSML") || strings.Contains(text, "invoke") {
			t.Errorf("%s: markup leaked: %q", name, text)
		}
	}
}

func TestBareInvokeKeepsTheTextBefore(t *testing.T) {
	text, calls, _ := Parse("Let me check.\n" + `<|DSML|invoke name="a"></|DSML|invoke>`)
	if text != "Let me check.\n" || len(calls) != 1 {
		t.Errorf("text = %q calls = %#v", text, calls)
	}
}

func TestByteOrderMarkIsDropped(t *testing.T) {
	text, _, _ := Parse("\ufeffhello")
	if text != "hello" {
		t.Errorf("text = %q", text)
	}
}
