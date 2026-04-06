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
	if err := d.migrateAgentRouting(); err != nil {
		return err
	}
	if err := d.migrateOriginalBody(); err != nil {
		return err
	}
	if err := d.migrateMaturity(); err != nil {
		return err
	}
	if err := d.migrateProjects(); err != nil {
		return err
	}
	if err := d.migrateSessionMessages(); err != nil {
		return err
	}
	if err := d.migrateScheduledTasks(); err != nil {
		return err
	}
	return d.migratePremiumRequests()
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

// migrateOriginalBody adds the original_body column and backfills it.
// First pass: set from body (or title). Second pass: recover from entry_versions
// where the earliest version has a longer title (indicating the real raw capture
// before classification overwrote it).
func (d *DB) migrateOriginalBody() error {
	cols, err := d.columnNames("entries")
	if err != nil {
		return err
	}
	if cols["original_body"] {
		// Column exists — run recovery pass for entries that got backfilled with
		// AI-generated titles instead of the actual raw text.
		_, _ = d.db.Exec(`
			UPDATE entries SET original_body = (
				SELECT ev.title FROM entry_versions ev
				WHERE ev.entry_id = entries.id
				ORDER BY ev.changed_at ASC LIMIT 1
			)
			WHERE original_body IS NOT NULL
			  AND length(original_body) < 100
			  AND EXISTS (
				SELECT 1 FROM entry_versions ev
				WHERE ev.entry_id = entries.id
				  AND length(ev.title) > length(entries.original_body)
			)`)
		return nil
	}
	if _, err := d.db.Exec("ALTER TABLE entries ADD COLUMN original_body TEXT"); err != nil {
		return fmt.Errorf("original_body migration: %w", err)
	}
	// Backfill: set original_body to body where body is non-empty, else title
	_, err = d.db.Exec(`UPDATE entries SET original_body = CASE WHEN body != '' THEN body ELSE title END WHERE original_body IS NULL`)
	if err != nil {
		return fmt.Errorf("backfilling original_body: %w", err)
	}
	// Recovery: for entries where body is empty and entry_versions has the original
	// raw capture text (longer title from before classification), recover from versions.
	_, _ = d.db.Exec(`
		UPDATE entries SET original_body = (
			SELECT ev.title FROM entry_versions ev
			WHERE ev.entry_id = entries.id
			ORDER BY ev.changed_at ASC LIMIT 1
		)
		WHERE body = ''
		  AND EXISTS (
			SELECT 1 FROM entry_versions ev
			WHERE ev.entry_id = entries.id
			  AND length(ev.title) > length(entries.title)
		)`)
	return nil
}

