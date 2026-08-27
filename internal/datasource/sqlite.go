package datasource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

type sqliteQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func closeSQLiteRows(rows *sql.Rows, operation string, resultErr *error) {
	if err := rows.Close(); err != nil {
		*resultErr = errors.Join(*resultErr, fmt.Errorf("closing %s rows: %w", operation, err))
	}
}

func tableColumns(queryer sqliteQueryer, table string) (columns map[string]bool, err error) {
	var query string
	switch table {
	case "dependencies":
		query = "PRAGMA table_info(dependencies)"
	case "issues":
		query = "PRAGMA table_info(issues)"
	default:
		return nil, fmt.Errorf("unsupported SQLite table metadata request: %s", table)
	}

	rows, err := queryer.Query(query)
	if err != nil {
		return nil, fmt.Errorf("querying %s table metadata: %w", table, err)
	}
	defer closeSQLiteRows(rows, table+" metadata", &err)

	columns = make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("scanning %s table metadata: %w", table, err)
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating %s table metadata: %w", table, err)
	}
	return columns, nil
}

func sqliteTableExists(queryer sqliteQueryer, table string) (bool, error) {
	var one int
	err := queryer.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name = ?", table).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking for SQLite table %q: %w", table, err)
	}
	return true, nil
}

// LoadIssues reads all issues from the database
func (r *SQLiteReader) LoadIssues() ([]model.Issue, error) {
	return r.LoadIssuesFiltered(nil)
}

// LoadIssuesFiltered reads issues matching the filter function
func (r *SQLiteReader) LoadIssuesFiltered(filter func(*model.Issue) bool) (issues []model.Issue, err error) {
	tx, err := r.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("beginning SQLite issue read transaction: %w", err)
	}
	defer func() {
		if tx == nil {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back SQLite issue read transaction: %w", rollbackErr))
			issues = nil
		}
	}()

	issues, err = r.loadIssuesFilteredTx(tx, filter)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing SQLite issue read transaction: %w", err)
	}
	tx = nil
	return issues, nil
}

func (r *SQLiteReader) loadIssuesFilteredTx(tx *sql.Tx, filter func(*model.Issue) bool) ([]model.Issue, error) {
	columns, err := tableColumns(tx, "issues")
	if err != nil {
		return nil, err
	}
	labelsTable, err := sqliteTableExists(tx, "labels")
	if err != nil {
		return nil, err
	}
	dependenciesTable, err := sqliteTableExists(tx, "dependencies")
	if err != nil {
		return nil, err
	}
	commentsTable, err := sqliteTableExists(tx, "comments")
	if err != nil {
		return nil, err
	}

	dependencyTypeExpr := ""
	if dependenciesTable {
		dependencyColumns, err := tableColumns(tx, "dependencies")
		if err != nil {
			return nil, err
		}
		for _, required := range []string{"issue_id", "depends_on_id"} {
			if !dependencyColumns[required] {
				return nil, fmt.Errorf("dependencies table is missing required column %q", required)
			}
		}
		switch {
		case dependencyColumns["dependency_type"]:
			dependencyTypeExpr = "COALESCE(dependency_type, '')"
		case dependencyColumns["type"]:
			dependencyTypeExpr = "COALESCE(type, '')"
		default:
			dependencyTypeExpr = "''"
		}
	}

	var issues []model.Issue
	if supportsFullIssueQuery(columns) {
		issues, err = loadFullIssueRows(tx, columns)
	} else {
		issues, err = loadSimpleIssueRows(tx, columns)
	}
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, nil
	}

	issueIDs := make(map[string]struct{}, len(issues))
	for i := range issues {
		issueIDs[issues[i].ID] = struct{}{}
	}
	liveIssueIDsQuery := "SELECT id FROM issues " + liveIssueWhereClause(columns, "")
	var labelsByIssue map[string][]string
	if labelsTable {
		labelsByIssue, err = loadLabelsFromTable(tx, issueIDs, liveIssueIDsQuery)
		if err != nil {
			return nil, err
		}
	}
	var dependenciesByIssue map[string][]*model.Dependency
	if dependenciesTable {
		dependenciesByIssue, err = loadDependencies(tx, issueIDs, liveIssueIDsQuery, dependencyTypeExpr)
		if err != nil {
			return nil, err
		}
	}
	var commentsByIssue map[string][]*model.Comment
	if commentsTable {
		commentsByIssue, err = loadComments(tx, issueIDs, liveIssueIDsQuery)
		if err != nil {
			return nil, err
		}
	}

	var filtered []model.Issue
	for i := range issues {
		issue := &issues[i]
		if labelsTable && len(issue.Labels) == 0 {
			issue.Labels = labelsByIssue[issue.ID]
		}
		if dependenciesTable {
			issue.Dependencies = dependenciesByIssue[issue.ID]
		}
		if commentsTable {
			issue.Comments = commentsByIssue[issue.ID]
		}
		if filter == nil || filter(issue) {
			filtered = append(filtered, *issue)
		}
	}
	return filtered, nil
}

