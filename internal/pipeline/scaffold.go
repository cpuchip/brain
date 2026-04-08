// Package pipeline — project initialization (agent-driven with mechanical fallback).
package pipeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/store"
)

// InitResult reports what happened during project initialization.
type InitResult struct {
	Method       string   `json:"method"`        // "agent" or "mechanical"
	FilesCreated []string `json:"files_created"` // paths relative to project dir
	ProjectDir   string   `json:"project_dir,omitempty"`
	GitInited    bool     `json:"git_inited"`
	GHCreated    bool     `json:"gh_created"`
	Error        string   `json:"error,omitempty"`
}

// InitializeProject creates project files using an agent (preferred) or mechanical scaffold (fallback).
// For external projects: creates dirs, git init, writes governance + context files, commits.
// For integrated/subfolder projects: writes a project context file.
func (p *Pipeline) InitializeProject(project *store.Project) (*InitResult, error) {
	result := &InitResult{}

	wsType := project.WorkspaceType
	if wsType == "" {
		wsType = "integrated"
	}

	// Resolve working directory
	workDir := p.workspace
	if wsType == "external" {
		workDir = p.resolveExternalDir(project)
		result.ProjectDir = workDir
	} else if wsType == "subfolder" && project.WorkspacePath != "" {
		workDir = filepath.Join(p.workspace, project.WorkspacePath)
	}

	// For external projects: create directory structure + git init first (agent needs a directory)
	if wsType == "external" {
		if err := p.scaffoldExternalDirs(workDir); err != nil {
			return result, err
		}
	}

	// Try agent-driven initialization
	if p.pool != nil {
		agentResult, err := p.initializeWithAgent(project, workDir, wsType)
		if err != nil {
			log.Printf("Agent initialization failed for project %d (%s), falling back to mechanical: %v", project.ID, project.Name, err)
			// Fall through to mechanical
		} else {
			result.Method = "agent"
			result.FilesCreated = agentResult
			// For external: git add + commit + optional gh repo create
			if wsType == "external" {
				if err := p.gitCommitExternal(project, workDir, result); err != nil {
					result.Error = err.Error()
				}
			}
			p.updateWorkspacePath(project, wsType, workDir)
			return result, nil
		}
	}

	// Mechanical fallback
	files, err := p.mechanicalInit(project, workDir, wsType)
	if err != nil {
		return result, err
	}
	result.Method = "mechanical"
	result.FilesCreated = files

	// For external: git add + commit + optional gh repo create
	if wsType == "external" {
		if err := p.gitCommitExternal(project, workDir, result); err != nil {
			result.Error = err.Error()
		}
	}

	p.updateWorkspacePath(project, wsType, workDir)
	return result, nil
}

// initializeWithAgent runs a Sonnet agent to create meaningful project files.
func (p *Pipeline) initializeWithAgent(project *store.Project, workDir, wsType string) ([]string, error) {
	systemMsg := initSystemMessage()

	prompt := buildInitPrompt(project, wsType)

	agentCfg := ai.AgentConfig{
		Model:         config.PipelineSmartModel,
		SystemMessage: systemMsg,
		WorkingDir:    workDir,
		AgentName:     "init",
		AllowedWritePaths: map[string][]string{
			"init": {"."},
		},
		TokenWarningThreshold: 50000,
		PremiumRequestCost:    1.0, // Sonnet
	}

	agent := ai.NewAgent(p.pool.Client(), agentCfg)

	log.Printf("Agent initialization starting for project %d (%s) in %s", project.ID, project.Name, workDir)

	ctx, cancel := context.WithCancel(p.ctx)
	defer cancel()

	response, err := agent.Ask(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("init agent failed: %w", err)
	}

	log.Printf("Agent initialization complete for project %d (%d chars response)", project.ID, len(response))

	// Parse file list from response (best-effort — the files are already written by the agent)
	files := parseCreatedFiles(response)
	return files, nil
}