// migrateMaturity adds pipeline maturity columns if they don't exist.
func (d *DB) migrateMaturity() error {
	cols, err := d.columnNames("entries")
	if err != nil {
		return err
	}
	if cols["maturity"] {
		return nil // already migrated
	}
	for _, stmt := range []string{
		"ALTER TABLE entries ADD COLUMN maturity TEXT NOT NULL DEFAULT 'raw'",
		"ALTER TABLE entries ADD COLUMN maturity_updated_at TEXT",
		"ALTER TABLE entries ADD COLUMN scratch_path TEXT",
		"ALTER TABLE entries ADD COLUMN scenarios TEXT",
		"ALTER TABLE entries ADD COLUMN maturity_notes TEXT",
	} {
		if _, err := d.db.Exec(stmt); err != nil {
			return fmt.Errorf("maturity migration: %w", err)
		}
	}
	// Index for pipeline queue queries
	d.db.Exec("CREATE INDEX IF NOT EXISTS idx_entries_maturity ON entries(maturity)")
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

// ListByRouteStatus returns entries with the given route_status, newest first.
func (d *DB) ListByRouteStatus(status string) ([]*Entry, error) {
	rows, err := d.db.Query(`
		SELECT id, title, category, body, confidence, source, created_at, updated_at,
			agent_route, route_status, agent_output, tokens_used
		FROM entries WHERE route_status = ? ORDER BY updated_at DESC`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		e := &Entry{}
		var createdStr, updatedStr string
		var agentRoute, routeStatus, agentOutput sql.NullString
		var tokensUsed sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Body, &e.Confidence, &e.Source, &createdStr, &updatedStr,
			&agentRoute, &routeStatus, &agentOutput, &tokensUsed); err != nil {
			return nil, err
		}
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		e.AgentRoute = agentRoute.String
		e.RouteStatus = routeStatus.String
		e.AgentOutput = agentOutput.String
		e.TokensUsed = tokensUsed.Int64
		entries = append(entries, e)
	}
	return entries, nil
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
			id, title, category, body, original_body, confidence, needs_review, source,
			created_at, updated_at,
			person_name, person_context, follow_ups,
			status, next_action, one_liner,
			due_date, action_done, scripture_refs, insight,
			mood, gratitude
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Title, e.Category, e.Body, e.OriginalBody, e.Confidence, boolToInt(e.NeedsReview), e.Source,
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

// ListVersions returns the version history for an entry, newest first.
func (d *DB) ListVersions(entryID string) ([]map[string]any, error) {
	rows, err := d.db.Query(`
		SELECT id, title, category, body, changed_by, changed_at
		FROM entry_versions WHERE entry_id = ? ORDER BY changed_at DESC`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []map[string]any
	for rows.Next() {
		var id int
		var title, category, body, changedBy, changedAt string
		if err := rows.Scan(&id, &title, &category, &body, &changedBy, &changedAt); err != nil {
			return nil, err
		}
		versions = append(versions, map[string]any{
			"id":         id,
			"title":      title,
			"category":   category,
			"body":       body,
			"changed_by": changedBy,
			"changed_at": changedAt,
		})
	}
	return versions, nil
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
	var premiumRequestsUsed sql.NullFloat64
	var originalBody sql.NullString
	var maturity, maturityUpdated, scratchPath, scenarios, maturityNotes sql.NullString
	var projectID sql.NullInt64

	err := d.db.QueryRow(`
		SELECT id, title, category, body, confidence, needs_review, source,
			created_at, updated_at,
			person_name, person_context, follow_ups,
			status, next_action, one_liner,
			due_date, action_done, scripture_refs, insight,
			mood, gratitude,
			agent_route, route_status, agent_output, tokens_used,
			premium_requests_used,
			original_body,
			maturity, maturity_updated_at, scratch_path, scenarios, maturity_notes,
			project_id
		FROM entries WHERE id = ?`, id).Scan(
		&e.ID, &e.Title, &e.Category, &e.Body, &e.Confidence, &needsReview, &e.Source,
		&createdStr, &updatedStr,
		&personName, &personCtx, &followUps,
		&status, &nextAction, &oneLiner,
		&dueDate, &actionDone, &scriptureRefs, &insight,
		&mood, &gratitude,
		&agentRoute, &routeStatus, &agentOutput, &tokensUsed,
		&premiumRequestsUsed,
		&originalBody,
		&maturity, &maturityUpdated, &scratchPath, &scenarios, &maturityNotes,
		&projectID,
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
	e.PremiumRequestsUsed = premiumRequestsUsed.Float64
	e.OriginalBody = originalBody.String
	e.Maturity = maturity.String
	e.MaturityUpdated = maturityUpdated.String
	e.ScratchPath = scratchPath.String
	e.Scenarios = scenarios.String
	e.MaturityNotes = maturityNotes.String
	if projectID.Valid {
		v := int(projectID.Int64)
		e.ProjectID = &v
	}

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
			mood = ?, gratitude = ?, project_id = ?
		WHERE id = ?`,
		e.Title, e.Category, e.Body, e.Confidence,
		boolToInt(e.NeedsReview), e.Source, e.Updated.UTC().Format(time.RFC3339),
		nullStr(e.Name), nullStr(e.Context), nullStr(e.FollowUps),
		nullStr(e.Status), nullStr(e.NextAction), nullStr(e.OneLiner),
		nullStr(e.DueDate), boolToInt(e.ActionDone), nullStr(e.References), nullStr(e.Insight),
		nullStr(e.Mood), nullStr(e.Gratitude), e.ProjectID,
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
		SELECT id, title, category, body, confidence, needs_review, source, created_at, updated_at,
			agent_route, route_status, project_id, maturity
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
		var projectID sql.NullInt64
		var maturity sql.NullString
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Body, &e.Confidence, &needsReview, &e.Source, &createdStr, &updatedStr,
			&agentRoute, &routeStatus, &projectID, &maturity); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		e.AgentRoute = agentRoute.String
		e.RouteStatus = routeStatus.String
		if projectID.Valid {
			v := int(projectID.Int64)
			e.ProjectID = &v
		}
		if maturity.Valid {
			e.Maturity = maturity.String
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// ListAll returns all entries, newest first.
func (d *DB) ListAll(limit, offset int) ([]*Entry, error) {
	rows, err := d.db.Query(`
		SELECT id, title, category, body, confidence, needs_review, source, created_at, updated_at, project_id,
			agent_route, route_status, maturity
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
		var projectID sql.NullInt64
		var agentRoute, routeStatus, maturity sql.NullString
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Body, &e.Confidence, &needsReview, &e.Source, &createdStr, &updatedStr, &projectID,
			&agentRoute, &routeStatus, &maturity); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		if projectID.Valid {
			v := int(projectID.Int64)
			e.ProjectID = &v
		}
		if agentRoute.Valid {
			e.AgentRoute = agentRoute.String
		}
		if routeStatus.Valid {
			e.RouteStatus = routeStatus.String
		}
		if maturity.Valid {
			e.Maturity = maturity.String
		}
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
		SELECT id, title, category, body, confidence, needs_review, source, created_at, updated_at, project_id
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
		var projectID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Body, &e.Confidence, &needsReview, &e.Source, &createdStr, &updatedStr, &projectID); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		if projectID.Valid {
			v := int(projectID.Int64)
			e.ProjectID = &v
		}
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

// --- Pipeline maturity ---

// SetMaturity updates the maturity stage and related fields for an entry.
func (d *DB) SetMaturity(entryID, maturity, notes string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(
		"UPDATE entries SET maturity = ?, maturity_updated_at = ?, maturity_notes = ?, updated_at = ? WHERE id = ?",
		maturity, now, nullStr(notes), now, entryID,
	)
	return err
}

// SetScratchPath records the path to the entry's scratch/research file.
func (d *DB) SetScratchPath(entryID, path string) error {
	_, err := d.db.Exec(
		"UPDATE entries SET scratch_path = ?, updated_at = ? WHERE id = ?",
		path, time.Now().UTC().Format(time.RFC3339), entryID,
	)
	return err
}

// SetScenarios stores the JSON scenarios array for a specced entry.
func (d *DB) SetScenarios(entryID, scenariosJSON string) error {
	_, err := d.db.Exec(
		"UPDATE entries SET scenarios = ?, updated_at = ? WHERE id = ?",
		scenariosJSON, time.Now().UTC().Format(time.RFC3339), entryID,
	)
	return err
}

// PipelineCategories are the categories that enter the maturity pipeline.
var PipelineCategories = []string{"ideas", "projects", "study"}

// ListStaleEntries returns pipeline entries that are stale and have NOT already
// been nudged (route_status != 'your_turn'). Each cutoff is the absolute time:
// entries with maturity_updated_at (or created_at) before that time are considered stale.
func (d *DB) ListStaleEntries(rawCutoff, researchedCutoff, completeCutoff time.Time) ([]*Entry, error) {
	rawCut := rawCutoff.Format(time.RFC3339)
	resCut := researchedCutoff.Format(time.RFC3339)
	compCut := completeCutoff.Format(time.RFC3339)

	rows, err := d.db.Query(`
		SELECT id, title, category, body, confidence, needs_review, source,
		       created_at, updated_at, project_id,
		       maturity, maturity_updated_at, scratch_path, agent_route, route_status
		FROM entries
		WHERE category IN ('ideas', 'projects', 'study')
		  AND COALESCE(route_status, '') NOT IN ('your_turn', 'running', 'pending')
		  AND (
		    (COALESCE(maturity, 'raw') = 'raw' AND COALESCE(maturity_updated_at, created_at) < ?)
		    OR (maturity = 'researched' AND maturity_updated_at < ?)
		    OR (route_status = 'complete' AND updated_at < ?)
		  )
		ORDER BY created_at ASC
		LIMIT 10`, rawCut, resCut, compCut)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		e := &Entry{}
		var needsReview int
		var createdStr, updatedStr string
		var projectID sql.NullInt64
		var maturity, maturityUpdated, scratchPath, agentRoute, routeStatus sql.NullString
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Body, &e.Confidence, &needsReview, &e.Source,
			&createdStr, &updatedStr, &projectID,
			&maturity, &maturityUpdated, &scratchPath, &agentRoute, &routeStatus); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		if projectID.Valid {
			v := int(projectID.Int64)
			e.ProjectID = &v
		}
		e.Maturity = maturity.String
		e.MaturityUpdated = maturityUpdated.String
		e.ScratchPath = scratchPath.String
		e.AgentRoute = agentRoute.String
		e.RouteStatus = routeStatus.String
		entries = append(entries, e)
	}
	return entries, nil
}

