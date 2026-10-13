package datasource

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// SQLiteReader provides read access to a beads SQLite database
type SQLiteReader struct {
	db       *sql.DB
	path     string
	reportMu sync.Mutex
	report   LoadReport
}

type sqliteReadSchema struct {
	dependencyType string
	labelsTable    bool
	commentsTable  bool
}

// Every query that contributes to one issue graph must use the same WAL
// snapshot. Keep the transaction on a separate, load-local receiver so a
// helper cannot accidentally borrow a different connection from the pool.
// The persistent SQLiteReader never retains a transaction between refreshes.
type sqliteReadSession struct {
	db *sql.Tx
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

	return &SQLiteReader{
		db:   db,
		path: source.Path,
	}, nil
}

func sqliteReadOnlyDSN(path string) string {
	query := url.Values{}
	query.Set("mode", "ro")
	// Read-only SQLite still maintains the shared WAL index and reader locks
	// (br GH#555). Do not use immutable=1 or restore -shm bytes: the tracker
	// remains live, and WAL commits and concurrent readers must stay visible.
	// SAFETY: SQLite PRAGMAs are connection-local. database/sql may open new
	// pooled connections after NewSQLiteReader returns, so configuring one
	// borrowed connection with db.Exec does not configure the pool. modernc's
	// repeated _pragma DSN parameter applies each setting to every connection.
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "cache_size(-64000)")   // 64MB cache
	query.Add("_pragma", "mmap_size(268435456)") // 256MB mmap
	query.Add("_pragma", "temp_store(MEMORY)")
	query.Add("_pragma", "query_only(1)")
	return sqliteFileDSN(path, query.Encode())
}

func sqliteFileDSN(path, rawQuery string) string {
	u := url.URL{Scheme: "file", Path: sqliteURIPath(path, runtime.GOOS == "windows"), RawQuery: rawQuery}
	return u.String()
}

