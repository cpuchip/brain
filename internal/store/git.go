package store

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Git handles git operations on the brain data repo.
type Git struct {
	repoDir      string
	commitPrefix string
	autoCommit   bool

	// Rate limiting
	commitsToday int
	lastResetDay int
	maxCommits   int
}

// NewGit creates a new Git manager for the data repo.
func NewGit(repoDir, commitPrefix string, autoCommit bool, maxCommitsPerDay int) *Git {
	return &Git{
		repoDir:      repoDir,
		commitPrefix: commitPrefix,
		autoCommit:   autoCommit,
		maxCommits:   maxCommitsPerDay,
	}
}

// CommitFile stages and commits a single file.
func (g *Git) CommitFile(relPath, message string) error {
	if !g.autoCommit {
		return nil
	}

	if err := g.checkDayLimit(); err != nil {
		return err
	}

	// git add
	if err := g.run("add", relPath); err != nil {
		return fmt.Errorf("git add %s: %w", relPath, err)
	}

	// git commit
	fullMsg := g.commitPrefix + " " + message
	if err := g.run("commit", "-m", fullMsg, "--", relPath); err != nil {
		// If nothing to commit, that's fine
		if strings.Contains(err.Error(), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit: %w", err)
	}

	g.commitsToday++
	return nil
}

// CommitAll stages and commits all changes.
func (g *Git) CommitAll(message string) error {
	if !g.autoCommit {
		return nil
	}

	if err := g.checkDayLimit(); err != nil {
		return err
	}

	if err := g.run("add", "-A"); err != nil {
		return fmt.Errorf("git add -A: %w", err)
	}

	fullMsg := g.commitPrefix + " " + message
	if err := g.run("commit", "-m", fullMsg); err != nil {
		if strings.Contains(err.Error(), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit: %w", err)
	}

	g.commitsToday++
	return nil
}

// Push pushes commits to origin.
func (g *Git) Push() error {
	return g.run("push")
}

// Pull pulls latest from origin.
func (g *Git) Pull() error {
	return g.run("pull", "--rebase")
}

func (g *Git) run(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.repoDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, string(output))
	}
	return nil
}

func (g *Git) checkDayLimit() error {
	today := time.Now().YearDay()
	if today != g.lastResetDay {
		g.commitsToday = 0
		g.lastResetDay = today
	}
	if g.commitsToday >= g.maxCommits {
		return fmt.Errorf("daily git commit limit reached (%d/%d)", g.commitsToday, g.maxCommits)
	}
	return nil
}

// Status returns the current git status.
func (g *Git) Status() (string, error) {
	cmd := exec.Command("git", "status", "--short")
	cmd.Dir = g.repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	return string(output), nil
}

// RepoDir returns the path to the repository directory.
func (g *Git) RepoDir() string {
	return g.repoDir
}

// EnsureRepo checks that the directory is a valid git repo.
func (g *Git) EnsureRepo() error {
	gitDir := filepath.Join(g.repoDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return fmt.Errorf("%s is not a git repository", g.repoDir)
	}
	return nil
}
