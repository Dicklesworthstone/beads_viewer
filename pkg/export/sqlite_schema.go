// Package export provides data export functionality for bv.
//
// This file implements SQLite schema creation for static export,
// following mcp_agent_mail's architecture for client-side sql.js querying.
package export

import (
	"database/sql"
	"fmt"
)

// Schema version for tracking migrations
const SchemaVersion = 1

// CreateSchema creates all tables and indexes in one durable transaction.
func CreateSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin schema: %w", err)
	}
	defer tx.Rollback()

	// Create tables in order of dependencies
	if err := createCoreTables(tx); err != nil {
		return fmt.Errorf("create core tables: %w", err)
	}

	if err := createMetricsTables(tx); err != nil {
		return fmt.Errorf("create metrics tables: %w", err)
	}

	if err := createIndexes(tx); err != nil {
		return fmt.Errorf("create indexes: %w", err)
	}

	if err := createMetaTable(tx); err != nil {
		return fmt.Errorf("create meta table: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema: %w", err)
	}

	return nil
}

// createCoreTables creates the issues, dependencies, and comments tables.
func createCoreTables(tx *sql.Tx) error {
	// Issues table - core issue data
	issuesSQL := `
		CREATE TABLE IF NOT EXISTS issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			design TEXT,
			acceptance_criteria TEXT,
			notes TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			assignee TEXT,
			labels TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT
		)
	`
	if _, err := tx.Exec(issuesSQL); err != nil {
		return fmt.Errorf("create issues table: %w", err)
	}

	// Dependencies table - blocking relationships
	depsSQL := `
		CREATE TABLE IF NOT EXISTS dependencies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'blocks',
			FOREIGN KEY (issue_id) REFERENCES issues(id),
			FOREIGN KEY (depends_on_id) REFERENCES issues(id)
		)
	`
	if _, err := tx.Exec(depsSQL); err != nil {
		return fmt.Errorf("create dependencies table: %w", err)
	}

	// Comments table - issue discussion threads (bv-52)
	// id is TEXT to support composite IDs (issue_id:comment_id) in workspace mode
	commentsSQL := `
		CREATE TABLE IF NOT EXISTS comments (
			id TEXT PRIMARY KEY,
			issue_id TEXT NOT NULL,
			author TEXT,
			text TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY (issue_id) REFERENCES issues(id)
		)
	`
	if _, err := tx.Exec(commentsSQL); err != nil {
		return fmt.Errorf("create comments table: %w", err)
	}

	return nil
}

// createMetricsTables creates tables for computed graph metrics.
func createMetricsTables(tx *sql.Tx) error {
	// Issue metrics - computed by bv analysis
	metricsSQL := `
		CREATE TABLE IF NOT EXISTS issue_metrics (
			issue_id TEXT PRIMARY KEY,
			pagerank REAL DEFAULT 0,
			betweenness REAL DEFAULT 0,
			critical_path_depth INTEGER DEFAULT 0,
			triage_score REAL DEFAULT 0,
			blocks_count INTEGER DEFAULT 0,
			blocked_by_count INTEGER DEFAULT 0,
			FOREIGN KEY (issue_id) REFERENCES issues(id)
		)
	`
	if _, err := tx.Exec(metricsSQL); err != nil {
		return fmt.Errorf("create issue_metrics table: %w", err)
	}

	// Triage recommendations pre-computed
	triageSQL := `
		CREATE TABLE IF NOT EXISTS triage_recommendations (
			issue_id TEXT PRIMARY KEY,
			score REAL NOT NULL,
			action TEXT NOT NULL,
			reasons TEXT,
			unblocks_ids TEXT,
			blocked_by_ids TEXT,
			FOREIGN KEY (issue_id) REFERENCES issues(id)
		)
	`
	if _, err := tx.Exec(triageSQL); err != nil {
		return fmt.Errorf("create triage_recommendations table: %w", err)
	}

	return nil
}

// createIndexes creates performance indexes for common queries.
func createIndexes(tx *sql.Tx) error {
	indexes := []string{
		// Issues indexes
		`CREATE INDEX IF NOT EXISTS idx_issues_status ON issues(status)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_priority ON issues(priority, status)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_updated ON issues(updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_type_status ON issues(issue_type, status)`,

		// Dependencies indexes
		`CREATE INDEX IF NOT EXISTS idx_deps_issue ON dependencies(issue_id)`,
		`CREATE INDEX IF NOT EXISTS idx_deps_depends ON dependencies(depends_on_id)`,
		`CREATE INDEX IF NOT EXISTS idx_deps_type ON dependencies(type)`,

		// Comments indexes (bv-52)
		`CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_id)`,
		`CREATE INDEX IF NOT EXISTS idx_comments_created ON comments(created_at DESC)`,

		// Metrics indexes
		`CREATE INDEX IF NOT EXISTS idx_metrics_score ON issue_metrics(triage_score DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_metrics_pagerank ON issue_metrics(pagerank DESC)`,
	}

	for _, sql := range indexes {
		if _, err := tx.Exec(sql); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}

	return nil
}

// createMetaTable creates the export metadata table.
func createMetaTable(tx *sql.Tx) error {
	metaSQL := `
		CREATE TABLE IF NOT EXISTS export_meta (
			key TEXT PRIMARY KEY,
			value TEXT
		)
	`
	if _, err := tx.Exec(metaSQL); err != nil {
		return fmt.Errorf("create export_meta table: %w", err)
	}

	return nil
}