// sqliteURIPath converts a filesystem path into the path component of a
// "file:" URI that SQLite maps back onto the same file.
//
// url.URL.String() always emits "file://" + path, so the first path segment
// lands in the URI *authority* position unless the path starts with "/".
// SQLite only accepts an empty authority (or "localhost") and rejects
// anything else with "invalid uri authority: ...". Two path shapes hit that:
//
//   - Windows drive paths: `E:\repo\.beads\beads.db` became
//     `file://E:%5Crepo%5C...` (bv #198). SQLite's documented Windows form
//     is `file:///E:/repo/.beads/beads.db`, so backslashes are converted to
//     forward slashes and the drive letter is prefixed with "/".
//   - Relative paths: `.beads/beads.db` became `file://.beads/beads.db`.
//     They are made absolute first so the URI path is always rooted.
//
// UNC paths (`\\server\share\x.db`) come out as `file:////server/share/x.db`:
// empty authority, path `//server/share/x.db`, which the Windows file APIs
// accept with forward slashes.
//
// windows selects the Windows path rules explicitly so the conversion is
// unit-testable on every platform.
func sqliteURIPath(path string, windows bool) string {
	if !isRootedPath(path, windows) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	if windows {
		path = strings.ReplaceAll(path, `\`, "/")
		if hasDriveLetter(path) {
			path = "/" + path
		}
	}
	return path
}

func isRootedPath(path string, windows bool) bool {
	if strings.HasPrefix(path, "/") {
		return true
	}
	if !windows {
		return false
	}
	return strings.HasPrefix(path, `\`) || hasDriveLetter(path)
}

// hasDriveLetter reports whether path starts with a Windows drive
// designator such as "E:" (with or without a following separator).
func hasDriveLetter(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	c := path[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// Close closes the database connection
func (r *SQLiteReader) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

func (r *sqliteReadSession) issuesColumns() (map[string]bool, error) {
	return r.tableColumns("issues")
}

func (r *sqliteReadSession) tableColumns(table string) (map[string]bool, error) {
	var query string
	switch table {
	case "dependencies":
		query = "PRAGMA table_info(dependencies)"
	case "issues":
		query = "PRAGMA table_info(issues)"
	default:
		return nil, fmt.Errorf("unsupported schema table %q", table)
	}

	rows, err := r.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("inspecting %s columns: %w", table, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("reading %s column: %w", table, err)
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating %s columns: %w", table, err)
	}
	return columns, nil
}

// LoadIssues reads all issues from the database
func (r *SQLiteReader) LoadIssues() ([]model.Issue, error) {
	return r.LoadIssuesFiltered(nil)
}

// LoadIssuesFiltered reads issues matching the filter function
func (r *SQLiteReader) LoadIssuesFiltered(filter func(*model.Issue) bool) ([]model.Issue, error) {
	all, err := r.LoadIssueAuthority()
	if err != nil {
		return nil, err
	}
	out := make([]model.Issue, 0, len(all))
	for _, issue := range all {
		if !issue.Status.IsTombstone() && (filter == nil || filter(&issue)) {
			out = append(out, issue)
		}
	}
	return out, nil
}

// LoadIssueAuthority includes tombstones so a snapshot can distinguish a
// satisfied predecessor from a missing record. Display readers filter them
// only after retaining this dependency authority.
func (r *SQLiteReader) LoadIssueAuthority() ([]model.Issue, error) {
	report := LoadReport{Path: r.path}
	issues, err := r.readIssueSnapshot(&report)
	seen := make(map[string]bool, len(issues))
	unique := make([]model.Issue, 0, len(issues))
	for _, issue := range issues {
		if err := issue.Validate(); err != nil {
			report.Errors++
			report.addWarning(fmt.Sprintf("invalid issue %q: %v", issue.ID, err))
			continue
		}
		if seen[issue.ID] {
			report.Errors++
			report.addWarning(fmt.Sprintf("duplicate issue ID %q", issue.ID))
			continue
		}
		seen[issue.ID] = true
		unique = append(unique, issue)
	}
	issues = unique
	report.Valid = len(issues)
	report.TombstoneIDs = tombstoneIDs(issues)
	r.reportMu.Lock()
	r.report = report
	r.reportMu.Unlock()
	if err != nil {
		return nil, err
	}
	return issues, nil
}

func (r *SQLiteReader) LoadReport() LoadReport {
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	return cloneLoadReport(r.report)
}

func (r *SQLiteReader) readIssueSnapshot(report *LoadReport) ([]model.Issue, error) {
	// mode=ro/query_only are already set on every pooled connection. BEGIN
	// is deferred, not IMMEDIATE: this pins a read snapshot, not a writer lock.
	tx, err := r.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("beginning SQLite read snapshot: %w", err)
	}
	defer tx.Rollback()
	session := sqliteReadSession{db: tx}
	issues, err := session.loadIssueAuthority(report)
	if err != nil {
		return nil, err
	}
	// Release the reader mark before validation, filtering, graph analysis or
	// the TUI wait loop. The next load must acquire a fresh committed snapshot.
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("ending SQLite read snapshot: %w", err)
	}
	return issues, nil
}

func (r *sqliteReadSession) loadIssueAuthority(report *LoadReport) ([]model.Issue, error) {
	columns, err := r.issuesColumns()
	if err != nil {
		return nil, err
	}
	dependencyTypeExpr, dependencyErr := r.dependencyTypeExpr()
	if dependencyErr != nil {
		report.ReadErrors++
		report.addWarning(dependencyErr.Error())
	}
	schema := sqliteReadSchema{dependencyType: dependencyTypeExpr}
	for _, table := range []struct {
		name        string
		destination *bool
	}{{"labels", &schema.labelsTable}, {"comments", &schema.commentsTable}} {
		exists, err := r.tableExists(table.name)
		if err != nil {
			report.ReadErrors++
			report.addWarning(fmt.Sprintf("inspecting %s table: %v", table.name, err))
		}
		*table.destination = exists
	}
	// Select every available field independently. A missing optional column
	// must not downgrade the entire issue to a reduced, lossy fallback query.
	// All identifiers below are hard-coded, never interpolated from schema data.
	expr := func(name, fallback string) string {
		if columns[name] {
			return "i." + name
		}
		return fallback
	}
	coalesceExpr := func(name, fallback string) string {
		if columns[name] {
			return fmt.Sprintf("COALESCE(i.%s, %s)", name, fallback)
		}
		return fallback
	}
	// br's native deadline is due_at; older viewer/Go snapshots use due_date.
	// A present due_at column is authoritative even when this row is NULL.
	dueField := "due_date"
	if columns["due_at"] {
		dueField = "due_at"
	}
	fields := []string{
		"i.id", "i.title", expr("description", "NULL"), "i.status",
		coalesceExpr("priority", "3"), coalesceExpr("issue_type", "'task'"),
		expr("assignee", "NULL"), expr("estimated_minutes", "NULL"),
		expr("created_at", "NULL"), expr("updated_at", "NULL"),
		expr(dueField, "NULL"), expr("defer_until", "NULL"), expr("closed_at", "NULL"),
		expr("external_ref", "NULL"), expr("compaction_level", "NULL"),
		expr("compacted_at", "NULL"), expr("compacted_at_commit", "NULL"),
		expr("original_size", "NULL"), expr("labels", "NULL"), expr("design", "NULL"),
		expr("acceptance_criteria", "NULL"), expr("notes", "NULL"),
		expr("source_repo", "NULL"), expr("tombstone", "NULL"),
	}
	orderBy := "i.id"
	if columns["updated_at"] {
		orderBy = "i.updated_at DESC, i.id"
	}
	// Include tombstones: dependencies must retain their source authority.
	query := "SELECT " + strings.Join(fields, ", ") + " FROM issues i ORDER BY " + orderBy

	rows, err := r.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var issues []model.Issue
	for rows.Next() {
		var issue model.Issue
		var estimatedMinutes, compactionLevel, originalSize sql.NullInt64
		// modernc returns time.Time for DATETIME and strings for TEXT or
		// expressions. NullString accepts both, unlike NullTime on TEXT.
		var createdAt, updatedAt, dueDate, deferUntil, closedAt, compactedAt sql.NullString
		var description, assignee, externalRef, design, acceptanceCriteria, notes, sourceRepo, compactedAtCommit sql.NullString
		var labelsJSON sql.NullString
		var issueType string
		var tombstone sql.NullInt64

		err := rows.Scan(
			&issue.ID, &issue.Title, &description, &issue.Status, &issue.Priority, &issueType,
			&assignee, &estimatedMinutes, &createdAt, &updatedAt,
			&dueDate, &deferUntil, &closedAt, &externalRef, &compactionLevel,
			&compactedAt, &compactedAtCommit, &originalSize,
			&labelsJSON, &design, &acceptanceCriteria, &notes, &sourceRepo, &tombstone,
		)
		if err != nil {
			report.Errors++
			report.addWarning(fmt.Sprintf("reading issue row: %v", err))
			continue
		}
		if tombstone.Valid && tombstone.Int64 != 0 {
			issue.Status = model.StatusTombstone
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
		if value := sqliteIssueTime(createdAt, issue.ID, "created_at", report); value != nil {
			issue.CreatedAt = *value
		}
		if value := sqliteIssueTime(updatedAt, issue.ID, "updated_at", report); value != nil {
			issue.UpdatedAt = *value
		}
		issue.DueDate = sqliteIssueTime(dueDate, issue.ID, dueField, report)
		issue.DeferUntil = sqliteIssueTime(deferUntil, issue.ID, "defer_until", report)
		issue.ClosedAt = sqliteIssueTime(closedAt, issue.ID, "closed_at", report)
		if externalRef.Valid {
			s := externalRef.String
			issue.ExternalRef = &s
		}
		if compactionLevel.Valid {
			issue.CompactionLevel = int(compactionLevel.Int64)
		}
		issue.CompactedAt = sqliteIssueTime(compactedAt, issue.ID, "compacted_at", report)
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
			issue.Labels = parseSQLiteLabels(labelsJSON.String, issue.ID, report)
		}
		// Read table labels as rows, not a JSON aggregate that can turn a
		// malformed NULL label into an apparently valid empty string.
		if schema.labelsTable && len(issue.Labels) == 0 {
			issue.Labels = r.loadLabelsFromTable(issue.ID, report)
		}

		// Load dependencies for this issue
		issue.Dependencies = r.loadDependencies(issue.ID, dependencyTypeExpr, report)

		// Load comments for this issue
		if schema.commentsTable {
			issue.Comments = r.loadComments(issue.ID, report)
		}

		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating issues: %w", err)
	}

	return issues, nil
}

func sqliteIssueTime(raw sql.NullString, issueID, field string, report *LoadReport) *time.Time {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	if value, ok := parseSQLiteTime(raw.String); ok {
		return &value
	}
	report.ReadErrors++
	report.addWarning(fmt.Sprintf("issue %q has invalid %s %q", issueID, field, raw.String))
	return nil
}

// loadLabelsFromTable loads labels for an issue from the separate labels table
// used by beads-rs (br) databases.
func (r *sqliteReadSession) loadLabelsFromTable(issueID string, report *LoadReport) []string {
	rows, err := r.db.Query("SELECT label FROM labels WHERE issue_id = ?", issueID)
	if err != nil {
		report.ReadErrors++
		report.addWarning(fmt.Sprintf("reading labels for %q: %v", issueID, err))
		return []string{}
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			report.ReadErrors++
			report.addWarning(fmt.Sprintf("reading label for %q: %v", issueID, err))
			continue
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		report.ReadErrors++
		report.addWarning(fmt.Sprintf("iterating labels for %q: %v", issueID, err))
	}
	return labels
}

// loadDependencies loads dependencies for an issue
func (r *sqliteReadSession) loadDependencies(issueID, dependencyTypeExpr string, report *LoadReport) []*model.Dependency {
	if dependencyTypeExpr == "" {
		return []*model.Dependency{}
	}
	query := fmt.Sprintf(`SELECT depends_on_id, %s FROM dependencies WHERE issue_id = ?`, dependencyTypeExpr)
	rows, err := r.db.Query(query, issueID)
	if err != nil {
		report.ReadErrors++
		report.addWarning(fmt.Sprintf("reading dependencies for %q: %v", issueID, err))
		return []*model.Dependency{}
	}
	defer rows.Close()

	var deps []*model.Dependency
	for rows.Next() {
		var dep model.Dependency
		var depType string
		if err := rows.Scan(&dep.DependsOnID, &depType); err != nil {
			report.ReadErrors++
			report.addWarning(fmt.Sprintf("reading dependency for %q: %v", issueID, err))
			continue
		}
		dep.IssueID = issueID
		dep.Type = model.DependencyType(depType)
		if strings.TrimSpace(dep.DependsOnID) == "" || (dep.Type != "" && !dep.Type.IsValid()) {
			report.ReadErrors++
			report.addWarning(fmt.Sprintf("invalid dependency for %q: target %q, type %q", issueID, dep.DependsOnID, depType))
			continue
		}
		deps = append(deps, &dep)
	}
	if err := rows.Err(); err != nil {
		report.ReadErrors++
		report.addWarning(fmt.Sprintf("iterating dependencies for %q: %v", issueID, err))
	}
	return deps
}

func (r *sqliteReadSession) tableExists(name string) (bool, error) {
	var exists int
	err := r.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view') AND name = ?", name).Scan(&exists)
	return exists > 0, err
}

func (r *sqliteReadSession) dependencyTypeExpr() (string, error) {
	// Minimal snapshots may omit this optional table. Inspect it once per load,
	// so missing schema and unreadable schema do not become the same authority.
	exists, err := r.tableExists("dependencies")
	if err != nil {
		return "", fmt.Errorf("inspecting dependency table: %w", err)
	}
	if !exists {
		return "", nil
	}
	columns, err := r.tableColumns("dependencies")
	if err != nil {
		return "", err
	}
	if !columns["issue_id"] || !columns["depends_on_id"] {
		return "", fmt.Errorf("dependency table is missing issue_id or depends_on_id")
	}
	switch {
	case columns["dependency_type"]:
		return "dependency_type", nil
	case columns["type"]:
		return "type", nil
	default:
		// Older snapshots omit the type column. An empty type retains the
		// model's legacy blocking semantics instead of discarding the edge.
		return "''", nil
	}
}

// loadComments loads comments for an issue
func (r *sqliteReadSession) loadComments(issueID string, report *LoadReport) []*model.Comment {
	query := `SELECT id, author, text, created_at FROM comments WHERE issue_id = ? ORDER BY created_at`
	rows, err := r.db.Query(query, issueID)
	if err != nil {
		report.ReadErrors++
		report.addWarning(fmt.Sprintf("reading comments for %q: %v", issueID, err))
		return []*model.Comment{}
	}
	defer rows.Close()

	var comments []*model.Comment
	for rows.Next() {
		var comment model.Comment
		var createdAt sql.NullString
		if err := rows.Scan(&comment.ID, &comment.Author, &comment.Text, &createdAt); err != nil {
			report.ReadErrors++
			report.addWarning(fmt.Sprintf("reading comment for %q: %v", issueID, err))
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
	if err := rows.Err(); err != nil {
		report.ReadErrors++
		report.addWarning(fmt.Sprintf("iterating comments for %q: %v", issueID, err))
	}
	return comments
}

func parseSQLiteLabels(raw, issueID string, report *LoadReport) []string {
	var labels []string
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		report.ReadErrors++
		report.addWarning(fmt.Sprintf("parsing labels for %q: %v", issueID, err))
		return parseJSONStringArray(raw)
	}
	return labels
}

// CountIssues returns the count of non-tombstone issues
func (r *SQLiteReader) CountIssues() (int, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	session := sqliteReadSession{db: tx}
	columns, err := session.issuesColumns()
	if err != nil {
		return 0, err
	}
	var count int
	query := "SELECT COUNT(*) FROM issues WHERE status != 'tombstone'"
	if columns["tombstone"] {
		query += " AND (tombstone IS NULL OR tombstone = 0)"
	}
	if err := tx.QueryRow(query).Scan(&count); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
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
	tx, err := r.db.Begin()
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	session := sqliteReadSession{db: tx}
	columns, err := session.issuesColumns()
	if err != nil {
		return time.Time{}, err
	}
	if !columns["updated_at"] {
		return time.Time{}, nil
	}
	var raw sql.NullString
	if err := tx.QueryRow("SELECT MAX(updated_at) FROM issues").Scan(&raw); err != nil {
		return time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
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
