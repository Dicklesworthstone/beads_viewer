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
	issues, err := r.loadIssueAuthority(&report)
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

func (r *SQLiteReader) loadIssueAuthority(report *LoadReport) ([]model.Issue, error) {
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
	// Detect schema: beads-rs (br) databases store labels in a separate
	// "labels" table rather than a JSON column on "issues". We substitute
	// a subquery so that labels are loaded transparently.
	labelsExpr := "i.labels"
	if !r.hasLabelsColumn() && schema.labelsTable {
		labelsExpr = "(SELECT json_group_array(label) FROM labels WHERE issue_id = i.id)"
	}

	// defer_until was added to the beads schema after this query was written;
	// older databases lack the column, and selecting a missing column would
	// fail the whole query (and silently downgrade to loadIssuesSimple). Probe
	// the schema once and substitute NULL when absent.
	deferUntilExpr := "NULL"
	if r.issuesColumns()["defer_until"] {
		deferUntilExpr = "i.defer_until"
	}

	// Query all source records, including tombstones. Use table alias "i" to avoid
	// column ambiguity when a labels subquery references issue_id.
	query := fmt.Sprintf(`
		SELECT
			i.id, i.title, i.description, i.status, i.priority, i.issue_type,
			i.assignee, i.estimated_minutes, i.created_at, i.updated_at,
			i.due_date, %s, i.closed_at, i.external_ref, i.compaction_level,
			i.compacted_at, i.compacted_at_commit, i.original_size,
			%s, i.design, i.acceptance_criteria, i.notes, i.source_repo, i.tombstone
		FROM issues i
		ORDER BY i.updated_at DESC
	`, deferUntilExpr, labelsExpr)

	rows, err := r.db.Query(query)
	if err != nil {
		// Try simpler query if some columns don't exist
		return r.loadIssuesSimple(report, schema)
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
			issue.Labels = parseSQLiteLabels(labelsJSON.String, issue.ID, report)
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

// loadIssuesSimple is a fallback for databases with fewer columns
func (r *SQLiteReader) loadIssuesSimple(report *LoadReport, schema sqliteReadSchema) ([]model.Issue, error) {
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
	orderBy := "ORDER BY id"
	if columns["updated_at"] {
		orderBy = "ORDER BY updated_at DESC"
	}
	query := fmt.Sprintf(`
		SELECT id, title, %s, status, %s, %s, %s, %s, %s, %s, %s, %s
		FROM issues
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
		expr("tombstone", "NULL"),
		orderBy,
	)

	rows, err := r.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var issues []model.Issue
	for rows.Next() {
		var issue model.Issue
		var description, assignee sql.NullString
		var createdAt, updatedAt, deferUntil sql.NullString
		var labelsJSON sql.NullString
		var issueType string
		var tombstone sql.NullInt64

		err := rows.Scan(
			&issue.ID, &issue.Title, &description, &issue.Status, &issue.Priority, &issueType,
			&assignee, &createdAt, &updatedAt, &deferUntil, &labelsJSON, &tombstone,
		)
		if err != nil {
			report.Errors++
			report.addWarning(fmt.Sprintf("reading issue row: %v", err))
			continue
		}
		if tombstone.Valid && tombstone.Int64 != 0 {
			issue.Status = model.StatusTombstone
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
			} else if strings.TrimSpace(createdAt.String) != "" {
				report.ReadErrors++
				report.addWarning(fmt.Sprintf("issue %q has invalid created_at %q", issue.ID, createdAt.String))
			}
		}
		if updatedAt.Valid {
			if t, ok := parseSQLiteTime(updatedAt.String); ok {
				issue.UpdatedAt = t
			} else if strings.TrimSpace(updatedAt.String) != "" {
				report.ReadErrors++
				report.addWarning(fmt.Sprintf("issue %q has invalid updated_at %q", issue.ID, updatedAt.String))
			}
		}
		if deferUntil.Valid {
			if t, ok := parseSQLiteTime(deferUntil.String); ok {
				issue.DeferUntil = &t
			} else if strings.TrimSpace(deferUntil.String) != "" {
				report.ReadErrors++
				report.addWarning(fmt.Sprintf("issue %q has invalid defer_until %q", issue.ID, deferUntil.String))
			}
		}
		if labelsJSON.Valid && labelsJSON.String != "" && labelsJSON.String != "null" {
			issue.Labels = parseSQLiteLabels(labelsJSON.String, issue.ID, report)
		}

		// Load labels from separate table if present (beads-rs compatibility)
		if schema.labelsTable && len(issue.Labels) == 0 {
			issue.Labels = r.loadLabelsFromTable(issue.ID, report)
		}

		issue.Dependencies = r.loadDependencies(issue.ID, schema.dependencyType, report)
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

// loadLabelsFromTable loads labels for an issue from the separate labels table
// used by beads-rs (br) databases.
func (r *SQLiteReader) loadLabelsFromTable(issueID string, report *LoadReport) []string {
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
func (r *SQLiteReader) loadDependencies(issueID, dependencyTypeExpr string, report *LoadReport) []*model.Dependency {
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

func (r *SQLiteReader) tableExists(name string) (bool, error) {
	var exists int
	err := r.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view') AND name = ?", name).Scan(&exists)
	return exists > 0, err
}

func (r *SQLiteReader) dependencyTypeExpr() (string, error) {
	// Minimal snapshots may omit this optional table. Inspect it once per load,
	// so missing schema and unreadable schema do not become the same authority.
	exists, err := r.tableExists("dependencies")
	if err != nil {
		return "", fmt.Errorf("inspecting dependency table: %w", err)
	}
	if !exists {
		return "", nil
	}
	columns := r.tableColumns("dependencies")
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
func (r *SQLiteReader) loadComments(issueID string, report *LoadReport) []*model.Comment {
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
	var count int
	query := "SELECT COUNT(*) FROM issues WHERE status != 'tombstone'"
	if r.issuesColumns()["tombstone"] {
		query += " AND (tombstone IS NULL OR tombstone = 0)"
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
