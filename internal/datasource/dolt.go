package datasource

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// DoltReader implements IssueReader for Dolt databases via the MySQL wire protocol.
type DoltReader struct {
	db  *sql.DB
	dsn string
}

// NewDoltReader opens a connection to a Dolt sql-server.
// source.Path is expected to be a MySQL DSN (e.g. "root:@tcp(127.0.0.1:3306)/dbname?parseTime=true").
func NewDoltReader(source DataSource) (*DoltReader, error) {
	if source.Type != SourceTypeDolt {
		return nil, fmt.Errorf("source is not Dolt: %s", source.Type)
	}

	db, err := sql.Open("mysql", source.Path)
	if err != nil {
		return nil, fmt.Errorf("cannot open Dolt connection: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot connect to Dolt server: %w", err)
	}

	return &DoltReader{db: db, dsn: source.Path}, nil
}

// Close closes the database connection.
func (r *DoltReader) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

// LoadIssues returns all non-tombstone issues.
func (r *DoltReader) LoadIssues() ([]model.Issue, error) {
	return r.LoadIssuesFiltered(nil)
}

// LoadIssuesFiltered returns issues matching the filter function.
func (r *DoltReader) LoadIssuesFiltered(filter func(*model.Issue) bool) ([]model.Issue, error) {
	labelsExpr := r.labelsExpression()

	query := fmt.Sprintf(`
		SELECT
			i.id, i.title, i.description, i.status, i.priority, i.issue_type,
			i.assignee, i.estimated_minutes, i.created_at, i.updated_at,
			i.due_date, i.closed_at, i.external_ref, i.compaction_level,
			i.compacted_at, i.compacted_at_commit, i.original_size,
			%s, i.design, i.acceptance_criteria, i.notes, i.source_repo
		FROM issues i
		WHERE (i.tombstone IS NULL OR i.tombstone = 0)
		ORDER BY i.updated_at DESC
	`, labelsExpr)

	rows, err := r.db.Query(query)
	if err != nil {
		return r.loadIssuesSimple(filter)
	}
	defer rows.Close()

	var issues []model.Issue
	for rows.Next() {
		issue, err := r.scanFullIssue(rows)
		if err != nil {
			continue
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

// scanFullIssue scans a row from the full SELECT into a model.Issue.
func (r *DoltReader) scanFullIssue(rows *sql.Rows) (model.Issue, error) {
	var issue model.Issue
	var estimatedMinutes, compactionLevel, originalSize sql.NullInt64
	var createdAt, updatedAt, dueDate, closedAt, compactedAt sql.NullTime
	var description, assignee, externalRef, design, acceptanceCriteria, notes, sourceRepo, compactedAtCommit sql.NullString
	var labelsJSON sql.NullString
	var issueType string

	err := rows.Scan(
		&issue.ID, &issue.Title, &description, &issue.Status, &issue.Priority, &issueType,
		&assignee, &estimatedMinutes, &createdAt, &updatedAt,
		&dueDate, &closedAt, &externalRef, &compactionLevel,
		&compactedAt, &compactedAtCommit, &originalSize,
		&labelsJSON, &design, &acceptanceCriteria, &notes, &sourceRepo,
	)
	if err != nil {
		return issue, err
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

	return issue, nil
}

// loadIssuesSimple is a fallback for databases with fewer columns.
func (r *DoltReader) loadIssuesSimple(filter func(*model.Issue) bool) ([]model.Issue, error) {
	rows, err := r.db.Query(`
		SELECT id, title, description, status, priority, issue_type, created_at, updated_at
		FROM issues
		WHERE (tombstone IS NULL OR tombstone = 0)
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var issues []model.Issue
	for rows.Next() {
		var issue model.Issue
		var description sql.NullString
		var createdAt, updatedAt sql.NullTime
		var issueType string

		if err := rows.Scan(
			&issue.ID, &issue.Title, &description, &issue.Status, &issue.Priority, &issueType,
			&createdAt, &updatedAt,
		); err != nil {
			continue
		}

		if description.Valid {
			issue.Description = description.String
		}
		issue.IssueType = model.IssueType(issueType)
		if createdAt.Valid {
			issue.CreatedAt = createdAt.Time
		}
		if updatedAt.Valid {
			issue.UpdatedAt = updatedAt.Time
		}
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

// CountIssues returns the count of non-tombstone issues.
func (r *DoltReader) CountIssues() (int, error) {
	var count int
	err := r.db.QueryRow("SELECT COUNT(*) FROM issues WHERE (tombstone IS NULL OR tombstone = 0)").Scan(&count)
	return count, err
}

// GetIssueByID retrieves a single issue by ID.
func (r *DoltReader) GetIssueByID(id string) (*model.Issue, error) {
	issues, err := r.LoadIssuesFiltered(func(iss *model.Issue) bool {
		return iss.ID == id
	})
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, fmt.Errorf("issue not found: %s", id)
	}
	return &issues[0], nil
}

// GetLastModified returns the most recent update time across all issues.
func (r *DoltReader) GetLastModified() (time.Time, error) {
	var updatedAt sql.NullTime
	err := r.db.QueryRow("SELECT MAX(updated_at) FROM issues").Scan(&updatedAt)
	if err != nil {
		return time.Time{}, err
	}
	if !updatedAt.Valid {
		return time.Time{}, nil
	}
	return updatedAt.Time, nil
}

// labelsExpression returns the SQL expression for loading labels.
// Dolt uses INFORMATION_SCHEMA instead of sqlite_master.
func (r *DoltReader) labelsExpression() string {
	if r.hasLabelsColumn() {
		return "i.labels"
	}
	if r.hasLabelsTable() {
		return "(SELECT JSON_ARRAYAGG(label) FROM labels WHERE issue_id = i.id)"
	}
	return "NULL"
}

// hasLabelsColumn checks if the issues table has a labels column.
func (r *DoltReader) hasLabelsColumn() bool {
	var count int
	err := r.db.QueryRow(`
		SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_NAME = 'issues' AND COLUMN_NAME = 'labels'
	`).Scan(&count)
	return err == nil && count > 0
}

// hasLabelsTable checks if a separate labels table exists.
func (r *DoltReader) hasLabelsTable() bool {
	var count int
	err := r.db.QueryRow(`
		SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_NAME = 'labels'
	`).Scan(&count)
	return err == nil && count > 0
}

// loadDependencies loads dependencies for an issue.
func (r *DoltReader) loadDependencies(issueID string) []*model.Dependency {
	rows, err := r.db.Query("SELECT depends_on_id, dependency_type FROM dependencies WHERE issue_id = ?", issueID)
	if err != nil {
		return nil
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
	return deps
}

// loadComments loads comments for an issue.
func (r *DoltReader) loadComments(issueID string) []*model.Comment {
	rows, err := r.db.Query("SELECT id, author, text, created_at FROM comments WHERE issue_id = ? ORDER BY created_at", issueID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var comments []*model.Comment
	for rows.Next() {
		var comment model.Comment
		var createdAt sql.NullTime
		if err := rows.Scan(&comment.ID, &comment.Author, &comment.Text, &createdAt); err != nil {
			continue
		}
		if createdAt.Valid {
			comment.CreatedAt = createdAt.Time
		}
		comment.IssueID = issueID
		comments = append(comments, &comment)
	}
	return comments
}
