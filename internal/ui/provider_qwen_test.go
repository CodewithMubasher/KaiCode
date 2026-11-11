package ui

import "testing"

func TestQwenIsARememberedProviderChoice(t *testing.T) {
	a := parseActive("qwen")
	if a.Kind != "qwen" || a.String() != "qwen" {
		t.Errorf("parseActive(qwen) = %+v (%q)", a, a.String())
	}
	if got := parseActive("web"); got.Kind != "web" {
		t.Errorf("parseActive(web) = %+v", got)
	}
	if got := parseActive("nonsense"); got.Kind != "web" {
		t.Errorf("an unknown value must fall back to DeepSeek Web, got %+v", got)
	}
}
