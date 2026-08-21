package datasource

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// SQLiteReader provides read access to a beads SQLite database
type SQLiteReader struct {
	db   *sql.DB
	path string
}

// NewSQLiteReader opens a SQLite database for reading
func NewSQLiteReader(source DataSource) (*SQLiteReader, error) {
	if source.Type != SourceTypeSQLite {
		return nil, fmt.Errorf("source is not SQLite: %s", source.Type)
	}
	if _, err := os.Stat(source.Path); err != nil {
		return nil, fmt.Errorf("cannot access database: %w", err)
	}

	// Open in read-only mode with various pragmas for read performance.
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(source.Path))
	if err != nil {
		return nil, fmt.Errorf("cannot open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot connect to database: %w", err)
	}

	// Set pragmas for read performance
	pragmas := []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA cache_size = -64000",   // 64MB cache
		"PRAGMA mmap_size = 268435456", // 256MB mmap
		"PRAGMA temp_store = MEMORY",
		"PRAGMA query_only = ON",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			// Non-fatal, just log
		}
	}

	return &SQLiteReader{
		db:   db,
		path: source.Path,
	}, nil
}

func sqliteReadOnlyDSN(path string) string {
	return sqliteFileDSN(path, "mode=ro")
}

func sqliteFileDSN(path, rawQuery string) string {
	u := url.URL{Scheme: "file", Path: path, RawQuery: rawQuery}
	return u.String()
}

// Close closes the database connection
func (r *SQLiteReader) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

// hasLabelsColumn checks whether the issues table has a "labels" column.
// beads-rs (br) stores labels in a separate table instead.
func (r *SQLiteReader) hasLabelsColumn() bool {
	rows, err := r.db.Query("PRAGMA table_info(issues)")
	if err != nil {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			continue
		}
		if strings.EqualFold(name, "labels") {
			return true
		}
	}
	return false
}

func (r *SQLiteReader) issuesColumns() map[string]bool {
	return r.tableColumns("issues")
}

func (r *SQLiteReader) tableColumns(table string) map[string]bool {
	var query string
	switch table {
	case "dependencies":
		query = "PRAGMA table_info(dependencies)"
	case "issues":
		query = "PRAGMA table_info(issues)"
	default:
		return map[string]bool{}
	}

	rows, err := r.db.Query(query)
	if err != nil {
		return map[string]bool{}
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			continue
		}
		columns[strings.ToLower(name)] = true
	}
	return columns
}

// hasLabelsTable checks whether a separate "labels" table exists.
// beads-rs (br) databases use this schema instead of a JSON column on issues.
func (r *SQLiteReader) hasLabelsTable() bool {
	var name string
	err := r.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='labels'").Scan(&name)
	return err == nil && name == "labels"
}

// LoadIssues reads all issues from the database
func (r *SQLiteReader) LoadIssues() ([]model.Issue, error) {
	return r.LoadIssuesFiltered(nil)
}

