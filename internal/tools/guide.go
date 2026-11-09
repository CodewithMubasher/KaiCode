package tools

import (
	"fmt"
	"os"
	"runtime"
)

// SystemGuide is the part of Kai's instructions that describes the machine and
// how to use the tools. It is sent once, in the first message of a chat.
func SystemGuide() string {
	wd, _ := os.Getwd()
	shell := "a POSIX shell"
	switch resolveShell().flavor {
	case flavorPowerShell:
		shell = "PowerShell"
	case flavorCmd:
		shell = "cmd.exe"
	}
	return fmt.Sprintf("Working directory: %s\nOperating system: %s\nThe shell tool runs commands in %s.\n\n", wd, runtime.GOOS, shell) +
		"How to work:\n" +
		"- Use the shell tool for everything about the file system and running programs: list files, search text and names (grep, find), " +
		"create and delete files and folders, move and copy, git, builds and tests. Prefer one short command per call.\n" +
		"- Use edit_file to change a file. Copy old_string exactly from the file, with a few neighbouring lines so it is unique. " +
		"To create a new file, call edit_file with an empty old_string. Use read_file to read a small file.\n" +
		"- Look before you change: inspect the files first, make small edits, then check the result (read it back, build or run the tests).\n" +
		"- Only change what the user asked for. Never delete or overwrite anything else. Do not start servers, watchers or interactive programs.\n" +
		"- Keep answers short and concrete."
}
