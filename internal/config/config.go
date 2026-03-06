package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// ModelPreset maps a friendly name to a Copilot SDK model identifier.
type ModelPreset struct {
	ID          string // Copilot model ID (e.g. "gpt-5-mini")
	DisplayName string // Human-friendly name
	PremiumRate string // Premium request multiplier (e.g. "0x", "1x")
}

// AvailableModels are the presets the user can switch between via Discord.
var AvailableModels = map[string]ModelPreset{
	"gpt-mini": {ID: "gpt-5-mini", DisplayName: "GPT-5 Mini", PremiumRate: "0x"},
	"haiku":    {ID: "claude-haiku-4.5", DisplayName: "Claude Haiku 4.5", PremiumRate: "0.33x"},
	"sonnet":   {ID: "claude-sonnet-4.6", DisplayName: "Claude Sonnet 4.6", PremiumRate: "1x"},
	"flash":    {ID: "gemini-3-flash", DisplayName: "Gemini 3 Flash", PremiumRate: "0.33x"},
	"gpt5":     {ID: "gpt-5", DisplayName: "GPT-5", PremiumRate: "1x"},
}

// Config holds all brain configuration.
type Config struct {
	// Paths
	BrainDataDir string // Path to data directory (for DB, vec, and optional archive)
	BrainCodeDir string // Path to this repo (scripts/brain)

	// Storage
	DBPath     string // SQLite database path (default: {BrainDataDir}/brain.db)
	VecDir     string // chromem-go persistence dir (default: {BrainDataDir}/vec)
	ArchiveDir string // optional private-brain repo for archive export

	// Embedding
	EmbeddingBackend string // "lmstudio", "ollama", "openai", or "none" (default: lmstudio)
	EmbeddingModel   string // Model name for embeddings (default: text-embedding-qwen3-embedding-4b)
	OllamaURL        string // Ollama API base URL (if using Ollama)

	// Web UI
	WebEnabled bool   // Enable web UI (default: true)
	WebPort    string // Web UI port (default: 8445)

	// AI
	AIBackend     string // "lmstudio" or "copilot" (default: lmstudio)
	GitHubToken   string // Optional: GitHub PAT (SDK can use logged-in Copilot user)
	AIModel       string // Current Copilot model ID (e.g. "gpt-5-mini")
	AIModelPreset string // Current preset name (e.g. "gpt-mini")

	// LM Studio
	LMStudioURL   string // LM Studio API base URL (default: http://localhost:1234/v1)
	LMStudioModel string // Model identifier loaded in LM Studio

	// Relay (ibeco.me WebSocket)
	RelayEnabled bool   // Enable relay transport
	RelayURL     string // WebSocket endpoint (e.g. "wss://ibeco.me/ws/brain")
	RelayToken   string // Bearer token (bec_...)

	// ibecome task sync (creates tasks from actions/projects)
	IbecomeURL      string // REST API base (e.g. "https://ibeco.me")
	IbecomeTaskSync bool   // Auto-create tasks in ibecome for actions/projects

	// Discord
	DiscordEnabled   bool   // Enable Discord transport
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
	MaxAPICallsPerHour     int `yaml:"max_api_calls_per_hour"`
	MaxGitCommitsPerDay    int `yaml:"max_git_commits_per_day"`
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
// It loads .env from the current directory if present.
func Load() (*Config, error) {
	// Load .env file (silently ignore if missing)
	_ = godotenv.Load()

	cfg := &Config{
		AIBackend:           "lmstudio", // default to local LM Studio
		AIModel:             AvailableModels["gpt-mini"].ID,
		AIModelPreset:       "gpt-mini",
		LMStudioURL:         "http://localhost:1234/v1",
		LMStudioModel:       "mistralai/ministral-3-3b",
		EmbeddingBackend:    "lmstudio", // default: use LM Studio for embeddings too
		EmbeddingModel:      "text-embedding-qwen3-embedding-4b",
		WebEnabled:          true,
		WebPort:             "8445",
		RelayEnabled:        true, // Relay on by default
		RelayURL:            "wss://ibeco.me/ws/brain",
		IbecomeURL:          "https://ibeco.me",
		IbecomeTaskSync:     true,  // Create tasks in ibecome when brain classifies actions/projects
		DiscordEnabled:      false, // Discord off by default
		ConfidenceThreshold: 0.6,
		RateLimits: RateLimitConfig{
			MaxAPICallsPerHour:     60,
			MaxGitCommitsPerDay:    100,
			MaxNotificationsPerDay: 20,
		},
	}

	// Required env vars
	cfg.GitHubToken = os.Getenv("GITHUB_TOKEN")
	cfg.DiscordToken = os.Getenv("DISCORD_TOKEN")
	cfg.DiscordChannelID = os.Getenv("DISCORD_CHANNEL_ID")

	// Relay config — prefer IBECOME_* (matches .env convention), fall back to RELAY_*
	cfg.RelayToken = envFirst("IBECOME_TOKEN", "RELAY_TOKEN")
	if v := envFirst("IBECOME_URL", "RELAY_URL"); v != "" {
		// Convert HTTP URL to WebSocket URL if needed
		u := v
		u = strings.Replace(u, "https://", "wss://", 1)
		u = strings.Replace(u, "http://", "ws://", 1)
		u = strings.TrimRight(u, "/")
		if !strings.HasSuffix(u, "/ws/brain") {
			u += "/ws/brain"
		}
		cfg.RelayURL = u
	}
	if v := envFirst("IBECOME_ENABLED", "RELAY_ENABLED"); v != "" {
		cfg.RelayEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("IBECOME_TASK_SYNC"); v != "" {
		cfg.IbecomeTaskSync = v == "true" || v == "1"
	}
	// Derive IbecomeURL from IBECOME_URL if not separately set
	if v := os.Getenv("IBECOME_API_URL"); v != "" {
		cfg.IbecomeURL = strings.TrimRight(v, "/")
	} else if v := envFirst("IBECOME_URL", "RELAY_URL"); v != "" {
		// Strip ws path to get the HTTP base
		u := v
		u = strings.Replace(u, "wss://", "https://", 1)
		u = strings.Replace(u, "ws://", "http://", 1)
		u = strings.TrimSuffix(u, "/ws/brain")
		u = strings.TrimRight(u, "/")
		cfg.IbecomeURL = u
	}

	// Discord config
	if v := os.Getenv("DISCORD_ENABLED"); v != "" {
		cfg.DiscordEnabled = v == "true" || v == "1"
	}

	// AI backend
	if v := os.Getenv("AI_BACKEND"); v != "" {
		cfg.AIBackend = v
	}

	// LM Studio config
	if v := os.Getenv("LMSTUDIO_URL"); v != "" {
		cfg.LMStudioURL = v
	}
	if v := os.Getenv("LMSTUDIO_MODEL"); v != "" {
		cfg.LMStudioModel = v
	}

	// Optional env vars with defaults
	if v := os.Getenv("AI_MODEL"); v != "" {
		// Check if it's a preset name first
		if preset, ok := AvailableModels[v]; ok {
			cfg.AIModel = preset.ID
			cfg.AIModelPreset = v
		} else {
			// Otherwise treat it as a raw model ID
			cfg.AIModel = v
			cfg.AIModelPreset = ""
		}
	}
	if v := os.Getenv("BRAIN_DATA_DIR"); v != "" {
		cfg.BrainDataDir = v
	}

	// Resolve brain data dir — default to ~/.brain-data or check common locations
	if cfg.BrainDataDir == "" {
		cfg.BrainDataDir = findBrainDataDir()
	}

	if cfg.BrainDataDir == "" {
		// Default: create ~/.brain-data
		home, _ := os.UserHomeDir()
		if home != "" {
			cfg.BrainDataDir = filepath.Join(home, ".brain-data")
		} else {
			cfg.BrainDataDir = ".brain-data"
		}
	}

	// Ensure data dir exists
	if err := os.MkdirAll(cfg.BrainDataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	// Storage paths — derived from BrainDataDir if not explicitly set
	if v := os.Getenv("BRAIN_DB_PATH"); v != "" {
		cfg.DBPath = v
	} else {
		cfg.DBPath = filepath.Join(cfg.BrainDataDir, "brain.db")
	}
	if v := os.Getenv("BRAIN_VEC_DIR"); v != "" {
		cfg.VecDir = v
	} else {
		cfg.VecDir = filepath.Join(cfg.BrainDataDir, "vec")
	}
	cfg.ArchiveDir = os.Getenv("BRAIN_ARCHIVE_DIR") // optional, no default

	// Embedding config
	if v := os.Getenv("EMBEDDING_BACKEND"); v != "" {
		cfg.EmbeddingBackend = v
	}
	if v := os.Getenv("EMBEDDING_MODEL"); v != "" {
		cfg.EmbeddingModel = v
	}
	if v := os.Getenv("OLLAMA_URL"); v != "" {
		cfg.OllamaURL = v
	}

	// Web UI config
	if v := os.Getenv("WEB_ENABLED"); v != "" {
		cfg.WebEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("WEB_PORT"); v != "" {
		cfg.WebPort = v
	}

	// Load .brain/config.yaml from data dir (optional)
	brainCfgPath := filepath.Join(cfg.BrainDataDir, ".brain", "config.yaml")
	if err := loadBrainConfig(brainCfgPath, cfg); err != nil {
		// Not fatal — we have defaults
		fmt.Fprintf(os.Stderr, "warning: could not load %s: %v\n", brainCfgPath, err)
	}

	return cfg, nil
}

// Validate checks that required configuration is present.
func (c *Config) Validate() error {
	// At least one transport must be enabled
	if !c.RelayEnabled && !c.DiscordEnabled && !c.WebEnabled {
		return fmt.Errorf("at least one interface must be enabled (RELAY_ENABLED, DISCORD_ENABLED, or WEB_ENABLED)")
	}
	if c.RelayEnabled && c.RelayToken == "" {
		return fmt.Errorf("IBECOME_TOKEN (or RELAY_TOKEN) is required when relay is enabled")
	}
	if c.DiscordEnabled && c.DiscordToken == "" {
		return fmt.Errorf("DISCORD_TOKEN is required when Discord is enabled")
	}
	if c.DBPath == "" {
		return fmt.Errorf("database path not configured")
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

// envFirst returns the first non-empty value from the given env var names.
func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}
