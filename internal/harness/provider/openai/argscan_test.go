package openai

import (
	"strings"
	"testing"

	"kai/internal/harness"
)

func scanAll(s string, step int) (harness.ToolProgress, bool) {
	var sc argScanner
	var pr harness.ToolProgress
	var ok bool
	for i := 0; i < len(s); i += step {
		end := min(i+step, len(s))
		pr, ok = sc.Feed(s[i:end])
	}
	return pr, ok
}

func TestArgScanner(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want harness.ToolProgress
		ok   bool
	}{
		{"create", `{"path":"a.go","old_string":"","new_string":"one\ntwo\nthree"}`,
			harness.ToolProgress{Name: "edit_file", Path: "a.go", Lines: 3, Creating: true}, true},
		{"create without old_string", `{"path":"a.go","new_string":"x"}`,
			harness.ToolProgress{Name: "edit_file", Path: "a.go", Lines: 1, Creating: true}, true},
		{"replace", `{"path":"a.go","old_string":"foo","new_string":"bar\nbaz"}`,
			harness.ToolProgress{Name: "edit_file", Path: "a.go", Lines: 2}, true},
		{"new_string before path", `{"new_string":"a\nb","path":"x/y.txt"}`,
			harness.ToolProgress{Name: "edit_file", Path: "x/y.txt", Lines: 2, Creating: true}, true},
		{"escapes", `{"path":"caf\u00e9/\"q\".txt","new_string":"a\\nb\nc"}`,
			harness.ToolProgress{Name: "edit_file", Path: `café/"q".txt`, Lines: 2, Creating: true}, true},
		{"literal backslash-n is not a newline", `{"path":"p","new_string":"C:\\new"}`,
			harness.ToolProgress{Name: "edit_file", Path: "p", Lines: 1, Creating: true}, true},
		{"other values are skipped", `{"replace_all":true,"extra":{"a":["}","\"x"]},"path":"p","new_string":"z"}`,
			harness.ToolProgress{Name: "edit_file", Path: "p", Lines: 1, Creating: true}, true},
		{"empty new_string", `{"path":"p","new_string":""}`,
			harness.ToolProgress{Name: "edit_file", Path: "p", Creating: true}, true},
		{"old_string of only whitespace creates", `{"path":"p","old_string":" \n","new_string":"x"}`,
			harness.ToolProgress{Name: "edit_file", Path: "p", Lines: 1, Creating: true}, true},
		{"path still arriving", `{"path":"a.g`, harness.ToolProgress{}, false},
		{"not an object", `["path"]`, harness.ToolProgress{}, false},
	}
	for _, c := range cases {
		// Whole, in small pieces and byte by byte must all agree.
		for _, step := range []int{len(c.in), 7, 1} {
			got, ok := scanAll(c.in, step)
			if ok != c.ok || got != c.want {
				t.Errorf("%s (step %d): got %+v ok=%v, want %+v ok=%v", c.name, step, got, ok, c.want, c.ok)
			}
		}
	}
}

// A long file must cost time proportional to its size, not to its size times
// the number of fragments.
func TestArgScannerIsLinear(t *testing.T) {
	body := strings.Repeat("line of code\\n", 200000)
	in := `{"path":"big.txt","old_string":"","new_string":"` + body + `"}`
	pr, ok := scanAll(in, 16)
	if !ok || pr.Lines != 200001 {
		t.Fatalf("got %+v ok=%v", pr, ok)
	}
}
