package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// DB wraps a SQLite database for structured entry storage.
type DB struct {
	db *sql.DB
}

// OpenDB opens (or creates) a SQLite database at the given path.
func OpenDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	d := &DB{db: db}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating database: %w", err)
	}
	return d, nil
}

// Close closes the underlying database.
func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) migrate() error {
	if _, err := d.db.Exec(schema); err != nil {
		return err
	}
	if err := d.migrateIbecomeTaskID(); err != nil {
		return err
	}
	return d.migrateAgentRouting()
}

const schema = `
CREATE TABLE IF NOT EXISTS entries (
    id            TEXT PRIMARY KEY,
    title         TEXT NOT NULL,
    category      TEXT NOT NULL,
    body          TEXT NOT NULL,
    confidence    REAL NOT NULL DEFAULT 0.0,
    needs_review  INTEGER NOT NULL DEFAULT 0,
    source        TEXT NOT NULL DEFAULT 'relay',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,

    -- Category-specific fields
    person_name    TEXT,
    person_context TEXT,
    follow_ups     TEXT,
    status         TEXT,
    next_action    TEXT,
    one_liner      TEXT,
    due_date       TEXT,
    action_done    INTEGER DEFAULT 0,
    scripture_refs TEXT,
    insight        TEXT,
    mood           TEXT,
    gratitude      TEXT
);

CREATE TABLE IF NOT EXISTS tags (
    entry_id TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    tag      TEXT NOT NULL,
    PRIMARY KEY (entry_id, tag)
);

CREATE TABLE IF NOT EXISTS audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id    TEXT REFERENCES entries(id) ON DELETE SET NULL,
    raw_text    TEXT NOT NULL,
    category    TEXT NOT NULL,
    title       TEXT NOT NULL,
    confidence  REAL NOT NULL,
    needs_review INTEGER NOT NULL,
    source      TEXT NOT NULL DEFAULT 'relay',
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS entry_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id    TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    category    TEXT NOT NULL,
    body        TEXT NOT NULL,
    changed_by  TEXT NOT NULL DEFAULT 'system',
    changed_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS embedding_status (
    entry_id    TEXT PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
    embedded_at TEXT,
    model       TEXT,
    error       TEXT
);

CREATE TABLE IF NOT EXISTS subtasks (
    id         TEXT PRIMARY KEY,
    entry_id   TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    text       TEXT NOT NULL,
    done       INTEGER NOT NULL DEFAULT 0,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_entries_category ON entries(category);
CREATE INDEX IF NOT EXISTS idx_entries_created ON entries(created_at);
CREATE INDEX IF NOT EXISTS idx_entries_needs_review ON entries(needs_review) WHERE needs_review = 1;
CREATE INDEX IF NOT EXISTS idx_tags_tag ON tags(tag);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
CREATE INDEX IF NOT EXISTS idx_subtasks_entry ON subtasks(entry_id);
`

// migrateIbecomeTaskID adds the ibecome_task_id column if it doesn't exist.
func (d *DB) migrateIbecomeTaskID() error {
	rows, err := d.db.Query("PRAGMA table_info(entries)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == "ibecome_task_id" {
			return nil // already exists
		}
	}
	_, err = d.db.Exec("ALTER TABLE entries ADD COLUMN ibecome_task_id INTEGER")
	return err
}

// migrateAgentRouting adds agent routing columns if they don't exist.
func (d *DB) migrateAgentRouting() error {
	cols, err := d.columnNames("entries")
	if err != nil {
		return err
	}
	if cols["agent_route"] {
		return nil // already migrated
	}
	for _, stmt := range []string{
		"ALTER TABLE entries ADD COLUMN agent_route TEXT",
		"ALTER TABLE entries ADD COLUMN route_status TEXT",
		"ALTER TABLE entries ADD COLUMN agent_output TEXT",
		"ALTER TABLE entries ADD COLUMN tokens_used INTEGER DEFAULT 0",
	} {
		if _, err := d.db.Exec(stmt); err != nil {
			return fmt.Errorf("agent routing migration: %w", err)
		}
	}
	return nil
}

// columnNames returns a set of column names for the given table.
func (d *DB) columnNames(table string) (map[string]bool, error) {
	rows, err := d.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, nil
}