// LoadIssuesFiltered reads issues matching the filter function
func (r *SQLiteReader) LoadIssuesFiltered(filter func(*model.Issue) bool) ([]model.Issue, error) {
	columns := r.issuesColumns()
	deferUntilExpr := "NULL"
	if columns["defer_until"] {
		deferUntilExpr = "i.defer_until"
	}

	// Detect schema: beads-rs (br) databases store labels in a separate
	// "labels" table rather than a JSON column on "issues". We substitute
	// a subquery so that labels are loaded transparently.
	labelsExpr := "i.labels"
	if !r.hasLabelsColumn() && r.hasLabelsTable() {
		labelsExpr = "(SELECT json_group_array(label) FROM labels WHERE issue_id = i.id)"
	}

	// Query for all non-tombstone issues. Use table alias "i" to avoid
	// column ambiguity when a labels subquery references issue_id.
	query := fmt.Sprintf(`
		SELECT
			i.id, i.title, i.description, i.status, i.priority, i.issue_type,
			i.assignee, i.estimated_minutes, i.created_at, i.updated_at,
			i.due_date, %s, i.closed_at, i.external_ref, i.compaction_level,
			i.compacted_at, i.compacted_at_commit, i.original_size,
			%s, i.design, i.acceptance_criteria, i.notes, i.source_repo
		FROM issues i
		WHERE (i.tombstone IS NULL OR i.tombstone = 0)
		ORDER BY i.updated_at DESC
	`, deferUntilExpr, labelsExpr)

	rows, err := r.db.Query(query)
	if err != nil {
		// Try simpler query if some columns don't exist
		return r.loadIssuesSimple(filter)
	}
	defer rows.Close()

	var issues []model.Issue
	for rows.Next() {
		var issue model.Issue
		var estimatedMinutes, compactionLevel, originalSize sql.NullInt64
		var createdAt, updatedAt, dueDate, deferUntil, closedAt, compactedAt sql.NullTime
		var description, assignee, externalRef, design, acceptanceCriteria, notes, sourceRepo, compactedAtCommit sql.NullString
		var labelsJSON sql.NullString
		var issueType string

		err := rows.Scan(
			&issue.ID, &issue.Title, &description, &issue.Status, &issue.Priority, &issueType,
			&assignee, &estimatedMinutes, &createdAt, &updatedAt,
			&dueDate, &deferUntil, &closedAt, &externalRef, &compactionLevel,
			&compactedAt, &compactedAtCommit, &originalSize,
			&labelsJSON, &design, &acceptanceCriteria, &notes, &sourceRepo,
		)
		if err != nil {
			continue
		}

		// Map nullable fields
		if description.Valid {
			issue.Description = description.String
		}
		issue.IssueType = model.IssueType(issueType)
		if assignee.Valid {
			issue.Assignee = assignee.String
		}
		if estimatedMinutes.Valid {
			v := int(estimatedMinutes.Int64)
			issue.EstimatedMinutes = &v
		}
		if createdAt.Valid {
			issue.CreatedAt = createdAt.Time
		}
		if updatedAt.Valid {
			issue.UpdatedAt = updatedAt.Time
		}
		if dueDate.Valid {
			t := dueDate.Time
			issue.DueDate = &t
		}
		if deferUntil.Valid {
			t := deferUntil.Time
			issue.DeferUntil = &t
		}
		if closedAt.Valid {
			t := closedAt.Time
			issue.ClosedAt = &t
		}
		if externalRef.Valid {
			s := externalRef.String
			issue.ExternalRef = &s
		}
		if compactionLevel.Valid {
			issue.CompactionLevel = int(compactionLevel.Int64)
		}
		if compactedAt.Valid {
			t := compactedAt.Time
			issue.CompactedAt = &t
		}
		if compactedAtCommit.Valid {
			s := compactedAtCommit.String
			issue.CompactedAtCommit = &s
		}
		if originalSize.Valid {
			issue.OriginalSize = int(originalSize.Int64)
		}
		if design.Valid {
			issue.Design = design.String
		}
		if acceptanceCriteria.Valid {
			issue.AcceptanceCriteria = acceptanceCriteria.String
		}
		if notes.Valid {
			issue.Notes = notes.String
		}
		if sourceRepo.Valid {
			issue.SourceRepo = sourceRepo.String
		}

		// Parse labels JSON array
		if labelsJSON.Valid && labelsJSON.String != "" && labelsJSON.String != "null" {
			labels := parseJSONStringArray(labelsJSON.String)
			issue.Labels = labels
		}

		// Load dependencies for this issue
		issue.Dependencies = r.loadDependencies(issue.ID)

		// Load comments for this issue
		issue.Comments = r.loadComments(issue.ID)

		// Apply filter
		if filter != nil && !filter(&issue) {
			continue
		}

		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating issues: %w", err)
	}

	return issues, nil
}

