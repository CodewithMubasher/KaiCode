package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kai/internal/tools"
)

// While the model streams an edit_file call, the UI must learn the file and
// the growing line count before the call is complete.
func TestEditFileReportsLiveProgress(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry()
	reg.Register(tools.EditFile{Dir: dir})

	reply := "Writing it.\n" +
		"<|DSML|tool_calls>\n" +
		"<|DSML|invoke name=\"edit_file\">\n" +
		"<|DSML|parameter name=\"path\" string=\"true\">index.html</|DSML|parameter>\n" +
		"<|DSML|parameter name=\"new_string\" string=\"true\">" +
		strings.Repeat("<div></div>\n", 20) +
		"</|DSML|parameter>\n</|DSML|invoke>\n</|DSML|tool_calls>"

	mp := &MockProvider{ChunkRunes: 9, Script: func(n int, _ Turn) string {
		if n == 0 {
			return reply
		}
		return "done"
	}}
	a := New(Config{Provider: mp, Tools: reg})

	evs := drain(t, a.Run(context.Background(), "build it"))

	var lines []int
	for _, e := range evs {
		if e.Kind != EvToolProgress {
			continue
		}
		p := e.Progress
		if p == nil || p.Name != "edit_file" || p.Path != "index.html" || !p.Creating {
			t.Fatalf("progress = %+v", p)
		}
		lines = append(lines, p.Lines)
	}
	if len(lines) < 2 {
		t.Fatalf("progress events = %v, want the count to arrive more than once", lines)
	}
	if last := lines[len(lines)-1]; last != 21 {
		t.Errorf("last line count = %d, want 21", last)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] < lines[i-1] {
			t.Errorf("line count went backwards: %v", lines)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		t.Errorf("the edit did not run: %v", err)
	}
}