// SetAgentRoute sets the agent routing info on an entry after classification.
func (d *DB) SetAgentRoute(entryID, agentRoute, routeStatus string) error {
	_, err := d.db.Exec(
		"UPDATE entries SET agent_route = ?, route_status = ?, updated_at = ? WHERE id = ?",
		agentRoute, routeStatus, time.Now().UTC().Format(time.RFC3339), entryID,
	)
	return err
}

// UpdateRouteStatus updates just the route status of an entry.
func (d *DB) UpdateRouteStatus(entryID, routeStatus string) error {
	_, err := d.db.Exec(
		"UPDATE entries SET route_status = ?, updated_at = ? WHERE id = ?",
		routeStatus, time.Now().UTC().Format(time.RFC3339), entryID,
	)
	return err
}

// SetAgentOutput records the agent's output path and token usage on an entry.
func (d *DB) SetAgentOutput(entryID, agentOutput string, tokensUsed int64) error {
	_, err := d.db.Exec(
		"UPDATE entries SET agent_output = ?, tokens_used = ?, route_status = 'complete', updated_at = ? WHERE id = ?",
		agentOutput, tokensUsed, time.Now().UTC().Format(time.RFC3339), entryID,
	)
	return err
}

// SetIbecomeTaskID links a brain entry to an ibecome task.
func (d *DB) SetIbecomeTaskID(entryID string, taskID int64) error {
	_, err := d.db.Exec("UPDATE entries SET ibecome_task_id = ? WHERE id = ?", taskID, entryID)
	return err
}

// UpdateEntryStatus updates the status and action_done fields of an entry.
// Used when ibecome notifies that a linked task's status has changed.
func (d *DB) UpdateEntryStatus(entryID, status string, actionDone bool) error {
	done := 0
	if actionDone {
		done = 1
	}
	_, err := d.db.Exec(
		"UPDATE entries SET status = ?, action_done = ?, updated_at = ? WHERE id = ?",
		status, done, time.Now().UTC().Format(time.RFC3339), entryID,
	)
	return err
}

