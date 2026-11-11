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
	head := fmt.Sprintf("Working directory: %s\nOperating system: %s\nThe shell tool runs commands in %s.\n\n", wd, runtime.GOOS, shell)
	return head +
		"Tools (use exactly these names and parameter names; every call must include all required parameters):\n" +
		"- " + Shell{}.Spec().Usage() + ": run a command; list, search, delete, move, git, builds and tests.\n" +
		"- " + ReadFile{}.Spec().Usage() + ": read a small text file.\n" +
		"- " + CreateFile{}.Spec().Usage() + ": create a new file, or fully rewrite one, with its complete content.\n" +
		"- " + EditFile{}.Spec().Usage() + ": change part of an existing file by exact text replacement.\n\n" +
		"How to work:\n" +
		"- Use the shell tool for everything about the file system and running programs: list files, search text and names (grep, find), " +
		"delete, move and copy, git, builds and tests. Prefer one short command per call.\n" +
		"- To change an existing file use edit_file: copy old_string exactly from the file, with a few neighbouring lines so it is unique. " +
		"To make a new file use create_file. Use read_file to read a small file.\n" +
		"- Look before you change: inspect the files first, make small edits, then check the result (read it back, build or run the tests).\n" +
		"- Only change what the user asked for. Never delete or overwrite anything else. Do not start servers, watchers or interactive programs.\n" +
		"- Keep answers short and concrete.\n\n" +
		"When a tool call fails (the result starts with \"Error:\"):\n" +
		"- Read the whole error. It says what is wrong and shows the correct usage and an example.\n" +
		"- Never send the same call again unchanged. First fix the cause: a missing or misspelled parameter, a path that does not exist, " +
		"an old_string that does not match the file.\n" +
		"- If edit_file cannot find old_string, read the file again and copy the text exactly. Do not guess.\n" +
		"- If a tool keeps failing, switch to another approach (for example the shell tool) or tell the user what is blocking you.\n" +
		"- Never invent tools or parameters. Only the ones listed above exist."
}