func supportsFullIssueQuery(columns map[string]bool) bool {
	for _, required := range []string{
		"id", "title", "description", "status", "priority", "issue_type",
		"assignee", "estimated_minutes", "created_at", "updated_at", "due_date",
		"closed_at", "external_ref", "compaction_level", "compacted_at",
		"compacted_at_commit", "original_size", "design", "acceptance_criteria",
		"notes", "source_repo",
	} {
		if !columns[required] {
			return false
		}
	}
	return true
}

// liveIssueWhereClause keeps the SQLite backend aligned with the IssueReader
// contract even when an export omits the optional numeric tombstone column (or
// leaves it inconsistent). JSONL represents deletion canonically through the
// status field, so status=tombstone must always be excluded too.
func liveIssueWhereClause(columns map[string]bool, qualifier string) string {
	predicates := []string{fmt.Sprintf("LOWER(TRIM(COALESCE(%sstatus, ''))) <> 'tombstone'", qualifier)}
	if columns["tombstone"] {
		predicates = append(predicates, fmt.Sprintf("(%stombstone IS NULL OR %stombstone = 0)", qualifier, qualifier))
	}
	return "WHERE " + strings.Join(predicates, " AND ")
}

// normalizeSQLiteIssueStatus applies the same canonical status representation
// as the JSONL loader. Graph and claimability code compare model.Status values
// exactly, so allowing a database row such as " CLOSED " to escape here would
// make a completed issue look active even though the SQL live-row predicate
// correctly understood the value.
func normalizeSQLiteIssueStatus(issue *model.Issue) {
	if issue == nil {
		return
	}
	issue.Status = model.Status(strings.ToLower(strings.TrimSpace(string(issue.Status))))
}

func loadFullIssueRows(queryer sqliteQueryer, columns map[string]bool) (issues []model.Issue, err error) {
	deferUntilExpr := "NULL"
	if columns["defer_until"] {
		deferUntilExpr = "i.defer_until"
	}
	labelsExpr := "NULL"
	if columns["labels"] {
		labelsExpr = "i.labels"
	}
	where := liveIssueWhereClause(columns, "i.")
	query := fmt.Sprintf(`
		SELECT
			i.id, i.title, i.description, i.status, i.priority, i.issue_type,
			i.assignee, i.estimated_minutes, i.created_at, i.updated_at,
			i.due_date, %s, i.closed_at, i.external_ref, i.compaction_level,
			i.compacted_at, i.compacted_at_commit, i.original_size,
			%s, i.design, i.acceptance_criteria, i.notes, i.source_repo
		FROM issues i
		%s
		ORDER BY i.updated_at DESC, i.id ASC
	`, deferUntilExpr, labelsExpr, where)

	rows, err := queryer.Query(query)
	if err != nil {
		return nil, fmt.Errorf("querying full SQLite issues: %w", err)
	}
	defer closeSQLiteRows(rows, "full issues", &err)

	rowNumber := 0
	for rows.Next() {
		rowNumber++
		var issue model.Issue
		var estimatedMinutes, compactionLevel, originalSize sql.NullInt64
		var createdAt, updatedAt, dueDate, deferUntil, closedAt, compactedAt sql.NullTime
		var description, assignee, externalRef, design, acceptanceCriteria, notes, sourceRepo, compactedAtCommit sql.NullString
		var labelsJSON sql.NullString
		var issueType string

		if err := rows.Scan(
			&issue.ID, &issue.Title, &description, &issue.Status, &issue.Priority, &issueType,
			&assignee, &estimatedMinutes, &createdAt, &updatedAt,
			&dueDate, &deferUntil, &closedAt, &externalRef, &compactionLevel,
			&compactedAt, &compactedAtCommit, &originalSize,
			&labelsJSON, &design, &acceptanceCriteria, &notes, &sourceRepo,
		); err != nil {
			return nil, fmt.Errorf("scanning full SQLite issue row %d: %w", rowNumber, err)
		}

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
		if labelsJSON.Valid && labelsJSON.String != "" && labelsJSON.String != "null" {
			issue.Labels = parseJSONStringArray(labelsJSON.String)
		}
		normalizeSQLiteIssueStatus(&issue)
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating full SQLite issues: %w", err)
	}
	return issues, nil
}