// InsertEntry inserts a new entry and its tags, returning the generated ID.
func (d *DB) InsertEntry(e *Entry) (string, error) {
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.Created.IsZero() {
		e.Created = time.Now().UTC()
	}
	if e.Updated.IsZero() {
		e.Updated = time.Now().UTC()
	}

	tx, err := d.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		INSERT INTO entries (
			id, title, category, body, confidence, needs_review, source,
			created_at, updated_at,
			person_name, person_context, follow_ups,
			status, next_action, one_liner,
			due_date, action_done, scripture_refs, insight,
			mood, gratitude
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Title, e.Category, e.Body, e.Confidence, boolToInt(e.NeedsReview), e.Source,
		e.Created.UTC().Format(time.RFC3339), e.Updated.UTC().Format(time.RFC3339),
		nullStr(e.Name), nullStr(e.Context), nullStr(e.FollowUps),
		nullStr(e.Status), nullStr(e.NextAction), nullStr(e.OneLiner),
		nullStr(e.DueDate), boolToInt(e.ActionDone), nullStr(e.References), nullStr(e.Insight),
		nullStr(e.Mood), nullStr(e.Gratitude),
	)
	if err != nil {
		return "", fmt.Errorf("inserting entry: %w", err)
	}

	for _, tag := range e.Tags {
		if _, err := tx.Exec(`INSERT INTO tags (entry_id, tag) VALUES (?, ?)`, e.ID, tag); err != nil {
			return "", fmt.Errorf("inserting tag %q: %w", tag, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return e.ID, nil
}

// InsertAudit writes an audit log record.
func (d *DB) InsertAudit(entryID string, record *AuditRecord) error {
	_, err := d.db.Exec(`
		INSERT INTO audit_log (entry_id, raw_text, category, title, confidence, needs_review, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nullStr(entryID), record.RawText, record.Category, record.Title,
		record.Confidence, boolToInt(record.NeedsReview), record.Source,
		record.Timestamp.UTC().Format(time.RFC3339),
	)
	return err
}

// InsertVersion snapshots the current state of an entry before modification.
func (d *DB) InsertVersion(entryID, title, category, body, changedBy string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		INSERT INTO entry_versions (entry_id, title, category, body, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		entryID, title, category, body, changedBy, now,
	)
	return err
}

// GetEntry retrieves a single entry by ID, including its tags.
func (d *DB) GetEntry(id string) (*Entry, error) {
	e := &Entry{}
	var createdStr, updatedStr string
	var needsReview, actionDone int
	var personName, personCtx, followUps sql.NullString
	var status, nextAction, oneLiner sql.NullString
	var dueDate, scriptureRefs, insight sql.NullString
	var mood, gratitude sql.NullString
	var agentRoute, routeStatus, agentOutput sql.NullString
	var tokensUsed sql.NullInt64

	err := d.db.QueryRow(`
		SELECT id, title, category, body, confidence, needs_review, source,
			created_at, updated_at,
			person_name, person_context, follow_ups,
			status, next_action, one_liner,
			due_date, action_done, scripture_refs, insight,
			mood, gratitude,
			agent_route, route_status, agent_output, tokens_used
		FROM entries WHERE id = ?`, id).Scan(
		&e.ID, &e.Title, &e.Category, &e.Body, &e.Confidence, &needsReview, &e.Source,
		&createdStr, &updatedStr,
		&personName, &personCtx, &followUps,
		&status, &nextAction, &oneLiner,
		&dueDate, &actionDone, &scriptureRefs, &insight,
		&mood, &gratitude,
		&agentRoute, &routeStatus, &agentOutput, &tokensUsed,
	)
	if err != nil {
		return nil, err
	}

	e.NeedsReview = needsReview != 0
	e.ActionDone = actionDone != 0
	e.Created, _ = time.Parse(time.RFC3339, createdStr)
	e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
	e.Name = personName.String
	e.Context = personCtx.String
	e.FollowUps = followUps.String
	e.Status = status.String
	e.NextAction = nextAction.String
	e.OneLiner = oneLiner.String
	e.DueDate = dueDate.String
	e.References = scriptureRefs.String
	e.Insight = insight.String
	e.Mood = mood.String
	e.Gratitude = gratitude.String
	e.AgentRoute = agentRoute.String
	e.RouteStatus = routeStatus.String
	e.AgentOutput = agentOutput.String
	e.TokensUsed = tokensUsed.Int64

	// Load tags
	rows, err := d.db.Query(`SELECT tag FROM tags WHERE entry_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		e.Tags = append(e.Tags, tag)
	}

	// Load sub-tasks
	subtasks, err := d.ListSubTasks(id)
	if err != nil {
		return nil, fmt.Errorf("loading subtasks: %w", err)
	}
	e.SubTasks = subtasks

	return e, nil
}

// UpdateEntry updates an entry's mutable fields and its tags.
// It snapshots the previous state into entry_versions.
func (d *DB) UpdateEntry(e *Entry) error {
	// Get current state for versioning
	old, err := d.GetEntry(e.ID)
	if err != nil {
		return fmt.Errorf("reading current entry for version: %w", err)
	}

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Snapshot old state
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.Exec(`
		INSERT INTO entry_versions (entry_id, title, category, body, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		old.ID, old.Title, old.Category, old.Body, "user", now,
	)
	if err != nil {
		return fmt.Errorf("inserting version: %w", err)
	}

	// Update entry
	e.Updated = time.Now().UTC()
	_, err = tx.Exec(`
		UPDATE entries SET
			title = ?, category = ?, body = ?, confidence = ?,
			needs_review = ?, source = ?, updated_at = ?,
			person_name = ?, person_context = ?, follow_ups = ?,
			status = ?, next_action = ?, one_liner = ?,
			due_date = ?, action_done = ?, scripture_refs = ?, insight = ?,
			mood = ?, gratitude = ?
		WHERE id = ?`,
		e.Title, e.Category, e.Body, e.Confidence,
		boolToInt(e.NeedsReview), e.Source, e.Updated.UTC().Format(time.RFC3339),
		nullStr(e.Name), nullStr(e.Context), nullStr(e.FollowUps),
		nullStr(e.Status), nullStr(e.NextAction), nullStr(e.OneLiner),
		nullStr(e.DueDate), boolToInt(e.ActionDone), nullStr(e.References), nullStr(e.Insight),
		nullStr(e.Mood), nullStr(e.Gratitude),
		e.ID,
	)
	if err != nil {
		return fmt.Errorf("updating entry: %w", err)
	}

	// Replace tags
	if _, err := tx.Exec(`DELETE FROM tags WHERE entry_id = ?`, e.ID); err != nil {
		return fmt.Errorf("deleting old tags: %w", err)
	}
	for _, tag := range e.Tags {
		if _, err := tx.Exec(`INSERT INTO tags (entry_id, tag) VALUES (?, ?)`, e.ID, tag); err != nil {
			return fmt.Errorf("inserting tag: %w", err)
		}
	}

	return tx.Commit()
}

// DeleteEntry removes an entry and all related records (cascades).
func (d *DB) DeleteEntry(id string) error {
	_, err := d.db.Exec(`DELETE FROM entries WHERE id = ?`, id)
	return err
}

// Reclassify changes an entry's category.
func (d *DB) Reclassify(id, newCategory string) error {
	old, err := d.GetEntry(id)
	if err != nil {
		return fmt.Errorf("reading entry for reclassify: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Snapshot
	_, err = tx.Exec(`
		INSERT INTO entry_versions (entry_id, title, category, body, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		old.ID, old.Title, old.Category, old.Body, "reclassify", now,
	)
	if err != nil {
		return err
	}

	// Update category + clear needs_review
	_, err = tx.Exec(`
		UPDATE entries SET category = ?, needs_review = 0, updated_at = ? WHERE id = ?`,
		newCategory, now, id,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// ListCategory returns entries in a given category, newest first.
func (d *DB) ListCategory(category string) ([]*Entry, error) {
	rows, err := d.db.Query(`
		SELECT id, title, category, confidence, needs_review, source, created_at, updated_at,
			agent_route, route_status
		FROM entries WHERE category = ? ORDER BY created_at DESC`, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		e := &Entry{}
		var needsReview int
		var createdStr, updatedStr string
		var agentRoute, routeStatus sql.NullString
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Confidence, &needsReview, &e.Source, &createdStr, &updatedStr,
			&agentRoute, &routeStatus); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		e.AgentRoute = agentRoute.String
		e.RouteStatus = routeStatus.String
		entries = append(entries, e)
	}
	return entries, nil
}

// ListAll returns all entries, newest first.
func (d *DB) ListAll(limit, offset int) ([]*Entry, error) {
	rows, err := d.db.Query(`
		SELECT id, title, category, confidence, needs_review, source, created_at, updated_at
		FROM entries ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		e := &Entry{}
		var needsReview int
		var createdStr, updatedStr string
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Confidence, &needsReview, &e.Source, &createdStr, &updatedStr); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		entries = append(entries, e)
	}
	return entries, nil
}

// ListAllForSync returns all entries with fields needed for relay sync.
func (d *DB) ListAllForSync() ([]*Entry, error) {
	rows, err := d.db.Query(`
		SELECT id, title, category, body, status, action_done, due_date, next_action, source, created_at, updated_at
		FROM entries ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		e := &Entry{}
		var actionDone int
		var status, dueDate, nextAction sql.NullString
		var createdStr, updatedStr string
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Body, &status, &actionDone, &dueDate, &nextAction, &e.Source, &createdStr, &updatedStr); err != nil {
			return nil, err
		}
		e.ActionDone = actionDone != 0
		e.Status = status.String
		e.DueDate = dueDate.String
		e.NextAction = nextAction.String
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)

		// Load tags
		tagRows, err := d.db.Query(`SELECT tag FROM tags WHERE entry_id = ?`, e.ID)
		if err == nil {
			for tagRows.Next() {
				var tag string
				if tagRows.Scan(&tag) == nil {
					e.Tags = append(e.Tags, tag)
				}
			}
			tagRows.Close()
		}

		entries = append(entries, e)
	}
	return entries, nil
}

// NeedsReviewEntries returns entries flagged for review.
func (d *DB) NeedsReviewEntries() ([]*Entry, error) {
	rows, err := d.db.Query(`
		SELECT id, title, category, confidence, needs_review, source, created_at, updated_at
		FROM entries WHERE needs_review = 1 ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		e := &Entry{}
		var needsReview int
		var createdStr, updatedStr string
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Confidence, &needsReview, &e.Source, &createdStr, &updatedStr); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		entries = append(entries, e)
	}
	return entries, nil
}

// SearchText performs a simple text search across title and body.
func (d *DB) SearchText(query string, limit int) ([]*Entry, error) {
	like := "%" + query + "%"
	rows, err := d.db.Query(`
		SELECT id, title, category, confidence, needs_review, source, created_at, updated_at
		FROM entries
		WHERE title LIKE ? OR body LIKE ?
		ORDER BY created_at DESC LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		e := &Entry{}
		var needsReview int
		var createdStr, updatedStr string
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Confidence, &needsReview, &e.Source, &createdStr, &updatedStr); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		entries = append(entries, e)
	}
	return entries, nil
}

// Stats returns entry counts grouped by category.
func (d *DB) Stats() (map[string]int, error) {
	rows, err := d.db.Query(`SELECT category, COUNT(*) FROM entries GROUP BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[string]int)
	for rows.Next() {
		var cat string
		var count int
		if err := rows.Scan(&cat, &count); err != nil {
			return nil, err
		}
		stats[cat] = count
	}
	return stats, nil
}

// ListTags returns all tags with their usage counts.
func (d *DB) ListTags() (map[string]int, error) {
	rows, err := d.db.Query(`SELECT tag, COUNT(*) FROM tags GROUP BY tag ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tags := make(map[string]int)
	for rows.Next() {
		var tag string
		var count int
		if err := rows.Scan(&tag, &count); err != nil {
			return nil, err
		}
		tags[tag] = count
	}
	return tags, nil
}

// SetEmbeddingStatus records the embedding state for an entry.
func (d *DB) SetEmbeddingStatus(entryID, model string, embeddedAt time.Time, embErr string) error {
	_, err := d.db.Exec(`
		INSERT INTO embedding_status (entry_id, embedded_at, model, error)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(entry_id) DO UPDATE SET
			embedded_at = excluded.embedded_at,
			model = excluded.model,
			error = excluded.error`,
		entryID, embeddedAt.UTC().Format(time.RFC3339), model, nullStr(embErr),
	)
	return err
}

// helpers

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// EntryCount returns the total number of entries.
func (d *DB) EntryCount() (int, error) {
	var count int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count)
	return count, err
}

// --- Sub-task CRUD ---

// ListSubTasks returns all sub-tasks for an entry, ordered by sort_order.
func (d *DB) ListSubTasks(entryID string) ([]SubTask, error) {
	rows, err := d.db.Query(`
		SELECT id, entry_id, text, done, sort_order, created_at, updated_at
		FROM subtasks WHERE entry_id = ? ORDER BY sort_order, created_at`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []SubTask
	for rows.Next() {
		var st SubTask
		var done int
		var createdStr, updatedStr string
		if err := rows.Scan(&st.ID, &st.EntryID, &st.Text, &done, &st.SortOrder, &createdStr, &updatedStr); err != nil {
			return nil, err
		}
		st.Done = done != 0
		st.Created, _ = time.Parse(time.RFC3339, createdStr)
		st.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		tasks = append(tasks, st)
	}
	return tasks, nil
}

// InsertSubTask creates a new sub-task under an entry.
func (d *DB) InsertSubTask(st *SubTask) error {
	if st.ID == "" {
		st.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	if st.Created.IsZero() {
		st.Created = now
	}
	st.Updated = now

	_, err := d.db.Exec(`
		INSERT INTO subtasks (id, entry_id, text, done, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		st.ID, st.EntryID, st.Text, boolToInt(st.Done), st.SortOrder,
		st.Created.UTC().Format(time.RFC3339), st.Updated.UTC().Format(time.RFC3339),
	)
	return err
}

// UpdateSubTask updates a sub-task's text, done state, and sort order.
func (d *DB) UpdateSubTask(st *SubTask) error {
	st.Updated = time.Now().UTC()
	_, err := d.db.Exec(`
		UPDATE subtasks SET text = ?, done = ?, sort_order = ?, updated_at = ?
		WHERE id = ? AND entry_id = ?`,
		st.Text, boolToInt(st.Done), st.SortOrder,
		st.Updated.UTC().Format(time.RFC3339),
		st.ID, st.EntryID,
	)
	return err
}

// DeleteSubTask removes a sub-task by ID.
func (d *DB) DeleteSubTask(entryID, subtaskID string) error {
	_, err := d.db.Exec(`DELETE FROM subtasks WHERE id = ? AND entry_id = ?`, subtaskID, entryID)
	return err
}

// ReorderSubTasks sets sort_order for each sub-task by its position in the ids slice.
func (d *DB) ReorderSubTasks(entryID string, ids []string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE subtasks SET sort_order = ?, updated_at = ? WHERE id = ? AND entry_id = ?`,
			i, now, id, entryID); err != nil {
			return fmt.Errorf("reordering subtask %s: %w", id, err)
		}
	}
	return tx.Commit()
}
