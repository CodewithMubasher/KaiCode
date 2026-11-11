package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"kai/internal/harness"
	"kai/internal/store"
	"kai/internal/ui/components"
)

// A session saved, closed and reopened must show the same timeline and the
// same token count, with every file node collapsed until it is clicked.
func TestSessionStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kai.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(store.Session{ID: "s1", Title: "work", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	ps := newPersister(st, "cwd")
	ps.use("s1")
	m := Model{
		chat: components.NewChat(100, 40).
			AppendUser("go").
			StartReply().
			EditFile("x.go", false, []components.DiffLine{
				{Kind: components.DiffDel, Num: 4, Text: "old line"},
				{Kind: components.DiffAdd, Num: 4, Text: "new line"},
			}).
			StreamAnswer("done").
			EndAnswer(),
		st:  st,
		ps:  ps,
		run: runState{agent: harness.New(harness.Config{}), tokens: 4321},
	}
	m = m.saveState()
	_ = st.Close()

	// Reopen exactly as a new Kai process would.
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ps = newPersister(st, "cwd")
	fresh := Model{
		chat: components.NewChat(100, 40),
		st:   st,
		ps:   ps,
		run:  runState{agent: harness.New(harness.Config{})},
	}
	loaded, _ := fresh.loadSession("s1")
	if loaded.run.tokens != 4321 {
		t.Errorf("tokens after restart = %d, want 4321", loaded.run.tokens)
	}
	v := ansi.Strip(loaded.chat.View())
	if !strings.Contains(v, "Edited x.go") {
		t.Fatalf("the timeline node did not survive the restart:\n%s", v)
	}
	if strings.Contains(v, "new line") {
		t.Errorf("a restored node must be collapsed:\n%s", v)
	}
	if !strings.Contains(v, "done") {
		t.Errorf("the answer did not survive the restart:\n%s", v)
	}
}