// loadIssuesSimple is a fallback for databases with fewer columns
func (r *SQLiteReader) loadIssuesSimple(filter func(*model.Issue) bool) ([]model.Issue, error) {
	columns := r.issuesColumns()
	expr := func(name, fallback string) string {
		if columns[name] {
			return name
		}
		return fallback
	}
	coalesceExpr := func(name, fallback string) string {
		if columns[name] {
			return fmt.Sprintf("COALESCE(%s, %s)", name, fallback)
		}
		return fallback
	}
	where := ""
	if columns["tombstone"] {
		where = "WHERE (tombstone IS NULL OR tombstone = 0)"
	}
	orderBy := "ORDER BY id"
	if columns["updated_at"] {
		orderBy = "ORDER BY updated_at DESC"
	}
	query := fmt.Sprintf(`
		SELECT id, title, %s, status, %s, %s, %s, %s, %s, %s, %s
		FROM issues
		%s
		%s
	`,
		expr("description", "NULL"),
		coalesceExpr("priority", "3"),
		coalesceExpr("issue_type", "'task'"),
		expr("assignee", "NULL"),
		expr("created_at", "NULL"),
		expr("updated_at", "NULL"),
		expr("defer_until", "NULL"),
		expr("labels", "NULL"),
		where,
		orderBy,
	)

	rows, err := r.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	// Check once whether a separate labels table exists (beads-rs schema)
	separateLabels := r.hasLabelsTable()

	var issues []model.Issue
	for rows.Next() {
		var issue model.Issue
		var description, assignee sql.NullString
		var createdAt, updatedAt, deferUntil sql.NullString
		var labelsJSON sql.NullString
		var issueType string

		err := rows.Scan(
			&issue.ID, &issue.Title, &description, &issue.Status, &issue.Priority, &issueType,
			&assignee, &createdAt, &updatedAt, &deferUntil, &labelsJSON,
		)
		if err != nil {
			continue
		}

		if description.Valid {
			issue.Description = description.String
		}
		issue.IssueType = model.IssueType(issueType)
		if assignee.Valid {
			issue.Assignee = assignee.String
		}
		if createdAt.Valid {
			if t, ok := parseSQLiteTime(createdAt.String); ok {
				issue.CreatedAt = t
			}
		}
		if updatedAt.Valid {
			if t, ok := parseSQLiteTime(updatedAt.String); ok {
				issue.UpdatedAt = t
			}
		}
		if deferUntil.Valid {
			if t, ok := parseSQLiteTime(deferUntil.String); ok {
				issue.DeferUntil = &t
			}
		}
		if labelsJSON.Valid && labelsJSON.String != "" && labelsJSON.String != "null" {
			issue.Labels = parseJSONStringArray(labelsJSON.String)
		}

		// Load labels from separate table if present (beads-rs compatibility)
		if separateLabels && len(issue.Labels) == 0 {
			issue.Labels = r.loadLabelsFromTable(issue.ID)
		}

		issue.Dependencies = r.loadDependencies(issue.ID)
		issue.Comments = r.loadComments(issue.ID)

		if filter != nil && !filter(&issue) {
			continue
		}

		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating issues: %w", err)
	}

	return issues, nil
}

// loadLabelsFromTable loads labels for an issue from the separate labels table
// used by beads-rs (br) databases.
func (r *SQLiteReader) loadLabelsFromTable(issueID string) []string {
	rows, err := r.db.Query("SELECT label FROM labels WHERE issue_id = ?", issueID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			continue
		}
		labels = append(labels, label)
	}
	// Best-effort: log iteration errors but return what we have
	if err := rows.Err(); err != nil {
		// Labels are non-critical metadata; return partial results
		_ = err
	}
	return labels
}

