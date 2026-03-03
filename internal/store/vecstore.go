package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/philippgille/chromem-go"
)

const thoughtsCollection = "thoughts"

// VecStore wraps chromem-go for semantic search over brain entries.
// If embedFunc is nil, the vector store is disabled and all operations are no-ops.
type VecStore struct {
	db    *chromem.DB
	embed chromem.EmbeddingFunc
	model string // embedding model name for status tracking
	dir   string // persistence directory
}

// NewVecStore creates a new vector store with persistence at the given directory.
// If embedFunc is nil, returns a disabled VecStore (all operations are no-ops).
func NewVecStore(dir string, embedFunc chromem.EmbeddingFunc, modelName string) (*VecStore, error) {
	if embedFunc == nil {
		log.Printf("Vector store disabled (no embedding function configured)")
		return &VecStore{}, nil
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating vec dir: %w", err)
	}

	db, err := chromem.NewPersistentDB(dir, true)
	if err != nil {
		return nil, fmt.Errorf("opening chromem-go DB: %w", err)
	}

	vs := &VecStore{
		db:    db,
		embed: embedFunc,
		model: modelName,
		dir:   dir,
	}

	return vs, nil
}

// Enabled returns true if the vector store has an embedding function configured.
func (vs *VecStore) Enabled() bool {
	return vs.db != nil && vs.embed != nil
}

// collection returns (or creates) the "thoughts" collection.
func (vs *VecStore) collection(ctx context.Context) (*chromem.Collection, error) {
	col := vs.db.GetCollection(thoughtsCollection, vs.embed)
	if col != nil {
		return col, nil
	}
	return vs.db.GetOrCreateCollection(thoughtsCollection, nil, vs.embed)
}

// Embed adds or updates a document in the vector store for the given entry.
func (vs *VecStore) Embed(ctx context.Context, entry *Entry) error {
	if !vs.Enabled() {
		return nil
	}

	col, err := vs.collection(ctx)
	if err != nil {
		return fmt.Errorf("getting collection: %w", err)
	}

	// Build searchable content: title + body
	content := entry.Title + ". " + entry.Body

	doc := chromem.Document{
		ID:      entry.ID,
		Content: content,
		Metadata: map[string]string{
			"category":   entry.Category,
			"source":     entry.Source,
			"created_at": entry.Created.UTC().Format(time.RFC3339),
			"title":      entry.Title,
		},
	}

	if err := col.AddDocument(ctx, doc); err != nil {
		return fmt.Errorf("adding document: %w", err)
	}

	return nil
}

// Remove deletes a document from the vector store.
func (vs *VecStore) Remove(ctx context.Context, entryID string) error {
	if !vs.Enabled() {
		return nil
	}

	col, err := vs.collection(ctx)
	if err != nil {
		return fmt.Errorf("getting collection: %w", err)
	}

	return col.Delete(ctx, nil, nil, entryID)
}

// ReEmbed removes and re-adds a document (used after edits).
func (vs *VecStore) ReEmbed(ctx context.Context, entry *Entry) error {
	if !vs.Enabled() {
		return nil
	}

	if err := vs.Remove(ctx, entry.ID); err != nil {
		// Don't fail if the document didn't exist
		log.Printf("warning: remove before re-embed: %v", err)
	}

	return vs.Embed(ctx, entry)
}

// SearchResult holds a semantic search match.
type SearchResult struct {
	EntryID    string            `json:"entry_id"`
	Content    string            `json:"content"`
	Similarity float32           `json:"similarity"`
	Metadata   map[string]string `json:"metadata"`
}

// Search performs a semantic search returning the top N most similar entries.
func (vs *VecStore) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	if !vs.Enabled() {
		return nil, nil
	}

	col, err := vs.collection(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting collection: %w", err)
	}

	results, err := col.Query(ctx, query, n, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("querying: %w", err)
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			EntryID:    r.ID,
			Content:    r.Content,
			Similarity: r.Similarity,
			Metadata:   r.Metadata,
		}
	}
	return out, nil
}

// SearchWithCategory performs a semantic search filtered by category.
func (vs *VecStore) SearchWithCategory(ctx context.Context, query string, category string, n int) ([]SearchResult, error) {
	if !vs.Enabled() {
		return nil, nil
	}

	col, err := vs.collection(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting collection: %w", err)
	}

	where := map[string]string{"category": category}
	results, err := col.Query(ctx, query, n, where, nil)
	if err != nil {
		return nil, fmt.Errorf("querying with category: %w", err)
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			EntryID:    r.ID,
			Content:    r.Content,
			Similarity: r.Similarity,
			Metadata:   r.Metadata,
		}
	}
	return out, nil
}

// Count returns the number of documents in the vector store.
func (vs *VecStore) Count(ctx context.Context) int {
	if !vs.Enabled() {
		return 0
	}
	col, err := vs.collection(ctx)
	if err != nil {
		return 0
	}
	return col.Count()
}

// Model returns the embedding model name.
func (vs *VecStore) Model() string {
	return vs.model
}
