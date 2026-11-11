package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxEditSize = 5 << 20 // refuse to edit files bigger than 5 MB

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// EditFile changes a text file by exact text replacement, or creates a new one.
type EditFile struct {
	Dir string // relative paths are resolved against this; empty means the working directory
}

func (EditFile) Spec() Spec {
	return Spec{
		Name: "edit_file",
		Description: "Edit a text file by replacing an exact piece of text with new text. " +
			"old_string must match the file exactly, including indentation and line breaks, and must occur exactly once " +
			"(include a few neighbouring lines to make it unique, or set replace_all to change every occurrence). " +
			"To create a new file, leave old_string empty and put the whole content in new_string; the file must not exist yet. " +
			"Read the file first so old_string is copied from its real text. For renaming, moving, deleting or bulk rewrites use the shell tool.",
		Params: []Param{
			{Name: "path", Type: "string", Description: "Path of the file, relative to the working directory.", Required: true,
			Aliases: []string{"file_path", "filepath", "file", "filename", "file_name", "filePath"}, Example: "src/main.go"},
			{Name: "old_string", Type: "string", Description: "The exact text to replace. Empty to create a new file.",
				Aliases: []string{"old_str", "old_text", "old", "search", "find", "target"}, Example: "old text"},
			{Name: "new_string", Type: "string", Description: "The replacement text (empty to delete old_string).", Required: true,
				Aliases: []string{"new_str", "new_text", "new", "replace", "replacement", "replace_with", "content"}, Example: "new text"},
			{Name: "replace_all", Type: "boolean", Description: "Optional. Replace every occurrence of old_string instead of requiring exactly one."},
		},
	}
}

func (e EditFile) Run(ctx context.Context, args map[string]any) (string, error) {
	path, _ := StringArg(args, "path")
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("parameter %q must be a non-empty string", "path")
	}
	oldS, _ := StringArg(args, "old_string")
	newS, ok := StringArg(args, "new_string")
	if !ok {
		return "", fmt.Errorf("parameter %q must be a string (use an empty string to delete text)", "new_string")
	}
	replaceAll := BoolArg(args, "replace_all")

	full := path
	if !filepath.IsAbs(full) && e.Dir != "" {
		full = filepath.Join(e.Dir, full)
	}
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		full = resolved // edit the real file, not the link
	}

	info, statErr := os.Stat(full)
	if oldS == "" {
		return create(path, full, newS, info, statErr)
	}
	if oldS == newS {
		return "", errors.New("old_string and new_string are identical: nothing to change")
	}
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		return "", fmt.Errorf("file not found: %s (to create a new file, leave old_string empty)", path)
	case statErr != nil:
		return "", statErr
	case info.IsDir():
		return "", fmt.Errorf("%s is a directory, not a file", path)
	case info.Size() > maxEditSize:
		return "", fmt.Errorf("%s is %d bytes, too large to edit with this tool (limit %d): use the shell tool", path, info.Size(), maxEditSize)
	}

	raw, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", fmt.Errorf("%s looks like a binary file; refusing to edit it", path)
	}
	bom := bytes.HasPrefix(raw, utf8BOM)
	if bom {
		raw = raw[len(utf8BOM):]
	}
	content := string(raw)

	// Models write "\n"; Windows files often use "\r\n". Match on LF and put
	// the file's own line endings back when writing.
	crlf := strings.Contains(content, "\r\n") && !hasBareLF(content)
	if crlf {
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	oldS = strings.ReplaceAll(oldS, "\r\n", "\n")
	newS = strings.ReplaceAll(newS, "\r\n", "\n")

	count := strings.Count(content, oldS)
	if count == 0 {
		// A common slip: a newline right after the opening tag or before the
		// closing tag of the parameter.
		o2, n2 := trimOneNewline(oldS), trimOneNewline(newS)
		if o2 != "" && o2 != oldS && strings.Contains(content, o2) {
			oldS, newS = o2, n2
			count = strings.Count(content, oldS)
		}
	}
	if count == 0 {
		return "", errors.New(notFoundMessage(path, content, oldS))
	}
	if count > 1 && !replaceAll {
		return "", fmt.Errorf("old_string occurs %d times in %s (at lines %s): add neighbouring lines so it matches exactly once, or set replace_all to true",
			count, path, lineNumbers(content, oldS, 6))
	}

	firstLine := 1 + strings.Count(content[:strings.Index(content, oldS)], "\n")
	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldS, newS)
	} else {
		updated = strings.Replace(content, oldS, newS, 1)
	}
	replaced := 1
	if replaceAll {
		replaced = count
	}

	out := updated
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	data := []byte(out)
	if bom {
		data = append(append([]byte(nil), utf8BOM...), data...)
	}
	if err := writeFileAtomic(full, data, info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("could not write %s: %w", path, err)
	}

	return fmt.Sprintf("Edited %s: %d replacement(s), first at line %d.\n%s",
		path, replaced, firstLine, diffPreview(oldS, newS)), nil
}