// loadDependencies loads dependencies for an issue
func (r *SQLiteReader) loadDependencies(issueID string) []*model.Dependency {
	dependencyTypeExpr := r.dependencyTypeExpr()
	query := fmt.Sprintf(`SELECT depends_on_id, %s FROM dependencies WHERE issue_id = ?`, dependencyTypeExpr)
	rows, err := r.db.Query(query, issueID)
	if err != nil {
		return []*model.Dependency{}
	}
	defer rows.Close()

	var deps []*model.Dependency
	for rows.Next() {
		var dep model.Dependency
		var depType string
		if err := rows.Scan(&dep.DependsOnID, &depType); err != nil {
			continue
		}
		dep.IssueID = issueID
		dep.Type = model.DependencyType(depType)
		deps = append(deps, &dep)
	}
	// Note: rows.Err() not checked here since loadDependencies is a
	// best-effort helper that returns an empty slice on any error.
	return deps
}

func (r *SQLiteReader) dependencyTypeExpr() string {
	columns := r.tableColumns("dependencies")
	switch {
	case columns["dependency_type"]:
		return "dependency_type"
	case columns["type"]:
		return "type"
	default:
		return "''"
	}
}

// loadComments loads comments for an issue
func (r *SQLiteReader) loadComments(issueID string) []*model.Comment {
	query := `SELECT id, author, text, created_at FROM comments WHERE issue_id = ? ORDER BY created_at`
	rows, err := r.db.Query(query, issueID)
	if err != nil {
		return []*model.Comment{}
	}
	defer rows.Close()

	var comments []*model.Comment
	for rows.Next() {
		var comment model.Comment
		var createdAt sql.NullString
		if err := rows.Scan(&comment.ID, &comment.Author, &comment.Text, &createdAt); err != nil {
			continue
		}
		if createdAt.Valid {
			if t, ok := parseSQLiteTime(createdAt.String); ok {
				comment.CreatedAt = t
			}
		}
		comment.IssueID = issueID
		comments = append(comments, &comment)
	}
	// Note: rows.Err() not checked here since loadComments is a
	// best-effort helper that returns an empty slice on any error.
	return comments
}

// CountIssues returns the count of non-tombstone issues
func (r *SQLiteReader) CountIssues() (int, error) {
	var count int
	query := "SELECT COUNT(*) FROM issues"
	if r.issuesColumns()["tombstone"] {
		query += " WHERE (tombstone IS NULL OR tombstone = 0)"
	}
	err := r.db.QueryRow(query).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetIssueByID retrieves a single issue by ID
func (r *SQLiteReader) GetIssueByID(id string) (*model.Issue, error) {
	issues, err := r.LoadIssuesFiltered(func(issue *model.Issue) bool {
		return issue.ID == id
	})
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, fmt.Errorf("issue not found: %s", id)
	}
	return &issues[0], nil
}

// GetLastModified returns the most recent update time.
// modernc.org/sqlite stores DATETIME columns as text, so we scan as string
// and parse manually.
func (r *SQLiteReader) GetLastModified() (time.Time, error) {
	if !r.issuesColumns()["updated_at"] {
		return time.Time{}, nil
	}
	var raw sql.NullString
	err := r.db.QueryRow("SELECT MAX(updated_at) FROM issues").Scan(&raw)
	if err != nil {
		return time.Time{}, err
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, nil
	}
	if t, ok := parseSQLiteTime(raw.String); ok {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse updated_at %q", raw.String)
}

func parseSQLiteTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05-07:00",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseJSONStringArray parses a JSON array of strings
func parseJSONStringArray(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" || s == "[]" {
		return nil
	}

	// Use proper JSON unmarshaling to handle edge cases like commas in labels
	var result []string
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		// Fallback to simple parser for malformed JSON
		s = strings.TrimPrefix(s, "[")
		s = strings.TrimSuffix(s, "]")
		if s == "" {
			return nil
		}
		for _, item := range strings.Split(s, ",") {
			item = strings.TrimSpace(item)
			item = strings.Trim(item, `"`)
			if item != "" {
				result = append(result, item)
			}
		}
	}
	return result
}
