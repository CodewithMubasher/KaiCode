package tools

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	defaultShellTimeout = 60 * time.Second
	maxShellTimeout     = 10 * time.Minute

	// The model gets the start and the end of long output (errors are
	// usually at the end), at most shellHead+shellTail bytes in total.
	shellHead = 12 << 10
	shellTail = 16 << 10
)

// Environment that keeps programs from waiting for a person.
var shellEnv = []string{
	"CI=1", "TERM=dumb", "NO_COLOR=1",
	"PAGER=cat", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never",
}

type shellFlavor int

const (
	flavorPosix shellFlavor = iota
	flavorPowerShell
	flavorCmd
)

type shellKind struct {
	flavor shellFlavor
	path   string
}

var (
	shellOnce sync.Once
	shellInfo shellKind
)

// resolveShell picks the shell once: PowerShell (or cmd.exe) on Windows, bash
// (or sh) elsewhere. KAI_SHELL overrides it with the name or path of a shell.
func resolveShell() shellKind {
	shellOnce.Do(func() { shellInfo = findShell() })
	return shellInfo
}

func flavorOf(path string) shellFlavor {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(base, ".exe")
	switch {
	case base == "pwsh" || base == "powershell":
		return flavorPowerShell
	case base == "cmd":
		return flavorCmd
	}
	return flavorPosix
}

func findShell() shellKind {
	if v := strings.TrimSpace(os.Getenv("KAI_SHELL")); v != "" {
		if p, err := exec.LookPath(v); err == nil {
			return shellKind{flavor: flavorOf(p), path: p}
		}
	}
	if runtime.GOOS == "windows" {
		for _, n := range []string{"pwsh", "powershell"} {
			if p, err := exec.LookPath(n); err == nil {
				return shellKind{flavor: flavorPowerShell, path: p}
			}
		}
		p := os.Getenv("ComSpec")
		if p == "" {
			p = "cmd.exe"
		}
		return shellKind{flavor: flavorCmd, path: p}
	}
	for _, n := range []string{"bash", "sh"} {
		if p, err := exec.LookPath(n); err == nil {
			return shellKind{flavor: flavorPosix, path: p}
		}
	}
	return shellKind{flavor: flavorPosix, path: "/bin/sh"}
}

// psPrelude makes PowerShell print UTF-8 and stay quiet about progress bars.
// Setting the console encoding can fail when there is no console, hence try.
const psPrelude = `$ErrorActionPreference='Continue';$ProgressPreference='SilentlyContinue';` +
	`try{[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new($false)}catch{};` +
	`$OutputEncoding=[System.Text.UTF8Encoding]::new($false);`

// psArgs runs the command through -EncodedCommand (UTF-16LE base64), which
// avoids every quoting problem between Go, the Windows command line and
// PowerShell.
func psArgs(command string) []string {
	u := utf16.Encode([]rune(psPrelude + command))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b)}
}

func (k shellKind) args(command string) []string {
	switch k.flavor {
	case flavorPowerShell:
		return psArgs(command)
	case flavorCmd:
		return []string{"/D", "/S", "/C", command}
	}
	return []string{"-c", command}
}

// Shell runs a command and returns its output. It is the one tool for
// listing, searching, creating, moving and deleting files and folders, git,
// builds and tests.
type Shell struct {
	Dir string // working directory; empty means the process's
}

func (Shell) Spec() Spec {
	var where string
	switch resolveShell().flavor {
	case flavorPowerShell:
		where = "PowerShell: use PowerShell syntax (Get-ChildItem, Select-String, New-Item, Remove-Item, Copy-Item, Move-Item) and chain commands with ';' rather than '&&'"
	case flavorCmd:
		where = "Windows cmd.exe (dir, findstr, mkdir, rmdir /s /q, del, copy, move)"
	default:
		where = "a POSIX shell (ls, grep -rn, rg, find, mkdir -p, rm -r, cp, mv)"
	}
	return Spec{
		Name: "shell",
		Description: "Run a command in the working directory and return its combined output and exit code. " +
			"This is the one tool for exploring and changing the file system and for running programs: " +
			"list files, search text and file names (grep, find), create and delete files and folders, move and copy, " +
			"git, builds and tests. Commands run in " + where + ". " +
			"Every call starts fresh (nothing is remembered between calls, so cd and set variables inside the same command) " +
			"and has no keyboard input: never start interactive programs, servers or file watchers. " +
			"Long output is shortened to its start and end, so narrow it (grep, head, -First) rather than printing whole large files.",
		Params: []Param{
			{Name: "command", Type: "string", Description: "The command line to run.", Required: true,
				Aliases: []string{"cmd", "script", "bash", "sh", "input", "code", "cmdline", "commands"}, Example: "ls -la"},
			{Name: "timeout_seconds", Type: "integer", Description: "Optional. Seconds before the command is stopped (default 60, maximum 600).",
				Aliases: []string{"timeout", "timeout_secs"}, Example: "60"},
		},
	}
}