func create(path, full, content string, info os.FileInfo, statErr error) (string, error) {
	switch {
	case statErr == nil && info.IsDir():
		return "", fmt.Errorf("%s is a directory, not a file", path)
	case statErr == nil && info.Size() > 0:
		return "", fmt.Errorf("%s already exists and is not empty: give old_string to change part of it, or use the shell tool to overwrite it", path)
	case statErr != nil && !errors.Is(statErr, os.ErrNotExist):
		return "", statErr
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(full, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("could not write %s: %w", path, err)
	}
	return fmt.Sprintf("Created %s (%d lines, %d bytes).", path, countLines(content), len(content)), nil
}

func hasBareLF(s string) bool {
	return strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n")
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func trimOneNewline(s string) string {
	s = strings.TrimPrefix(s, "\n")
	return strings.TrimSuffix(s, "\n")
}

// lineNumbers lists the 1-based lines where sub starts, at most max of them.
func lineNumbers(content, sub string, max int) string {
	var lines []string
	from := 0
	for len(lines) < max {
		i := strings.Index(content[from:], sub)
		if i < 0 {
			break
		}
		at := from + i
		lines = append(lines, fmt.Sprint(1+strings.Count(content[:at], "\n")))
		from = at + max1(len(sub))
	}
	return strings.Join(lines, ", ")
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// notFoundMessage explains why old_string was not found and, when it can,
// where the nearest match is, so the model fixes the call instead of guessing.
func notFoundMessage(path, content, oldS string) string {
	msg := fmt.Sprintf("old_string was not found in %s.", path)
	want := strings.Split(strings.Trim(oldS, "\n"), "\n")
	have := strings.Split(content, "\n")
	trim := func(s string) string { return strings.TrimSpace(s) }

	// Same lines, different indentation or trailing spaces?
	for i := 0; i+len(want) <= len(have); i++ {
		match := true
		for j := range want {
			if trim(have[i+j]) != trim(want[j]) {
				match = false
				break
			}
		}
		if match {
			return fmt.Sprintf("%s The same text exists at line %d but with different indentation or spacing. "+
				"Copy it exactly as it appears in the file (read_file, or the shell tool with a line-range command).", msg, i+1)
		}
	}
	// First line present, the rest differs?
	if first := trim(want[0]); first != "" {
		for i, l := range have {
			if trim(l) == first {
				return fmt.Sprintf("%s Its first line matches line %d, but the following lines differ. "+
					"Read that part of the file again and copy it exactly.", msg, i+1)
			}
		}
	}
	return msg + " Read the file again: it may have changed, or old_string may not be copied exactly."
}

// diffPreview shows what changed, a few lines each side.
func diffPreview(oldS, newS string) string {
	const maxLines = 12
	show := func(prefix, s string) string {
		if s == "" {
			return ""
		}
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		var b strings.Builder
		for i, l := range lines {
			if i == maxLines {
				fmt.Fprintf(&b, "%s... (%d more lines)\n", prefix, len(lines)-maxLines)
				break
			}
			b.WriteString(prefix + l + "\n")
		}
		return b.String()
	}
	return strings.TrimRight(show("- ", oldS)+show("+ ", newS), "\n")
}

// writeFileAtomic writes to a temporary file next to the target and renames it
// over the target, so a crash cannot leave half a file. If the rename is not
// possible (for example the file is open elsewhere on Windows) it writes in place.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".kai-edit-*")
	if err == nil {
		name := tmp.Name()
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr == nil && cerr == nil && os.Chmod(name, mode) == nil && os.Rename(name, path) == nil {
			return nil
		}
		os.Remove(name)
	}
	return os.WriteFile(path, data, mode)
}
