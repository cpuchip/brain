// Package pipeline — external project scaffolding.
package pipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cpuchip/brain/internal/store"
)

// ScaffoldResult reports what happened during project initialization.
type ScaffoldResult struct {
	ProjectDir string `json:"project_dir"`
	GitInited  bool   `json:"git_inited"`
	GHCreated  bool   `json:"gh_created"`
	Error      string `json:"error,omitempty"`
}

// ScaffoldProject creates the directory structure, git repo, and optionally
// a GitHub remote for an external project. Only operates on projects with
// workspace_type = "external".
func (p *Pipeline) ScaffoldProject(project *store.Project) (*ScaffoldResult, error) {
	if project.WorkspaceType != "external" {
		return nil, fmt.Errorf("scaffold only applies to external projects (got %q)", project.WorkspaceType)
	}

	// Resolve project directory
	name := slugify(project.Name)
	wsPath := project.WorkspacePath
	if wsPath == "" {
		wsPath = filepath.Join("projects", name)
	}
	absDir := wsPath
	if !filepath.IsAbs(absDir) && p.workspace != "" {
		absDir = filepath.Join(p.workspace, absDir)
	}

	result := &ScaffoldResult{ProjectDir: absDir}

	// Check if already initialized
	if _, err := os.Stat(filepath.Join(absDir, ".git")); err == nil {
		return nil, fmt.Errorf("project directory already has .git: %s", absDir)
	}

	// Create directory structure
	dirs := []string{
		filepath.Join(".github"),
		filepath.Join(".spec", "proposals"),
		filepath.Join(".spec", "scratch"),
		filepath.Join(".spec", "memory"),
		filepath.Join("docs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(absDir, d), 0o755); err != nil {
			return result, fmt.Errorf("creating directory %s: %w", d, err)
		}
	}

	// Write README.md
	readme := fmt.Sprintf("# %s\n\n%s\n", project.Name, project.Description)
	if err := os.WriteFile(filepath.Join(absDir, "README.md"), []byte(readme), 0o644); err != nil {
		return result, fmt.Errorf("writing README.md: %w", err)
	}

	// Write thin copilot-instructions.md
	instructions := buildProjectInstructions(project)
	if err := os.WriteFile(filepath.Join(absDir, ".github", "copilot-instructions.md"), []byte(instructions), 0o644); err != nil {
		return result, fmt.Errorf("writing copilot-instructions.md: %w", err)
	}

	// git init
	cmd := exec.Command("git", "init")
	cmd.Dir = absDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return result, fmt.Errorf("git init: %w\n%s", err, string(out))
	}
	result.GitInited = true

	// Initial commit
	cmd = exec.Command("git", "add", "-A")
	cmd.Dir = absDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return result, fmt.Errorf("git add: %w\n%s", err, string(out))
	}
	cmd = exec.Command("git", "commit", "-m", "Initial scaffold")
	cmd.Dir = absDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return result, fmt.Errorf("git commit: %w\n%s", err, string(out))
	}

	// gh repo create (if github_repo is configured)
	if project.GithubRepo != "" {
		vis := "--private"
		if project.RepoVisibility == "public" {
			vis = "--public"
		}
		cmd = exec.Command("gh", "repo", "create", project.GithubRepo, vis, "--source=.", "--push")
		cmd.Dir = absDir
		if out, err := cmd.CombinedOutput(); err != nil {
			// Non-fatal — record the error but don't fail the whole scaffold
			result.Error = fmt.Sprintf("gh repo create: %s\n%s", err, string(out))
		} else {
			result.GHCreated = true
		}
	}

	// Update project's workspace_path if it was empty
	if project.WorkspacePath == "" {
		project.WorkspacePath = wsPath
		_ = p.store.DB().UpdateProject(project)
	}

	return result, nil
}

// buildProjectInstructions generates a thin copilot-instructions.md for an
// external project. Contains project identity and a reference back to the
// parent workspace for base governance.
func buildProjectInstructions(project *store.Project) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", project.Name))

	if project.Description != "" {
		sb.WriteString(project.Description + "\n\n")
	}

	sb.WriteString(`## Governance

This project inherits base governance (voice, covenant, core principles) from the scripture-study workspace. The pipeline injects those instructions at runtime — they are not duplicated here.

This file contains what is **unique** to this project: architecture decisions, tech stack, conventions, and patterns.

## Architecture

<!-- Add project-specific architecture decisions here -->

## Conventions

<!-- Add project-specific coding conventions here -->

## Tech Stack

<!-- Add project-specific tech stack details here -->
`)

	return sb.String()
}
