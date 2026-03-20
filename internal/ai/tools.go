package ai

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
)

// devTools returns the Go-implemented tools that give the agent filesystem
// access for spec execution. All paths are sandboxed under allowedRoots.
func devTools(allowedRoots []string) []copilot.Tool {
	validate := func(path string) (string, error) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("invalid path: %w", err)
		}
		for _, root := range allowedRoots {
			rootAbs, _ := filepath.Abs(root)
			if strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) || abs == rootAbs {
				return abs, nil
			}
		}
		return "", fmt.Errorf("path %q is outside allowed directories", path)
	}

	return []copilot.Tool{
		copilot.DefineTool("read_file", "Read the contents of a file. Returns the full file content as text.",
			func(params struct {
				Path string `json:"path" jsonschema:"Absolute path to the file to read"`
			}, _ copilot.ToolInvocation) (string, error) {
				abs, err := validate(params.Path)
				if err != nil {
					return "", err
				}
				data, err := os.ReadFile(abs)
				if err != nil {
					return "", fmt.Errorf("reading file: %w", err)
				}
				return string(data), nil
			}),

		copilot.DefineTool("write_file", "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Creates parent directories as needed.",
			func(params struct {
				Path    string `json:"path" jsonschema:"Absolute path to the file to write"`
				Content string `json:"content" jsonschema:"The complete file content to write"`
			}, _ copilot.ToolInvocation) (string, error) {
				abs, err := validate(params.Path)
				if err != nil {
					return "", err
				}
				dir := filepath.Dir(abs)
				if err := os.MkdirAll(dir, 0755); err != nil {
					return "", fmt.Errorf("creating directory: %w", err)
				}
				if err := os.WriteFile(abs, []byte(params.Content), 0644); err != nil {
					return "", fmt.Errorf("writing file: %w", err)
				}
				return fmt.Sprintf("wrote %d bytes to %s", len(params.Content), abs), nil
			}),

		copilot.DefineTool("list_directory", "List files and directories in a directory. Returns names, one per line. Directories end with /.",
			func(params struct {
				Path string `json:"path" jsonschema:"Absolute path to the directory to list"`
			}, _ copilot.ToolInvocation) (string, error) {
				abs, err := validate(params.Path)
				if err != nil {
					return "", err
				}
				entries, err := os.ReadDir(abs)
				if err != nil {
					return "", fmt.Errorf("listing directory: %w", err)
				}
				var b strings.Builder
				for _, e := range entries {
					name := e.Name()
					if e.IsDir() {
						name += "/"
					}
					b.WriteString(name)
					b.WriteByte('\n')
				}
				return b.String(), nil
			}),

		copilot.DefineTool("search_text", "Search for a text pattern in files under a directory. Returns matching lines with file paths and line numbers. Case-insensitive.",
			func(params struct {
				Query      string `json:"query" jsonschema:"Text to search for (case-insensitive substring match)"`
				Directory  string `json:"directory" jsonschema:"Absolute path to the directory to search in"`
				MaxResults int    `json:"max_results,omitempty" jsonschema:"Maximum number of results to return (default 50)"`
			}, _ copilot.ToolInvocation) (string, error) {
				abs, err := validate(params.Directory)
				if err != nil {
					return "", err
				}
				maxResults := params.MaxResults
				if maxResults <= 0 {
					maxResults = 50
				}
				queryLower := strings.ToLower(params.Query)
				var b strings.Builder
				count := 0
				err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return err
					}
					if count >= maxResults {
						return filepath.SkipAll
					}
					// Skip binary/large files
					ext := strings.ToLower(filepath.Ext(path))
					switch ext {
					case ".exe", ".dll", ".so", ".dylib", ".db", ".sqlite", ".bin", ".gz", ".zip", ".tar", ".png", ".jpg", ".gif", ".woff", ".woff2", ".ttf":
						return nil
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return nil // skip unreadable files
					}
					lines := strings.Split(string(data), "\n")
					for i, line := range lines {
						if strings.Contains(strings.ToLower(line), queryLower) {
							fmt.Fprintf(&b, "%s:%d: %s\n", path, i+1, strings.TrimSpace(line))
							count++
							if count >= maxResults {
								return filepath.SkipAll
							}
						}
					}
					return nil
				})
				if err != nil && err != filepath.SkipAll {
					return b.String(), fmt.Errorf("search error: %w", err)
				}
				if count == 0 {
					return "no matches found", nil
				}
				return b.String(), nil
			}),
	}
}
