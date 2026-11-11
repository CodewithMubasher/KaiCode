package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// CreateFile writes the provided content to a file at the specified path.
// If the file already exists, it will be overwritten.
type CreateFile struct{}

func (CreateFile) Spec() Spec {
	return Spec{
		Name:        "create_file",
		Description: "Create a new file, or overwrite an existing one, with the complete content you give. Use it for new files and full rewrites; use edit_file to change part of an existing file.",
		Params: []Param{
			{Name: "path", Type: "string", Description: "Path of the file, relative to the working directory.", Required: true,
			Aliases: []string{"file_path", "filepath", "file", "filename", "file_name", "filePath"}, Example: "src/main.go"},
			{Name: "content", Type: "string", Description: "Complete content of the file.", Required: true,
				Aliases: []string{"contents", "text", "body", "file_content", "data", "new_string"}, Example: "package main\n"},
		},
	}
}

func (CreateFile) Run(ctx context.Context, args map[string]any) (string, error) {
	path, ok := StringArg(args, "path")
	if !ok || path == "" {
		return "", fmt.Errorf("parameter %q must be a non-empty string", "path")
	}

	content, ok := StringArg(args, "content")
	if !ok {
		return "", fmt.Errorf("parameter %q is required", "content")
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %q: %v", dir, err)
	}

	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("successfully created file %q", path), nil
}