// ListPipeline returns entries in pipeline categories grouped by maturity stage.
// Returns a map of maturity stage -> entries, with entries ordered by updated_at desc.
func (d *DB) ListPipeline(stageFilter, categoryFilter string, limitPerStage int) (map[string][]*Entry, error) {
	query := `
		SELECT id, title, category, maturity, maturity_updated_at, scratch_path, created_at, updated_at
		FROM entries
		WHERE category IN ('ideas', 'projects', 'study')`
	var args []interface{}

	if stageFilter != "" {
		query += " AND maturity = ?"
		args = append(args, stageFilter)
	}
	if categoryFilter != "" {
		query += " AND category = ?"
		args = append(args, categoryFilter)
	}
	query += " ORDER BY updated_at DESC"

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]*Entry)
	counts := make(map[string]int)

	for rows.Next() {
		e := &Entry{}
		var createdStr, updatedStr string
		var maturity, maturityUpdated, scratchPath sql.NullString
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &maturity, &maturityUpdated, &scratchPath, &createdStr, &updatedStr); err != nil {
			return nil, err
		}
		e.Maturity = maturity.String
		if e.Maturity == "" {
			e.Maturity = "raw"
		}
		e.MaturityUpdated = maturityUpdated.String
		e.ScratchPath = scratchPath.String
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)

		if limitPerStage > 0 && counts[e.Maturity] >= limitPerStage {
			continue
		}
		result[e.Maturity] = append(result[e.Maturity], e)
		counts[e.Maturity]++
	}
	return result, nil
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

