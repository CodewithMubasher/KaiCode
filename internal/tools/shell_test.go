package tools

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// sh picks the command for the shell this machine uses.
func sh(posix, powershell string) string {
	if resolveShell().flavor == flavorPowerShell {
		return powershell
	}
	return posix
}

func runShell(t *testing.T, dir, command string, extra map[string]any) (string, error) {
	t.Helper()
	args := map[string]any{"command": command}
	for k, v := range extra {
		args[k] = v
	}
	return Shell{Dir: dir}.Run(context.Background(), args)
}

func TestShellRunsAndReportsExitCode(t *testing.T) {
	out, err := runShell(t, "", "echo hello", nil)
	if err != nil || !strings.Contains(out, "hello") || !strings.HasSuffix(out, "[exit code: 0]") {
		t.Errorf("out = %q, err = %v", out, err)
	}
}

func TestShellNonZeroExitIsNotAToolError(t *testing.T) {
	out, err := runShell(t, "", "exit 3", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "[exit code: 3]") || !strings.Contains(out, "(no output)") {
		t.Errorf("out = %q", out)
	}
}

func TestShellMergesStderr(t *testing.T) {
	out, _ := runShell(t, "", sh("echo out; echo err 1>&2", "'out'; [Console]::Error.WriteLine('err')"), nil)
	if !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Errorf("out = %q", out)
	}
}

func TestShellUsesTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("x"), 0o644)
	out, _ := runShell(t, dir, sh("ls", "Get-ChildItem -Name"), nil)
	if !strings.Contains(out, "marker.txt") {
		t.Errorf("out = %q", out)
	}
}

func TestShellCreatesAndDeletesFolders(t *testing.T) {
	dir := t.TempDir()
	if _, err := runShell(t, dir, sh("mkdir -p a/b && echo hi > a/b/f.txt", "New-Item -ItemType Directory -Force a/b | Out-Null; 'hi' | Set-Content a/b/f.txt"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b", "f.txt")); err != nil {
		t.Fatalf("file was not created: %v", err)
	}
	if _, err := runShell(t, dir, sh("rm -rf a", "Remove-Item -Recurse -Force a"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Errorf("folder still there: %v", err)
	}
}

func TestShellTimeoutStopsTheCommand(t *testing.T) {
	start := time.Now()
	out, err := runShell(t, "", sh("echo started; sleep 30", "'started'; Start-Sleep 30"), map[string]any{"timeout_seconds": float64(1)})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "started") {
		t.Errorf("partial output lost: %q", out)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %s: the command was not stopped", time.Since(start))
	}
}

func TestShellTimeoutKillsChildrenThatHoldThePipe(t *testing.T) {
	if resolveShell().flavor != flavorPosix {
		t.Skip("process-group kill is checked on POSIX shells")
	}
	start := time.Now()
	_, err := runShell(t, "", "sleep 30 & sleep 30", map[string]any{"timeout_seconds": "1"}) // string number works too
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %s: a child kept the call hanging", time.Since(start))
	}
}

func TestShellCancelStopsTheCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := Shell{}.Run(ctx, map[string]any{"command": sh("sleep 30", "Start-Sleep 30")})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestShellKeepsHeadAndTailOfLongOutput(t *testing.T) {
	out, err := runShell(t, "", sh("echo START; head -c 200000 /dev/zero | tr '\\0' x; echo; echo END", "'START'; 'x' * 200000; 'END'"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "START") || !strings.Contains(out, "END") || !strings.Contains(out, "bytes omitted") {
		t.Errorf("head/tail lost: len=%d", len(out))
	}
	if len(out) > MaxOutput {
		t.Errorf("output is %d bytes, more than the %d the registry would cut", len(out), MaxOutput)
	}
}

func TestShellRejectsEmptyCommand(t *testing.T) {
	if _, err := runShell(t, "", "   ", nil); err == nil {
		t.Error("expected an error")
	}
}

func TestShellHasNoStdin(t *testing.T) {
	// A program that reads input must see end-of-file at once, not hang.
	start := time.Now()
	runShell(t, "", sh("cat", "[Console]::In.ReadToEnd()"), nil)
	if time.Since(start) > 10*time.Second {
		t.Errorf("a program waiting for input hung for %s", time.Since(start))
	}
}

func TestDangerousCommandsAreRefused(t *testing.T) {
	bad := []string{
		"rm -rf /", "rm -rf /*", "sudo rm -rf ~", `rm -fr "$HOME"`, "echo hi; rm -rf /",
		"rm -r -f /home", "rm --recursive --force /",
		`Remove-Item -Recurse -Force C:\`, `Remove-Item C:\ -Recurse`, `rd /s /q C:\`, `rmdir /s /q c:\windows`, `del /s /q c:\*`,
		"format c:", "dd if=/dev/zero of=/dev/sda", "mkfs.ext4 /dev/sda1", ":(){ :|:& };:", "shutdown -h now", "Restart-Computer",
	}
	for _, c := range bad {
		if dangerous(c) == "" {
			t.Errorf("not refused: %s", c)
		}
	}
	good := []string{
		"rm -rf build", "rm -rf ./node_modules", "rm -rf /tmp/kai-test", "rm file.txt", "ls /", "grep -rn foo /usr/include",
		`Remove-Item -Recurse -Force .\build`, `rd /s /q build`, "git rm -r --cached /tmp/x", "echo format c:",
		"rm -rf /home/user/project/dist", "dd if=a of=b.img",
	}
	for _, c := range good {
		if why := dangerous(c); why != "" {
			t.Errorf("wrongly refused %q: %s", c, why)
		}
	}
}

func TestRefusedCommandNeverRuns(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "keep.txt")
	os.WriteFile(marker, []byte("x"), 0o644)
	_, err := runShell(t, dir, "echo hi; rm -rf /", nil)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestPowerShellArgsEncodeTheCommand(t *testing.T) {
	args := psArgs("Get-ChildItem 'a b' | Select-String \"x\"")
	if args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("args = %v", args)
	}
	raw, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	got := string(utf16.Decode(u))
	if !strings.HasSuffix(got, "Get-ChildItem 'a b' | Select-String \"x\"") || !strings.Contains(got, "UTF8Encoding") {
		t.Errorf("decoded = %q", got)
	}
}

func TestShellSpecMentionsTheShell(t *testing.T) {
	s := Shell{}.Spec()
	if s.Name != "shell" || !strings.Contains(s.Description, "grep") && !strings.Contains(s.Description, "Select-String") {
		t.Errorf("spec = %+v", s)
	}
	if !strings.Contains(s.JSON(), `"command"`) || !strings.Contains(s.JSON(), `"integer"`) {
		t.Errorf("json = %s", s.JSON())
	}
}

func TestShellThroughTheRegistry(t *testing.T) {
	r := NewRegistry()
	r.Register(Shell{})
	res := r.Execute(context.Background(), Call{Name: "shell", Args: map[string]any{"command": "echo via-registry"}}, 30*time.Second)
	if res.IsError || !strings.Contains(res.Output, "via-registry") {
		t.Errorf("result = %+v", res)
	}
	res = r.Execute(context.Background(), Call{Name: "shell", Args: map[string]any{}}, time.Second)
	if !res.IsError || !strings.Contains(res.Output, "command") {
		t.Errorf("missing command result = %+v", res)
	}
}

func TestSystemGuideNamesTheMachineAndTools(t *testing.T) {
	g := SystemGuide()
	for _, want := range []string{"Working directory:", "Operating system:", "shell tool", "edit_file", "read_file"} {
		if !strings.Contains(g, want) {
			t.Errorf("guide lacks %q:\n%s", want, g)
		}
	}
}
