package ui

import (
	"bytes"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"

	tea "charm.land/bubbletea/v2"
)

// copyToClipboard puts text on the system clipboard using the platform's own
// tool (clip, pbcopy, wl-copy/xclip/xsel). It is sent next to tea.SetClipboard
// (OSC 52), which covers terminals where no tool is installed, e.g. over SSH.
func copyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		_ = nativeCopy(text)
		return nil
	}
}

func nativeCopy(text string) error {
	var cmd *exec.Cmd
	var data []byte
	switch runtime.GOOS {
	case "windows":
		// clip.exe reads UTF-16LE when the input starts with a BOM.
		u := utf16.Encode([]rune(string(rune(0xFEFF)) + strings.ReplaceAll(text, "\n", "\r\n")))
		data = make([]byte, 0, len(u)*2)
		for _, c := range u {
			data = append(data, byte(c), byte(c>>8))
		}
		cmd = exec.Command("clip")
	case "darwin":
		data, cmd = []byte(text), exec.Command("pbcopy")
	default:
		data = []byte(text)
		switch {
		case have("wl-copy"):
			cmd = exec.Command("wl-copy")
		case have("xclip"):
			cmd = exec.Command("xclip", "-selection", "clipboard")
		case have("xsel"):
			cmd = exec.Command("xsel", "--clipboard", "--input")
		default:
			return errors.New("no clipboard tool found")
		}
	}
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

// pasteCmd reads the system clipboard and delivers it as a paste. Terminals
// normally send Ctrl+V as a bracketed paste (tea.PasteMsg) on their own; this
// covers the ones that pass the raw key through. If no tool can read the
// clipboard it asks the terminal (OSC 52) and the answer arrives as a
// tea.ClipboardMsg.
func pasteCmd() tea.Cmd {
	return func() tea.Msg {
		if s, err := nativePaste(); err == nil && s != "" {
			return tea.PasteMsg{Content: s}
		}
		return tea.ReadClipboard()
	}
}

func nativePaste() (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("powershell", "-NoProfile", "-Command",
			"[Console]::OutputEncoding=[Text.Encoding]::UTF8; Get-Clipboard -Raw")
	case "darwin":
		cmd = exec.Command("pbpaste")
	default:
		switch {
		case have("wl-paste"):
			cmd = exec.Command("wl-paste", "-n")
		case have("xclip"):
			cmd = exec.Command("xclip", "-selection", "clipboard", "-o")
		case have("xsel"):
			cmd = exec.Command("xsel", "--clipboard", "--output")
		default:
			return "", errors.New("no clipboard tool found")
		}
	}
	out, err := cmd.Output()
	return strings.TrimPrefix(string(out), string(rune(0xFEFF))), err
}

func have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