// --- Projects ---

// migrateProjects creates the projects table and adds project_id to entries.
func (d *DB) migrateProjects() error {
	// Create projects table if not exists
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS projects (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL UNIQUE,
			description TEXT,
			status      TEXT NOT NULL DEFAULT 'active',
			emoji       TEXT,
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL
		)`)
	if err != nil {
		return fmt.Errorf("creating projects table: %w", err)
	}

	// Add project_id column to entries if not exists
	cols, err := d.columnNames("entries")
	if err != nil {
		return err
	}
	if !cols["project_id"] {
		_, err = d.db.Exec("ALTER TABLE entries ADD COLUMN project_id INTEGER REFERENCES projects(id)")
		if err != nil {
			return fmt.Errorf("adding project_id to entries: %w", err)
		}
		d.db.Exec("CREATE INDEX IF NOT EXISTS idx_entries_project ON entries(project_id)")
	}

	// Add context_file column to projects if not exists
	projCols, err := d.columnNames("projects")
	if err != nil {
		return err
	}
	if !projCols["context_file"] {
		_, err = d.db.Exec("ALTER TABLE projects ADD COLUMN context_file TEXT")
		if err != nil {
			return fmt.Errorf("adding context_file to projects: %w", err)
		}
	}
	return nil
}

// CreateProject inserts a new project and returns its ID.
func (d *DB) CreateProject(p *Project) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := d.db.Exec(`
		INSERT INTO projects (name, description, status, emoji, context_file, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.Name, nullStr(p.Description), p.Status, nullStr(p.Emoji), nullStr(p.ContextFile), now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("creating project: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return int(id), nil
}

// GetProject retrieves a project by ID.
func (d *DB) GetProject(id int) (*Project, error) {
	p := &Project{}
	var createdStr, updatedStr string
	var desc, emoji, contextFile sql.NullString
	err := d.db.QueryRow(`
		SELECT id, name, description, status, emoji, context_file, created_at, updated_at
		FROM projects WHERE id = ?`, id).Scan(
		&p.ID, &p.Name, &desc, &p.Status, &emoji, &contextFile, &createdStr, &updatedStr,
	)
	if err != nil {
		return nil, err
	}
	p.Description = desc.String
	p.Emoji = emoji.String
	p.ContextFile = contextFile.String
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return p, nil
}

// ListUnassigned returns entries with no project assignment, ordered by newest first.
func (d *DB) ListUnassigned(limit int) ([]*Entry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.Query(`
		SELECT id, title, category, body, confidence, needs_review, source, created_at, updated_at, project_id,
			agent_route, route_status, maturity
		FROM entries WHERE project_id IS NULL ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		e := &Entry{}
		var needsReview int
		var createdStr, updatedStr string
		var projectID sql.NullInt64
		var agentRoute, routeStatus, maturity sql.NullString
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Body, &e.Confidence, &needsReview, &e.Source, &createdStr, &updatedStr, &projectID,
			&agentRoute, &routeStatus, &maturity); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		if projectID.Valid {
			v := int(projectID.Int64)
			e.ProjectID = &v
		}
		if agentRoute.Valid {
			e.AgentRoute = agentRoute.String
		}
		if routeStatus.Valid {
			e.RouteStatus = routeStatus.String
		}
		if maturity.Valid {
			e.Maturity = maturity.String
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// CountUnassigned returns the number of entries with no project assignment.
func (d *DB) CountUnassigned() int {
	var count int
	d.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE project_id IS NULL`).Scan(&count)
	return count
}

// ListProjects returns all projects with entry counts, ordered by status then name.
func (d *DB) ListProjects() ([]*Project, error) {
	rows, err := d.db.Query(`
		SELECT p.id, p.name, p.description, p.status, p.emoji, p.context_file, p.created_at, p.updated_at,
			COUNT(e.id) AS entry_count
		FROM projects p
		LEFT JOIN entries e ON e.project_id = p.id
		GROUP BY p.id
		ORDER BY
			CASE p.status WHEN 'active' THEN 0 WHEN 'paused' THEN 1 ELSE 2 END,
			p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []*Project
	for rows.Next() {
		p := &Project{}
		var createdStr, updatedStr string
		var desc, emoji, contextFile sql.NullString
		if err := rows.Scan(&p.ID, &p.Name, &desc, &p.Status, &emoji, &contextFile, &createdStr, &updatedStr, &p.EntryCount); err != nil {
			return nil, err
		}
		p.Description = desc.String
		p.Emoji = emoji.String
		p.ContextFile = contextFile.String
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		projects = append(projects, p)
	}
	return projects, nil
}

// UpdateProject updates a project's mutable fields.
func (d *DB) UpdateProject(p *Project) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		UPDATE projects SET name = ?, description = ?, status = ?, emoji = ?, context_file = ?, updated_at = ?
		WHERE id = ?`,
		p.Name, nullStr(p.Description), p.Status, nullStr(p.Emoji), nullStr(p.ContextFile), now, p.ID,
	)
	return err
}

// DeleteProject removes a project. Entries linked to it get project_id set to NULL.
func (d *DB) DeleteProject(id int) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Unlink entries
	if _, err := tx.Exec("UPDATE entries SET project_id = NULL WHERE project_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM projects WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetEntryProject assigns an entry to a project (or removes assignment with nil).
func (d *DB) SetEntryProject(entryID string, projectID *int) error {
	_, err := d.db.Exec(
		"UPDATE entries SET project_id = ?, updated_at = ? WHERE id = ?",
		projectID, time.Now().UTC().Format(time.RFC3339), entryID,
	)
	return err
}

// ProjectStats holds per-maturity-stage counts for a project.
type ProjectStats struct {
	MaturityCounts map[string]int `json:"maturity_counts"` // e.g. {"raw": 5, "researched": 2}
	YourTurnCount  int            `json:"your_turn_count"`
	RunningCount   int            `json:"running_count"`
	TotalEntries   int            `json:"total_entries"`
}

// GetProjectStats returns aggregate stats for a project.
func (d *DB) GetProjectStats(projectID int) (*ProjectStats, error) {
	stats := &ProjectStats{MaturityCounts: make(map[string]int)}

	// Maturity counts
	rows, err := d.db.Query(`
		SELECT COALESCE(maturity, 'raw') AS m, COUNT(*) AS c
		FROM entries WHERE project_id = ?
		GROUP BY m`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		var c int
		if err := rows.Scan(&m, &c); err != nil {
			return nil, err
		}
		if m == "" {
			m = "raw"
		}
		stats.MaturityCounts[m] = c
		stats.TotalEntries += c
	}

	// Your-turn count
	d.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE project_id = ? AND route_status = 'your_turn'`,
		projectID).Scan(&stats.YourTurnCount)

	// Running count
	d.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE project_id = ? AND route_status = 'running'`,
		projectID).Scan(&stats.RunningCount)

	return stats, nil
}

// ListEntriesByProject returns entries for a given project, newest first.
func (d *DB) ListEntriesByProject(projectID int) ([]*Entry, error) {
	rows, err := d.db.Query(`
		SELECT id, title, category, body, confidence, needs_review, source, created_at, updated_at,
			agent_route, route_status, maturity, project_id, premium_requests_used
		FROM entries WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*Entry
	for rows.Next() {
		e := &Entry{}
		var needsReview int
		var createdStr, updatedStr string
		var agentRoute, routeStatus, maturity sql.NullString
		var pid sql.NullInt64
		var premiumCost sql.NullFloat64
		if err := rows.Scan(&e.ID, &e.Title, &e.Category, &e.Body, &e.Confidence, &needsReview, &e.Source,
			&createdStr, &updatedStr, &agentRoute, &routeStatus, &maturity, &pid, &premiumCost); err != nil {
			return nil, err
		}
		e.NeedsReview = needsReview != 0
		e.Created, _ = time.Parse(time.RFC3339, createdStr)
		e.Updated, _ = time.Parse(time.RFC3339, updatedStr)
		e.AgentRoute = agentRoute.String
		e.RouteStatus = routeStatus.String
		e.Maturity = maturity.String
		if e.Maturity == "" {
			e.Maturity = "raw"
		}
		if pid.Valid {
			v := int(pid.Int64)
			e.ProjectID = &v
		}
		e.PremiumRequestsUsed = premiumCost.Float64
		entries = append(entries, e)
	}
	return entries, nil
}

// --- Session Messages (Phase 2: Iterative Sessions) ---

func (d *DB) migrateSessionMessages() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS session_messages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			entry_id   TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
			role       TEXT NOT NULL,
			content    TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`)
	if err != nil {
		return fmt.Errorf("creating session_messages table: %w", err)
	}
	d.db.Exec("CREATE INDEX IF NOT EXISTS idx_session_messages_entry ON session_messages(entry_id)")
	return nil
}

// AddSessionMessage records a turn in an entry's conversation.
func (d *DB) AddSessionMessage(entryID, role, content string) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := d.db.Exec(`
		INSERT INTO session_messages (entry_id, role, content, created_at)
		VALUES (?, ?, ?, ?)`, entryID, role, content, now)
	if err != nil {
		return 0, fmt.Errorf("adding session message: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return int(id), nil
}

// ListSessionMessages returns all messages for an entry, oldest first.
func (d *DB) ListSessionMessages(entryID string) ([]*SessionMessage, error) {
	rows, err := d.db.Query(`
		SELECT id, entry_id, role, content, created_at
		FROM session_messages WHERE entry_id = ? ORDER BY created_at ASC`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*SessionMessage
	for rows.Next() {
		m := &SessionMessage{}
		var createdStr string
		if err := rows.Scan(&m.ID, &m.EntryID, &m.Role, &m.Content, &createdStr); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// --- Scheduled Tasks ---

func (d *DB) migrateScheduledTasks() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS scheduled_tasks (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL UNIQUE,
			description TEXT,
			schedule    TEXT NOT NULL,
			project_id  INTEGER REFERENCES projects(id) ON DELETE SET NULL,
			agent_name  TEXT NOT NULL,
			prompt      TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'active',
			last_run_at TEXT,
			next_run_at TEXT,
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL
		)`)
	if err != nil {
		return fmt.Errorf("creating scheduled_tasks table: %w", err)
	}

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS task_runs (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id    INTEGER NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
			status     TEXT NOT NULL DEFAULT 'running',
			entry_id   TEXT,
			output     TEXT,
			error      TEXT,
			started_at TEXT NOT NULL,
			ended_at   TEXT
		)`)
	if err != nil {
		return fmt.Errorf("creating task_runs table: %w", err)
	}

	d.db.Exec("CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs(task_id)")
	return nil
}

// migratePremiumRequests adds the premium_requests_used column to entries.
func (d *DB) migratePremiumRequests() error {
	cols, err := d.columnNames("entries")
	if err != nil {
		return err
	}
	if cols["premium_requests_used"] {
		return nil // already migrated
	}
	_, err = d.db.Exec("ALTER TABLE entries ADD COLUMN premium_requests_used REAL DEFAULT 0")
	return err
}

// IncrementPremiumRequests atomically adds cost to an entry's premium_requests_used.
func (d *DB) IncrementPremiumRequests(entryID string, cost float64) error {
	_, err := d.db.Exec(
		"UPDATE entries SET premium_requests_used = COALESCE(premium_requests_used, 0) + ?, updated_at = ? WHERE id = ?",
		cost, time.Now().UTC().Format(time.RFC3339), entryID,
	)
	return err
}

// CreateScheduledTask creates a new scheduled task.
func (d *DB) CreateScheduledTask(task *ScheduledTask) (*ScheduledTask, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := d.db.Exec(`
		INSERT INTO scheduled_tasks (name, description, schedule, project_id, agent_name, prompt, status, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.Name, task.Description, task.Schedule, task.ProjectID, task.AgentName, task.Prompt, "active", nil, now, now)
	if err != nil {
		return nil, fmt.Errorf("creating scheduled task: %w", err)
	}
	id, _ := result.LastInsertId()
	return d.GetScheduledTask(int(id))
}

// GetScheduledTask returns a scheduled task by ID.
func (d *DB) GetScheduledTask(id int) (*ScheduledTask, error) {
	row := d.db.QueryRow(`
		SELECT id, name, description, schedule, project_id, agent_name, prompt, status, last_run_at, next_run_at, created_at, updated_at
		FROM scheduled_tasks WHERE id = ?`, id)
	return scanScheduledTask(row)
}

// ListScheduledTasks returns all scheduled tasks.
func (d *DB) ListScheduledTasks() ([]*ScheduledTask, error) {
	rows, err := d.db.Query(`
		SELECT id, name, description, schedule, project_id, agent_name, prompt, status, last_run_at, next_run_at, created_at, updated_at
		FROM scheduled_tasks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*ScheduledTask
	for rows.Next() {
		t, err := scanScheduledTaskRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// ListActiveScheduledTasks returns tasks with status "active".
func (d *DB) ListActiveScheduledTasks() ([]*ScheduledTask, error) {
	rows, err := d.db.Query(`
		SELECT id, name, description, schedule, project_id, agent_name, prompt, status, last_run_at, next_run_at, created_at, updated_at
		FROM scheduled_tasks WHERE status = 'active' ORDER BY next_run_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*ScheduledTask
	for rows.Next() {
		t, err := scanScheduledTaskRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// UpdateScheduledTask updates a scheduled task's fields.
func (d *DB) UpdateScheduledTask(id int, name, description, schedule, agentName, prompt, status string, projectID *int) (*ScheduledTask, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		UPDATE scheduled_tasks SET name=?, description=?, schedule=?, agent_name=?, prompt=?, status=?, project_id=?, updated_at=?
		WHERE id=?`,
		name, description, schedule, agentName, prompt, status, projectID, now, id)
	if err != nil {
		return nil, fmt.Errorf("updating scheduled task: %w", err)
	}
	return d.GetScheduledTask(id)
}

// DeleteScheduledTask deletes a scheduled task and its run history.
func (d *DB) DeleteScheduledTask(id int) error {
	_, err := d.db.Exec("DELETE FROM scheduled_tasks WHERE id = ?", id)
	return err
}

// SetTaskLastRun updates a task's last_run_at and next_run_at timestamps.
func (d *DB) SetTaskLastRun(id int, lastRun, nextRun time.Time) error {
	_, err := d.db.Exec(`
		UPDATE scheduled_tasks SET last_run_at=?, next_run_at=?, updated_at=? WHERE id=?`,
		lastRun.UTC().Format(time.RFC3339), nextRun.UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// CreateTaskRun records a new task run.
func (d *DB) CreateTaskRun(taskID int) (*TaskRun, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := d.db.Exec(`
		INSERT INTO task_runs (task_id, status, started_at) VALUES (?, 'running', ?)`,
		taskID, now)
	if err != nil {
		return nil, fmt.Errorf("creating task run: %w", err)
	}
	id, _ := result.LastInsertId()
	return &TaskRun{
		ID:        int(id),
		TaskID:    taskID,
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}, nil
}

// CompleteTaskRun marks a task run as complete or failed.
func (d *DB) CompleteTaskRun(id int, status, entryID, output, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		UPDATE task_runs SET status=?, entry_id=?, output=?, error=?, ended_at=? WHERE id=?`,
		status, entryID, output, errMsg, now, id)
	return err
}

// ListTaskRuns returns runs for a task, most recent first, limited.
func (d *DB) ListTaskRuns(taskID, limit int) ([]*TaskRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.db.Query(`
		SELECT id, task_id, status, entry_id, output, error, started_at, ended_at
		FROM task_runs WHERE task_id = ? ORDER BY started_at DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*TaskRun
	for rows.Next() {
		r := &TaskRun{}
		var entryID, output, errMsg, endedAt *string
		var startedStr string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Status, &entryID, &output, &errMsg, &startedStr, &endedAt); err != nil {
			return nil, err
		}
		r.StartedAt, _ = time.Parse(time.RFC3339, startedStr)
		if entryID != nil {
			r.EntryID = *entryID
		}
		if output != nil {
			r.Output = *output
		}
		if errMsg != nil {
			r.Error = *errMsg
		}
		if endedAt != nil {
			t, _ := time.Parse(time.RFC3339, *endedAt)
			r.EndedAt = &t
		}
		runs = append(runs, r)
	}
	return runs, nil
}

// RecentActivity returns recent events across all entries/tasks for the dashboard.
func (d *DB) RecentActivity(limit int) ([]*ActivityEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.db.Query(`
		SELECT id, title, category, project_id, created_at, updated_at, route_status, agent_route
		FROM entries ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*ActivityEvent
	for rows.Next() {
		var id, title, category, updatedStr, createdStr string
		var projectID *int
		var routeStatus, agentRoute *string
		if err := rows.Scan(&id, &title, &category, &projectID, &createdStr, &updatedStr, &routeStatus, &agentRoute); err != nil {
			return nil, err
		}
		ts, _ := time.Parse(time.RFC3339, updatedStr)

		eventType := "entry_updated"
		if createdStr == updatedStr {
			eventType = "entry_created"
		} else if routeStatus != nil {
			switch *routeStatus {
			case "complete":
				eventType = "agent_completed"
			case "running":
				eventType = "entry_routed"
			case "your_turn":
				eventType = "your_turn"
			}
		}
		events = append(events, &ActivityEvent{
			ID:        id,
			Type:      eventType,
			Title:     title,
			EntryID:   id,
			ProjectID: projectID,
			Timestamp: ts,
		})
	}
	return events, nil
}

// --- scan helpers ---

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanScheduledTask(row scannable) (*ScheduledTask, error) {
	t := &ScheduledTask{}
	var desc, lastRun, nextRun *string
	var createdStr, updatedStr string
	if err := row.Scan(&t.ID, &t.Name, &desc, &t.Schedule, &t.ProjectID, &t.AgentName, &t.Prompt, &t.Status, &lastRun, &nextRun, &createdStr, &updatedStr); err != nil {
		return nil, err
	}
	if desc != nil {
		t.Description = *desc
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	if lastRun != nil {
		lr, _ := time.Parse(time.RFC3339, *lastRun)
		t.LastRunAt = &lr
	}
	if nextRun != nil {
		nr, _ := time.Parse(time.RFC3339, *nextRun)
		t.NextRunAt = &nr
	}
	return t, nil
}

func scanScheduledTaskRow(rows *sql.Rows) (*ScheduledTask, error) {
	return scanScheduledTask(rows)
}
