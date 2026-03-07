// brain-mcp is an MCP server that exposes brain entries as searchable tools.
// It opens the same SQLite database and vector store used by the brain daemon,
// allowing any VS Code workspace to query your thoughts via the MCP protocol.
//
// Usage in .vscode/mcp.json:
//
//	{
//	  "servers": {
//	    "brain": {
//	      "type": "stdio",
//	      "command": "C:\\path\\to\\brain-mcp.exe"
//	    }
//	  }
//	}
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/cpuchip/brain/internal/lmstudio"
	brainmcp "github.com/cpuchip/brain/internal/mcp"
	"github.com/cpuchip/brain/internal/store"
	"github.com/joho/godotenv"
	"github.com/philippgille/chromem-go"
)

func main() {
	log.SetFlags(0) // MCP servers shouldn't clutter stderr with timestamps
	log.SetOutput(os.Stderr)

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "brain-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Load .env from brain code directory (same as brain daemon)
	loadDotEnv()

	dataDir := resolveDataDir()
	dbPath := filepath.Join(dataDir, "brain.db")
	vecDir := filepath.Join(dataDir, "vec")

	if v := os.Getenv("BRAIN_DB_PATH"); v != "" {
		dbPath = v
	}
	if v := os.Getenv("BRAIN_VEC_DIR"); v != "" {
		vecDir = v
	}

	log.Printf("brain-mcp starting (data: %s)", dataDir)

	// Open SQLite (WAL mode allows concurrent reads with the daemon)
	db, err := store.OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("opening database %s: %w", dbPath, err)
	}
	defer db.Close()

	// Open vector store (may be nil if embedding not configured)
	ctx := context.Background()
	embedFunc := chooseEmbedder(ctx)
	embeddingModel := os.Getenv("EMBEDDING_MODEL")
	if embeddingModel == "" {
		embeddingModel = "text-embedding-qwen3-embedding-4b"
	}

	var vec *store.VecStore
	if embedFunc != nil {
		vec, err = store.NewVecStore(vecDir, embedFunc, embeddingModel)
		if err != nil {
			log.Printf("warning: vector store unavailable: %v", err)
		}
	}

	st := store.New(db, vec, nil)

	srv := brainmcp.New(st)
	return srv.Serve()
}

func loadDotEnv() {
	// Try loading from brain code dir (where brain.exe lives)
	if exe, err := os.Executable(); err == nil {
		envPath := filepath.Join(filepath.Dir(exe), ".env")
		_ = godotenv.Load(envPath)
	}

	// Also try current directory
	_ = godotenv.Load(".env")

	// Also try brain code dir if BRAIN_CODE_DIR is set
	if codeDir := os.Getenv("BRAIN_CODE_DIR"); codeDir != "" {
		_ = godotenv.Load(filepath.Join(codeDir, ".env"))
	}
}

func resolveDataDir() string {
	if v := os.Getenv("BRAIN_DATA_DIR"); v != "" {
		return v
	}

	home, _ := os.UserHomeDir()

	// Check common locations where brain.db might exist
	candidates := []string{
		// Relative to executable (common for co-located installs)
		exeRelative("private-brain"),
		exeRelative(".."),
		exeRelative("..", "..", "private-brain"),
		// Relative to working directory
		"private-brain",
		filepath.Join("..", "private-brain"),
		filepath.Join("..", "..", "private-brain"),
		// Absolute known location
		filepath.Join(home, "Documents", "code", "stuffleberry", "scripture-study", "private-brain"),
		filepath.Join(home, ".brain-data"),
	}

	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		abs, _ := filepath.Abs(dir)
		if abs == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(abs, "brain.db")); err == nil && !info.IsDir() {
			return abs
		}
	}

	return filepath.Join(home, ".brain-data")
}

func exeRelative(parts ...string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	elems := append([]string{filepath.Dir(exe)}, parts...)
	return filepath.Join(elems...)
}

func chooseEmbedder(ctx context.Context) chromem.EmbeddingFunc {
	backend := os.Getenv("EMBEDDING_BACKEND")
	model := os.Getenv("EMBEDDING_MODEL")
	if model == "" {
		model = "text-embedding-qwen3-embedding-4b"
	}

	switch backend {
	case "lmstudio":
		url := os.Getenv("LMSTUDIO_URL")
		if url == "" {
			url = "http://localhost:1234/v1"
		}
		// Try to ensure the model is loaded (non-fatal if LM Studio isn't running)
		if err := lmstudio.EnsureModel(ctx, url, model); err != nil {
			log.Printf("warning: LM Studio embedding unavailable: %v (text search only)", err)
			return nil
		}
		return chromem.NewEmbeddingFuncOpenAICompat(url, "", model, nil)

	case "ollama":
		url := os.Getenv("OLLAMA_URL")
		if url == "" {
			return nil
		}
		return chromem.NewEmbeddingFuncOllama(model, url)

	case "openai":
		if os.Getenv("OPENAI_API_KEY") == "" {
			return nil
		}
		return chromem.NewEmbeddingFuncDefault()

	default:
		// Try LM Studio silently — if it's running, we get semantic search for free
		url := os.Getenv("LMSTUDIO_URL")
		if url == "" {
			url = "http://localhost:1234/v1"
		}
		if err := lmstudio.EnsureModel(ctx, url, model); err == nil {
			return chromem.NewEmbeddingFuncOpenAICompat(url, "", model, nil)
		}
		return nil
	}
}
