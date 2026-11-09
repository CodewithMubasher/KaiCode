package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func edit(t *testing.T, dir string, args map[string]any) (string, error) {
	t.Helper()
	return EditFile{Dir: dir}.Run(context.Background(), args)
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditReplacesOneOccurrence(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "a.go", "package a\n\nfunc A() int {\n\treturn 1\n}\n")
	out, err := edit(t, dir, map[string]any{"path": "a.go", "old_string": "return 1", "new_string": "return 2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "package a\n\nfunc A() int {\n\treturn 2\n}\n" {
		t.Errorf("file = %q", got)
	}
	if !strings.Contains(out, "line 4") || !strings.Contains(out, "- return 1") || !strings.Contains(out, "+ return 2") {
		t.Errorf("out = %q", out)
	}
}

func TestEditNeedsAUniqueMatch(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "a.txt", "x\ny\nx\n")
	_, err := edit(t, dir, map[string]any{"path": "a.txt", "old_string": "x", "new_string": "z"})
	if err == nil || !strings.Contains(err.Error(), "2 times") || !strings.Contains(err.Error(), "1, 3") {
		t.Fatalf("err = %v", err)
	}
	if read(t, p) != "x\ny\nx\n" {
		t.Error("file changed despite the error")
	}
	if _, err := edit(t, dir, map[string]any{"path": "a.txt", "old_string": "x", "new_string": "z", "replace_all": true}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "z\ny\nz\n" {
		t.Errorf("file = %q", got)
	}
	// replace_all also arrives as text when the model writes string="true"
	write(t, dir, "b.txt", "q q")
	if _, err := edit(t, dir, map[string]any{"path": "b.txt", "old_string": "q", "new_string": "r", "replace_all": "true"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "b.txt")); got != "r r" {
		t.Errorf("file = %q", got)
	}
}

func TestEditNotFoundExplainsWhy(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "func f() {\n\tif x {\n\t\treturn\n\t}\n}\n")

	_, err := edit(t, dir, map[string]any{"path": "a.go", "old_string": "if x {\nreturn\n}", "new_string": "y"})
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "indentation") {
		t.Errorf("indentation hint missing: %v", err)
	}
	_, err = edit(t, dir, map[string]any{"path": "a.go", "old_string": "if x {\n\t\tpanic()", "new_string": "y"})
	if err == nil || !strings.Contains(err.Error(), "first line matches line 2") {
		t.Errorf("first-line hint missing: %v", err)
	}
	_, err = edit(t, dir, map[string]any{"path": "a.go", "old_string": "nothing like it", "new_string": "y"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

func TestEditKeepsWindowsLineEndings(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "w.txt", "one\r\ntwo\r\nthree\r\n")
	// the model writes LF
	if _, err := edit(t, dir, map[string]any{"path": "w.txt", "old_string": "one\ntwo", "new_string": "uno\ndos\ndos-bis"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "uno\r\ndos\r\ndos-bis\r\nthree\r\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEditLeavesLFFilesAlone(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "u.txt", "a\nb\n")
	edit(t, dir, map[string]any{"path": "u.txt", "old_string": "a", "new_string": "c\nd"})
	if got := read(t, p); got != "c\nd\nb\n" || strings.Contains(got, "\r") {
		t.Errorf("file = %q", got)
	}
}

func TestEditKeepsTheBOM(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "bom.txt", "\xEF\xBB\xBFhello world")
	if _, err := edit(t, dir, map[string]any{"path": "bom.txt", "old_string": "world", "new_string": "there"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "\xEF\xBB\xBFhello there" {
		t.Errorf("file = %q", got)
	}
}

func TestEditToleratesNewlinesAroundTheParameter(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "n.txt", "alpha\nbeta\ngamma\n")
	// <parameter ...>\nbeta\n</parameter> gives "\nbeta\n"
	if _, err := edit(t, dir, map[string]any{"path": "n.txt", "old_string": "\nbeta\n", "new_string": "\nBETA\n"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "alpha\nBETA\ngamma\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEditCanDeleteText(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "d.txt", "keep drop keep")
	if _, err := edit(t, dir, map[string]any{"path": "d.txt", "old_string": " drop", "new_string": ""}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "keep keep" {
		t.Errorf("file = %q", got)
	}
}

func TestEditCreatesFilesAndFolders(t *testing.T) {
	dir := t.TempDir()
	out, err := edit(t, dir, map[string]any{"path": "pkg/new/file.go", "old_string": "", "new_string": "package new\n"})
	if err != nil || !strings.Contains(out, "Created") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if got := read(t, filepath.Join(dir, "pkg", "new", "file.go")); got != "package new\n" {
		t.Errorf("file = %q", got)
	}
	// an existing, non-empty file is not overwritten
	_, err = edit(t, dir, map[string]any{"path": "pkg/new/file.go", "old_string": "", "new_string": "x"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}
	// without old_string at all (omitted parameter) it is also a create
	if _, err := edit(t, dir, map[string]any{"path": "second.txt", "new_string": "hi"}); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestEditRefusesBadTargets(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	write(t, dir, "bin.dat", "ab\x00cd")
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"path": "missing.txt", "old_string": "a", "new_string": "b"}, "not found"},
		{map[string]any{"path": "sub", "old_string": "a", "new_string": "b"}, "directory"},
		{map[string]any{"path": "bin.dat", "old_string": "a", "new_string": "b"}, "binary"},
		{map[string]any{"path": "bin.dat", "old_string": "a", "new_string": "a"}, "identical"},
		{map[string]any{"path": "", "old_string": "a", "new_string": "b"}, "path"},
		{map[string]any{"path": "x.txt", "old_string": "a"}, "new_string"},
	}
	for _, c := range cases {
		if _, err := edit(t, dir, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.args, err, c.want)
		}
	}
}

func TestEditKeepsFileMode(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("file modes are not meaningful on Windows")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "run.sh")
	os.WriteFile(p, []byte("echo a\n"), 0o755)
	edit(t, dir, map[string]any{"path": "run.sh", "old_string": "a", "new_string": "b"})
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestEditLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "a")
	edit(t, dir, map[string]any{"path": "a.txt", "old_string": "a", "new_string": "b"})
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want 1", len(entries))
	}
}

func TestEditThroughTheRegistry(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "one")
	r := NewRegistry()
	r.Register(EditFile{Dir: dir})
	res := r.Execute(context.Background(), Call{Name: "edit_file", Args: map[string]any{"path": "a.txt", "old_string": "one", "new_string": "two"}}, 0)
	if res.IsError || !strings.Contains(res.Output, "Edited") {
		t.Errorf("result = %+v", res)
	}
	res = r.Execute(context.Background(), Call{Name: "edit_file", Args: map[string]any{"path": "a.txt"}}, 0)
	if !res.IsError || !strings.Contains(res.Output, "new_string") {
		t.Errorf("missing parameter result = %+v", res)
	}
}