// mechanicalInit creates files without an agent — uses init_instructions as content.
func (p *Pipeline) mechanicalInit(project *store.Project, workDir, wsType string) ([]string, error) {
	var files []string

	if wsType == "external" {
		// Write README.md
		readme := fmt.Sprintf("# %s\n\n%s\n", project.Name, project.Description)
		readmePath := filepath.Join(workDir, "README.md")
		if err := os.WriteFile(readmePath, []byte(readme), 0o644); err != nil {
			return files, fmt.Errorf("writing README.md: %w", err)
		}
		files = append(files, "README.md")

		// Write copilot-instructions.md
		instructions := buildProjectInstructions(project)
		instrPath := filepath.Join(workDir, ".github", "copilot-instructions.md")
		if err := os.WriteFile(instrPath, []byte(instructions), 0o644); err != nil {
			return files, fmt.Errorf("writing copilot-instructions.md: %w", err)
		}
		files = append(files, ".github/copilot-instructions.md")
	} else {
		// Integrated/subfolder: write a context file
		contextPath := project.ContextFile
		if contextPath == "" {
			contextPath = fmt.Sprintf(".spec/context/%s.md", slugify(project.Name))
		}
		absPath := filepath.Join(workDir, contextPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return files, fmt.Errorf("creating context dir: %w", err)
		}
		content := buildContextFile(project)
		if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
			return files, fmt.Errorf("writing context file: %w", err)
		}
		files = append(files, contextPath)

		// Update project's context_file if it was empty
		if project.ContextFile == "" {
			project.ContextFile = contextPath
			_ = p.store.DB().UpdateProject(project)
		}
	}

	return files, nil
}

// scaffoldExternalDirs creates the base directory structure for an external project.
func (p *Pipeline) scaffoldExternalDirs(absDir string) error {
	dirs := []string{
		".github",
		filepath.Join(".spec", "proposals"),
		filepath.Join(".spec", "scratch"),
		filepath.Join(".spec", "memory"),
		"docs",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(absDir, d), 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}
	return nil
}

// gitCommitExternal runs git init, add, commit, and optional gh repo create.
func (p *Pipeline) gitCommitExternal(project *store.Project, absDir string, result *InitResult) error {
	// git init (skip if already inited)
	if _, err := os.Stat(filepath.Join(absDir, ".git")); os.IsNotExist(err) {
		cmd := exec.Command("git", "init")
		cmd.Dir = absDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %w\n%s", err, string(out))
		}
	}
	result.GitInited = true

	// git add + commit
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = absDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w\n%s", err, string(out))
	}
	cmd = exec.Command("git", "commit", "-m", "Initialize project")
	cmd.Dir = absDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, string(out))
	}

	// gh repo create (if configured)
	if project.GithubRepo != "" {
		vis := "--private"
		if project.RepoVisibility == "public" {
			vis = "--public"
		}
		cmd = exec.Command("gh", "repo", "create", project.GithubRepo, vis, "--source=.", "--push")
		cmd.Dir = absDir
		if out, err := cmd.CombinedOutput(); err != nil {
			// Non-fatal
			result.Error = fmt.Sprintf("gh repo create: %s\n%s", err, string(out))
		} else {
			result.GHCreated = true
		}
	}

	return nil
}

// resolveExternalDir determines the absolute path for an external project.
func (p *Pipeline) resolveExternalDir(project *store.Project) string {
	name := slugify(project.Name)
	wsPath := project.WorkspacePath
	if wsPath == "" {
		wsPath = filepath.Join("projects", name)
	}
	if filepath.IsAbs(wsPath) {
		return wsPath
	}
	if p.workspace != "" {
		return filepath.Join(p.workspace, wsPath)
	}
	return wsPath
}

// updateWorkspacePath sets the project's workspace_path if it was empty (external).
func (p *Pipeline) updateWorkspacePath(project *store.Project, wsType, workDir string) {
	if wsType == "external" && project.WorkspacePath == "" {
		relPath := workDir
		if p.workspace != "" {
			if rel, err := filepath.Rel(p.workspace, workDir); err == nil {
				relPath = rel
			}
		}
		project.WorkspacePath = relPath
		_ = p.store.DB().UpdateProject(project)
	}
}