// loadSimpleIssueRows supports databases with fewer columns without treating
// unrelated operational query failures as a reason to silently downgrade.
func loadSimpleIssueRows(queryer sqliteQueryer, columns map[string]bool) (issues []model.Issue, err error) {
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
	where := liveIssueWhereClause(columns, "")
	orderBy := "ORDER BY id"
	if columns["updated_at"] {
		orderBy = "ORDER BY updated_at DESC, id ASC"
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

	rows, err := queryer.Query(query)
	if err != nil {
		return nil, fmt.Errorf("querying simple SQLite issues: %w", err)
	}
	defer closeSQLiteRows(rows, "simple issues", &err)

	rowNumber := 0
	for rows.Next() {
		rowNumber++
		var issue model.Issue
		var description, assignee sql.NullString
		var createdAt, updatedAt, deferUntil sql.NullString
		var labelsJSON sql.NullString
		var issueType string

		if err := rows.Scan(
			&issue.ID, &issue.Title, &description, &issue.Status, &issue.Priority, &issueType,
			&assignee, &createdAt, &updatedAt, &deferUntil, &labelsJSON,
		); err != nil {
			return nil, fmt.Errorf("scanning simple SQLite issue row %d: %w", rowNumber, err)
		}

		if description.Valid {
			issue.Description = description.String
		}
		issue.IssueType = model.IssueType(issueType)
		if assignee.Valid {
			issue.Assignee = assignee.String
		}
		if createdAt.Valid {
			t, ok := parseSQLiteTime(createdAt.String)
			if !ok {
				return nil, fmt.Errorf("parsing created_at for SQLite issue %q: %q", issue.ID, createdAt.String)
			}
			issue.CreatedAt = t
		}
		if updatedAt.Valid {
			t, ok := parseSQLiteTime(updatedAt.String)
			if !ok {
				return nil, fmt.Errorf("parsing updated_at for SQLite issue %q: %q", issue.ID, updatedAt.String)
			}
			issue.UpdatedAt = t
		}
		if deferUntil.Valid {
			t, ok := parseSQLiteTime(deferUntil.String)
			if !ok {
				return nil, fmt.Errorf("parsing defer_until for SQLite issue %q: %q", issue.ID, deferUntil.String)
			}
			issue.DeferUntil = &t
		}
		if labelsJSON.Valid && labelsJSON.String != "" && labelsJSON.String != "null" {
			issue.Labels = parseJSONStringArray(labelsJSON.String)
		}
		normalizeSQLiteIssueStatus(&issue)

		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating simple SQLite issues: %w", err)
	}

	return issues, nil
}

// loadLabelsFromTable loads labels for all live issues in one ordered scan.
// Orphan rows are ignored, matching the old per-issue query behavior.
func loadLabelsFromTable(queryer sqliteQueryer, issueIDs map[string]struct{}, liveIssueIDsQuery string) (labelsByIssue map[string][]string, err error) {
	query := fmt.Sprintf(`SELECT issue_id, label FROM labels WHERE issue_id IN (%s) ORDER BY issue_id ASC, label ASC`, liveIssueIDsQuery)
	rows, err := queryer.Query(query)
	if err != nil {
		return nil, fmt.Errorf("querying SQLite labels: %w", err)
	}
	defer closeSQLiteRows(rows, "labels", &err)

	labelsByIssue = make(map[string][]string)
	for rows.Next() {
		var issueID, label sql.NullString
		if err := rows.Scan(&issueID, &label); err != nil {
			return nil, fmt.Errorf("scanning SQLite labels: %w", err)
		}
		if !issueID.Valid {
			continue
		}
		if _, live := issueIDs[issueID.String]; !live {
			continue
		}
		if !label.Valid {
			return nil, fmt.Errorf("scanning labels for SQLite issue %q: label is NULL", issueID.String)
		}
		labelsByIssue[issueID.String] = append(labelsByIssue[issueID.String], label.String)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SQLite labels: %w", err)
	}
	return labelsByIssue, nil
}

// loadDependencies loads dependencies for all live issues in one ordered scan.
func loadDependencies(queryer sqliteQueryer, issueIDs map[string]struct{}, liveIssueIDsQuery, dependencyTypeExpr string) (dependenciesByIssue map[string][]*model.Dependency, err error) {
	query := fmt.Sprintf(`SELECT issue_id, depends_on_id, %s FROM dependencies WHERE issue_id IN (%s) ORDER BY issue_id ASC, depends_on_id ASC, 3 ASC`, dependencyTypeExpr, liveIssueIDsQuery)
	rows, err := queryer.Query(query)
	if err != nil {
		return nil, fmt.Errorf("querying SQLite dependencies: %w", err)
	}
	defer closeSQLiteRows(rows, "dependencies", &err)

	dependenciesByIssue = make(map[string][]*model.Dependency)
	for rows.Next() {
		var issueID, dependsOnID sql.NullString
		var depType string
		if err := rows.Scan(&issueID, &dependsOnID, &depType); err != nil {
			return nil, fmt.Errorf("scanning SQLite dependencies: %w", err)
		}
		if !issueID.Valid {
			continue
		}
		if _, live := issueIDs[issueID.String]; !live {
			continue
		}
		if !dependsOnID.Valid {
			return nil, fmt.Errorf("scanning dependencies for SQLite issue %q: depends_on_id is NULL", issueID.String)
		}
		dep := &model.Dependency{
			IssueID:     issueID.String,
			DependsOnID: dependsOnID.String,
			Type:        model.DependencyType(depType),
		}
		dependenciesByIssue[issueID.String] = append(dependenciesByIssue[issueID.String], dep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SQLite dependencies: %w", err)
	}
	return dependenciesByIssue, nil
}

// loadComments loads comments for all live issues in one ordered scan.
func loadComments(queryer sqliteQueryer, issueIDs map[string]struct{}, liveIssueIDsQuery string) (commentsByIssue map[string][]*model.Comment, err error) {
	query := fmt.Sprintf(`SELECT issue_id, id, author, text, created_at FROM comments WHERE issue_id IN (%s) ORDER BY issue_id ASC, created_at ASC, id ASC`, liveIssueIDsQuery)
	rows, err := queryer.Query(query)
	if err != nil {
		return nil, fmt.Errorf("querying SQLite comments: %w", err)
	}
	defer closeSQLiteRows(rows, "comments", &err)

	commentsByIssue = make(map[string][]*model.Comment)
	for rows.Next() {
		var issueID, id, author, text, createdAt sql.NullString
		if err := rows.Scan(&issueID, &id, &author, &text, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning SQLite comments: %w", err)
		}
		if !issueID.Valid {
			continue
		}
		if _, live := issueIDs[issueID.String]; !live {
			continue
		}
		if !id.Valid {
			return nil, fmt.Errorf("scanning comments for SQLite issue %q: id is NULL", issueID.String)
		}
		if !text.Valid {
			return nil, fmt.Errorf("scanning comments for SQLite issue %q: text is NULL", issueID.String)
		}
		comment := &model.Comment{ID: id.String, IssueID: issueID.String, Text: text.String}
		if author.Valid {
			comment.Author = author.String
		}
		if createdAt.Valid {
			t, ok := parseSQLiteTime(createdAt.String)
			if !ok {
				return nil, fmt.Errorf("parsing comment created_at for SQLite issue %q: %q", issueID.String, createdAt.String)
			}
			comment.CreatedAt = t
		}
		commentsByIssue[issueID.String] = append(commentsByIssue[issueID.String], comment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SQLite comments: %w", err)
	}
	return commentsByIssue, nil
}

// CountIssues returns the count of non-tombstone issues
func (r *SQLiteReader) CountIssues() (int, error) {
	columns, err := tableColumns(r.db, "issues")
	if err != nil {
		return 0, err
	}
	var count int
	query := "SELECT COUNT(*) FROM issues " + liveIssueWhereClause(columns, "")
	err = r.db.QueryRow(query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting SQLite issues: %w", err)
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
	columns, err := tableColumns(r.db, "issues")
	if err != nil {
		return time.Time{}, err
	}
	if !columns["updated_at"] {
		return time.Time{}, nil
	}
	var raw sql.NullString
	err = r.db.QueryRow("SELECT MAX(updated_at) FROM issues " + liveIssueWhereClause(columns, "")).Scan(&raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading latest SQLite issue update: %w", err)
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