// CreateFTSIndex creates the FTS5 full-text search virtual table.
// This must be called after issues are inserted.
func CreateFTSIndex(db *sql.DB) error {
	// Create FTS5 virtual table for full-text search
	ftsSQL := `
		CREATE VIRTUAL TABLE IF NOT EXISTS issues_fts USING fts5(
			id,
			title,
			description,
			labels,
			assignee,
			content='issues',
			content_rowid='rowid',
			tokenize='porter unicode61'
		)
	`
	if _, err := db.Exec(ftsSQL); err != nil {
		return fmt.Errorf("create FTS5 table: %w", err)
	}

	// Populate FTS index from issues table
	populateSQL := `
		INSERT INTO issues_fts(issues_fts) VALUES('rebuild')
	`
	if _, err := db.Exec(populateSQL); err != nil {
		return fmt.Errorf("populate FTS index: %w", err)
	}

	return nil
}

// CreateMaterializedViews creates denormalized views for fast queries.
// This must be called after all data is inserted.
func CreateMaterializedViews(db *sql.DB) error {
	// Issue overview materialized view - denormalized for fast list queries
	overviewSQL := `
		CREATE TABLE IF NOT EXISTS issue_overview_mv AS
		SELECT
			i.id,
			i.title,
			i.description,
			i.design,
			i.acceptance_criteria,
			i.notes,
			i.status,
			i.priority,
			i.issue_type,
			i.assignee,
			i.labels,
			i.created_at,
			i.updated_at,
			i.closed_at,
			COALESCE(m.pagerank, 0) as pagerank,
			COALESCE(m.betweenness, 0) as betweenness,
			COALESCE(m.critical_path_depth, 0) as critical_path_depth,
			COALESCE(m.triage_score, 0) as triage_score,
			COALESCE(m.blocks_count, 0) as blocks_count,
			COALESCE(m.blocked_by_count, 0) as blocked_by_count,
			COALESCE(m.blocked_by_count, 0) as blocker_count,
			COALESCE(m.blocks_count, 0) as dependent_count,
			COALESCE(m.critical_path_depth, 0) as critical_depth,
			0 as in_cycle,
			-- Comment count for display badges (bv-52)
			(SELECT COUNT(*) FROM comments c WHERE c.issue_id = i.id) as comment_count,
				-- dep.IssueID depends on dep.DependsOnID, so:
				-- - blocks_ids are the issues that depend on i (downstream)
				-- - blocked_by_ids are the issues i depends on (upstream)
				-- Both lists are filtered to ACTIVE endpoints so closing a
				-- blocker actually unblocks its dependents in this view
				-- (bv-issue#143/#144).
				(SELECT GROUP_CONCAT(issue_id) FROM (
					SELECT d.issue_id AS issue_id
					FROM dependencies d
					JOIN issues i2 ON d.issue_id = i2.id
					WHERE d.depends_on_id = i.id
					  AND (d.type = 'blocks' OR d.type = '')
					  AND i2.status NOT IN ('closed', 'tombstone')
					ORDER BY d.issue_id
				)) as blocks_ids,
				(SELECT GROUP_CONCAT(depends_on_id) FROM (
					SELECT d.depends_on_id AS depends_on_id
					FROM dependencies d
					JOIN issues i2 ON d.depends_on_id = i2.id
					WHERE d.issue_id = i.id
					  AND (d.type = 'blocks' OR d.type = '')
					  AND i2.status NOT IN ('closed', 'tombstone')
					ORDER BY d.depends_on_id
				)) as blocked_by_ids
			FROM issues i
			LEFT JOIN issue_metrics m ON i.id = m.issue_id
		`
	if _, err := db.Exec(overviewSQL); err != nil {
		return fmt.Errorf("create issue_overview_mv: %w", err)
	}

	// Create index on the materialized view
	mvIndexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_mv_status ON issue_overview_mv(status)`,
		`CREATE INDEX IF NOT EXISTS idx_mv_priority ON issue_overview_mv(priority)`,
		`CREATE INDEX IF NOT EXISTS idx_mv_score ON issue_overview_mv(triage_score DESC)`,
	}

	for _, sql := range mvIndexes {
		if _, err := db.Exec(sql); err != nil {
			return fmt.Errorf("create mv index: %w", err)
		}
	}

	return nil
}

// OptimizeDatabase runs optimizations for httpvfs streaming.
// Call this as the final step before closing the database.
func OptimizeDatabase(db *sql.DB, pageSize int) error {
	if pageSize <= 0 {
		pageSize = 1024 // Default optimal for httpvfs
	}

	optimizations := []string{
		// Single file mode (no WAL journal)
		`PRAGMA journal_mode=DELETE`,
		// Optimal page size for range requests
		fmt.Sprintf(`PRAGMA page_size=%d`, pageSize),
		// Analyze for query optimizer
		`ANALYZE`,
		// Run optimizer recommendations
		`PRAGMA optimize`,
	}

	for _, sql := range optimizations {
		if _, err := db.Exec(sql); err != nil {
			// Some pragmas may fail depending on state, continue
			continue
		}
	}

	// Try to optimize FTS index if it exists (may not be available in all SQLite builds)
	_, _ = db.Exec(`INSERT INTO issues_fts(issues_fts) VALUES('optimize')`)

	// VACUUM must be last and outside transaction
	if _, err := db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}

	return nil
}

// InsertMetaValue inserts or updates a metadata key-value pair.
func InsertMetaValue(db *sql.DB, key, value string) error {
	sql := `INSERT OR REPLACE INTO export_meta (key, value) VALUES (?, ?)`
	_, err := db.Exec(sql, key, value)
	return err
}
