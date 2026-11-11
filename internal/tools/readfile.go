package tools

import (
	"context"
	"fmt"
	"os"
)

// ReadFile returns the contents of a text file.
type ReadFile struct{}

func (ReadFile) Spec() Spec {
	return Spec{
		Name:        "read_file",
		Description: "Read a text file and return its contents.",
		Params: []Param{
			{Name: "path", Type: "string", Description: "Path of the file, relative to the working directory.", Required: true,
			Aliases: []string{"file_path", "filepath", "file", "filename", "file_name", "filePath"}, Example: "src/main.go"},
		},
	}
}

func (ReadFile) Run(ctx context.Context, args map[string]any) (string, error) {
	path, ok := StringArg(args, "path")
	if !ok || path == "" {
		return "", fmt.Errorf("parameter %q must be a non-empty string", "path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