// initSystemMessage returns the system prompt for the initialization agent.
func initSystemMessage() string {
	return `You are a project initialization agent. Your job is to create meaningful, project-specific files based on the instructions provided.

You will be given a project name, description, and initialization instructions describing the project's purpose, tech stack, and goals. You will also be told the workspace type.

Based on the workspace type, create appropriate files:

For EXTERNAL projects (standalone repo):
- .github/copilot-instructions.md — Project identity, architecture, tech stack, conventions. Write REAL content based on the instructions, not templates with placeholder comments.
- README.md — Clear project description with getting started section.
- Any other files the instructions suggest (e.g., docs/architecture.md).

For SUBFOLDER or INTEGRATED projects (part of parent workspace):
- Write a project context file (path specified in the prompt) that captures project purpose, conventions, and architecture for injection into agent prompts.

Guidelines:
- Write real content, not placeholders. If the instructions say "TypeScript monorepo with Next.js", write actual TypeScript conventions.
- Keep files concise but substantive — every sentence should be useful.
- Don't create files that aren't useful yet. A README and copilot-instructions.md are always useful. A CONTRIBUTING.md for a solo project is not.

After creating files, list what you created in your final response like:
FILES_CREATED:
- path/to/file1.md
- path/to/file2.md`
}

// buildInitPrompt builds the agent prompt from project details.
func buildInitPrompt(project *store.Project, wsType string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Initialize the project \"%s\".\n\n", project.Name))

	if project.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n\n", project.Description))
	}

	if project.InitInstructions != "" {
		sb.WriteString(fmt.Sprintf("Instructions:\n%s\n\n", project.InitInstructions))
	} else {
		sb.WriteString("No specific instructions provided — create sensible defaults based on the project name and description.\n\n")
	}

	sb.WriteString(fmt.Sprintf("Workspace type: %s\n", wsType))

	if wsType != "external" {
		contextPath := project.ContextFile
		if contextPath == "" {
			contextPath = fmt.Sprintf(".spec/context/%s.md", slugify(project.Name))
		}
		sb.WriteString(fmt.Sprintf("Write the project context to: %s\n", contextPath))
	}

	sb.WriteString("\nCreate the appropriate files for this project. Be specific to the instructions — this is not a generic template.")

	return sb.String()
}

// parseCreatedFiles extracts file paths from agent response.
func parseCreatedFiles(response string) []string {
	var files []string
	inList := false
	for _, line := range strings.Split(response, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "FILES_CREATED") {
			inList = true
			continue
		}
		if inList && strings.HasPrefix(trimmed, "- ") {
			file := strings.TrimPrefix(trimmed, "- ")
			file = strings.TrimSpace(file)
			if file != "" {
				files = append(files, file)
			}
		} else if inList && trimmed == "" {
			// End of list
			break
		}
	}
	return files
}

// buildProjectInstructions generates a copilot-instructions.md for mechanical scaffold.
// When init_instructions are available, uses them as the body. Otherwise uses placeholders.
func buildProjectInstructions(project *store.Project) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", project.Name))

	if project.Description != "" {
		sb.WriteString(project.Description + "\n\n")
	}

	sb.WriteString("## Governance\n\n")
	sb.WriteString("This project inherits base governance (voice, covenant, core principles) from the scripture-study workspace. The pipeline injects those instructions at runtime — they are not duplicated here.\n\n")
	sb.WriteString("This file contains what is **unique** to this project: architecture decisions, tech stack, conventions, and patterns.\n\n")

	if project.InitInstructions != "" {
		sb.WriteString("## Project Details\n\n")
		sb.WriteString(project.InitInstructions + "\n")
	} else {
		sb.WriteString("## Architecture\n\n<!-- Add project-specific architecture decisions here -->\n\n")
		sb.WriteString("## Conventions\n\n<!-- Add project-specific coding conventions here -->\n\n")
		sb.WriteString("## Tech Stack\n\n<!-- Add project-specific tech stack details here -->\n")
	}

	return sb.String()
}

// buildContextFile generates a project context doc for integrated/subfolder projects.
func buildContextFile(project *store.Project) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s — Project Context\n\n", project.Name))

	if project.Description != "" {
		sb.WriteString(project.Description + "\n\n")
	}

	if project.InitInstructions != "" {
		sb.WriteString(project.InitInstructions + "\n")
	} else {
		sb.WriteString("<!-- Add project-specific context for agent prompts here -->\n")
	}

	return sb.String()
}
