package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all brain configuration.
type Config struct {
	// Paths
	BrainDataDir string // Path to private-brain repo
	BrainCodeDir string // Path to this repo (scripts/brain)

	// AI
	GitHubToken string // GitHub PAT with models scope
	AIModel     string // e.g. "openai/gpt-4o-mini"
	AIEndpoint  string // GitHub Models endpoint

	// Discord
	DiscordToken     string // Discord bot token
	DiscordChannelID string // Optional: restrict to specific channel

	// Classification
	ConfidenceThreshold float64

	// Digest schedule
	Digest DigestConfig

	// Rate limits
	RateLimits RateLimitConfig
}

// DigestConfig controls proactive surfacing.
type DigestConfig struct {
	Morning struct {
		Enabled  bool   `yaml:"enabled"`
		Time     string `yaml:"time"`
		Timezone string `yaml:"timezone"`
	} `yaml:"morning"`
	Weekly struct {
		Enabled  bool   `yaml:"enabled"`
		Day      string `yaml:"day"`
		Time     string `yaml:"time"`
		Timezone string `yaml:"timezone"`
	} `yaml:"weekly"`
}

// RateLimitConfig prevents runaway behavior.
type RateLimitConfig struct {
	MaxAPICallsPerHour    int `yaml:"max_api_calls_per_hour"`
	MaxGitCommitsPerDay   int `yaml:"max_git_commits_per_day"`
	MaxNotificationsPerDay int `yaml:"max_notifications_per_day"`
}

// BrainConfig is the structure of .brain/config.yaml in the data repo.
type BrainConfig struct {
	Categories          map[string]CategoryConfig `yaml:"categories"`
	ConfidenceThreshold float64                   `yaml:"confidence_threshold"`
	Digest              DigestConfig              `yaml:"digest"`
	Limits              struct {
		DailyDigestWords  int `yaml:"daily_digest_words"`
		WeeklyReviewWords int `yaml:"weekly_review_words"`
	} `yaml:"limits"`
	Git struct {
		AutoCommit   bool   `yaml:"auto_commit"`
		CommitPrefix string `yaml:"commit_prefix"`
	} `yaml:"git"`
}

// CategoryConfig defines a classification bucket.
type CategoryConfig struct {
	Description string   `yaml:"description"`
	Directory   string   `yaml:"directory"`
	Fields      []string `yaml:"fields"`
}

// Load reads config from environment variables and the brain data config file.
func Load() (*Config, error) {
	cfg := &Config{
		AIEndpoint:          "https://models.github.ai/inference",
		AIModel:             "openai/gpt-4o-mini",
		ConfidenceThreshold: 0.6,
		RateLimits: RateLimitConfig{
			MaxAPICallsPerHour:    60,
			MaxGitCommitsPerDay:   100,
			MaxNotificationsPerDay: 20,
		},
	}

	// Required env vars
	cfg.GitHubToken = os.Getenv("GITHUB_TOKEN")
	cfg.DiscordToken = os.Getenv("DISCORD_TOKEN")
	cfg.DiscordChannelID = os.Getenv("DISCORD_CHANNEL_ID")

	// Optional env vars with defaults
	if v := os.Getenv("AI_MODEL"); v != "" {
		cfg.AIModel = v
	}
	if v := os.Getenv("AI_ENDPOINT"); v != "" {
		cfg.AIEndpoint = v
	}
	if v := os.Getenv("BRAIN_DATA_DIR"); v != "" {
		cfg.BrainDataDir = v
	}

	// Resolve brain data dir — default to ../../private-brain relative to this binary,
	// or check common locations
	if cfg.BrainDataDir == "" {
		cfg.BrainDataDir = findBrainDataDir()
	}

	if cfg.BrainDataDir == "" {
		return nil, fmt.Errorf("BRAIN_DATA_DIR not set and could not find private-brain directory")
	}

	// Load .brain/config.yaml from data dir
	brainCfgPath := filepath.Join(cfg.BrainDataDir, ".brain", "config.yaml")
	if err := loadBrainConfig(brainCfgPath, cfg); err != nil {
		// Not fatal — we have defaults
		fmt.Fprintf(os.Stderr, "warning: could not load %s: %v\n", brainCfgPath, err)
	}

	return cfg, nil
}

// Validate checks that required configuration is present.
func (c *Config) Validate() error {
	if c.GitHubToken == "" {
		return fmt.Errorf("GITHUB_TOKEN is required (PAT with 'models' scope)")
	}
	if c.DiscordToken == "" {
		return fmt.Errorf("DISCORD_TOKEN is required")
	}
	if c.BrainDataDir == "" {
		return fmt.Errorf("brain data directory not configured")
	}
	if _, err := os.Stat(c.BrainDataDir); os.IsNotExist(err) {
		return fmt.Errorf("brain data directory does not exist: %s", c.BrainDataDir)
	}
	return nil
}

// MorningDigestTime returns the parsed morning digest time for today.
func (c *Config) MorningDigestTime() (time.Time, error) {
	tz := c.Digest.Morning.Timezone
	if tz == "" {
		tz = "America/Denver"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, err
	}
	t, err := time.ParseInLocation("15:04", c.Digest.Morning.Time, loc)
	if err != nil {
		return time.Time{}, err
	}
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc), nil
}

func loadBrainConfig(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var bc BrainConfig
	if err := yaml.Unmarshal(data, &bc); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	if bc.ConfidenceThreshold > 0 {
		cfg.ConfidenceThreshold = bc.ConfidenceThreshold
	}
	cfg.Digest = bc.Digest
	return nil
}

func findBrainDataDir() string {
	// Check common locations relative to working directory
	candidates := []string{
		"private-brain",
		"../private-brain",
		"../../private-brain",
		filepath.Join(os.Getenv("USERPROFILE"), "Documents", "code", "stuffleberry", "scripture-study", "private-brain"),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return abs
		}
	}
	return ""
}