func (s Shell) Run(ctx context.Context, args map[string]any) (string, error) {
	command, _ := StringArg(args, "command")
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("parameter %q must be a non-empty string", "command")
	}
	if why := dangerous(command); why != "" {
		return "", fmt.Errorf("refused: %s. Name the exact folder or file instead", why)
	}

	timeout := defaultShellTimeout
	if n, ok := IntArg(args, "timeout_seconds"); ok && n > 0 {
		timeout = min(time.Duration(n)*time.Second, maxShellTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	k := resolveShell()
	cmd := exec.CommandContext(ctx, k.path, k.args(command)...)
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), shellEnv...)
	out := &capBuf{}
	cmd.Stdout = out
	cmd.Stderr = out
	setupProcess(cmd, k, command)
	// If a child process outlives the shell and keeps the output pipe open,
	// stop waiting for it instead of hanging.
	cmd.WaitDelay = 3 * time.Second

	err := cmd.Run()
	text := cleanOutput(out.String())

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return text, fmt.Errorf("timed out after %s and was stopped; the output above is partial. "+
			"Do not run servers, watchers or interactive programs; for a long build or test pass a larger timeout_seconds (maximum 600)", timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		return text, errors.New("cancelled")
	}

	code := 0
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case errors.Is(err, exec.ErrWaitDelay):
		text += "\n[a background process kept the output open and was ignored]"
	default:
		return "", fmt.Errorf("could not start the shell %q: %w", k.path, err)
	}

	if text == "" {
		text = "(no output)"
	}
	return fmt.Sprintf("%s\n[exit code: %d]", text, code), nil
}

// ---- output handling -----------------------------------------------------------

// capBuf keeps the first shellHead bytes and the last shellTail bytes of
// whatever is written, so a command that prints a lot cannot use up memory.
type capBuf struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	dropped int
}

func (b *capBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if room := shellHead - len(b.head); room > 0 {
		k := min(room, len(p))
		b.head = append(b.head, p[:k]...)
		p = p[k:]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		if len(b.tail) > 2*shellTail {
			cut := len(b.tail) - shellTail
			b.dropped += cut
			b.tail = append([]byte(nil), b.tail[cut:]...)
		}
	}
	return n, nil
}

func (b *capBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	tail, dropped := b.tail, b.dropped
	if len(tail) > shellTail {
		dropped += len(tail) - shellTail
		tail = tail[len(tail)-shellTail:]
	}
	s := string(b.head)
	if dropped > 0 {
		s += fmt.Sprintf("\n[... %d bytes omitted ...]\n", dropped)
	}
	return s + string(tail)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func cleanOutput(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, "\n ")
}

// ---- safety net -----------------------------------------------------------------

var protectedPaths = map[string]bool{
	"/": true, "/*": true, "~": true, "~/": true, "~/*": true,
	"$home": true, "$home/": true, "$home/*": true, "${home}": true,
	"/home": true, "/usr": true, "/etc": true, "/bin": true, "/sbin": true,
	"/var": true, "/boot": true, "/lib": true, "/opt": true, "/root": true,
	"/system": true, "/users": true, "/library": true,
	"%userprofile%": true, "$env:userprofile": true, "$env:systemroot": true,
}

var driveRootRe = regexp.MustCompile(`^[a-z]:([\\/](\*|windows|users|program files|program files \(x86\))?)?$`)
var segmentSplit = regexp.MustCompile(`[;&|\n]+`)

// dangerous returns why a command must not run, or "". It is a safety net
// against a model slip such as "rm -rf /", not a sandbox: a determined command
// can get around it, and the shell can still change anything the user can.
func dangerous(command string) string {
	lower := strings.ToLower(command)
	if strings.Contains(lower, ":(){") {
		return "fork bombs are not allowed"
	}
	for _, seg := range segmentSplit.Split(lower, -1) {
		f := strings.Fields(seg)
		for len(f) > 0 && (f[0] == "sudo" || f[0] == "doas" || f[0] == "&" || f[0] == "command") {
			f = f[1:]
		}
		if len(f) == 0 {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(f[0]), ".exe")
		var args []string
		for _, a := range f[1:] {
			args = append(args, strings.Trim(a, `"'`))
		}
		if strings.HasPrefix(name, "mkfs") {
			return "creating a file system (mkfs) is not allowed"
		}
		switch name {
		case "shutdown", "reboot", "halt", "poweroff", "stop-computer", "restart-computer", "diskpart":
			return name + " is not allowed"
		case "format":
			for _, a := range args {
				if driveRootRe.MatchString(a) {
					return "formatting a drive is not allowed"
				}
			}
		case "dd":
			for _, a := range args {
				if strings.HasPrefix(a, "of=/dev/") || strings.HasPrefix(a, `of=\\.\`) {
					return "writing to a raw device is not allowed"
				}
			}
		case "rm", "rmdir", "rd", "del", "erase", "remove-item", "ri":
			if !recursiveDelete(name, args) {
				continue
			}
			for _, a := range args {
				if isFlag(name, a) {
					continue
				}
				if protectedPaths[a] || protectedPaths[strings.TrimRight(a, `/\`)] || driveRootRe.MatchString(a) {
					return "recursively deleting " + a + " is not allowed"
				}
			}
		}
	}
	return ""
}

// isFlag tells an option (-rf, -Force, /s, /q) from a path. For the cmd-style
// commands "/x" is an option, but for rm a lone "/" is the root directory.
func isFlag(name, a string) bool {
	if strings.HasPrefix(a, "-") {
		return true
	}
	switch name {
	case "del", "erase", "rd", "rmdir":
		return strings.HasPrefix(a, "/") && len(a) <= 3 && len(a) > 1
	}
	return false
}

// recursiveDelete reports whether the arguments ask for a recursive delete.
func recursiveDelete(name string, args []string) bool {
	for _, a := range args {
		switch {
		case a == "--recursive", a == "/s", a == "-r", a == "-recurse", strings.HasPrefix(a, "-recu"):
			return true
		case name == "rm" && len(a) > 1 && a[0] == '-' && a[1] != '-' && len(a) <= 5 && strings.ContainsAny(a, "r"):
			return true
		}
	}
	return false
}
