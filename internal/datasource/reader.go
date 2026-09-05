package datasource

import (
	"fmt"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// IssueReader is the common interface for all beads data backends.
// Implementations include SQLiteReader and JSONLReader; additional backends
// (e.g. Dolt) can be added by implementing this interface and registering
// the type in NewReader.
type IssueReader interface {
	// LoadIssues returns all non-tombstone issues.
	LoadIssues() ([]model.Issue, error)

	// LoadReport returns owned accounting from this reader's most recent load.
	LoadReport() LoadReport

	// LoadIssuesFiltered returns issues that pass the filter function.
	// A nil filter returns all issues (same as LoadIssues).
	LoadIssuesFiltered(filter func(*model.Issue) bool) ([]model.Issue, error)

	// CountIssues returns the number of non-tombstone issues.
	CountIssues() (int, error)

	// GetIssueByID retrieves a single issue by its ID.
	GetIssueByID(id string) (*model.Issue, error)

	// GetLastModified returns the most recent update time across all issues.
	GetLastModified() (time.Time, error)

	// Close releases any resources held by the reader.
	Close() error
}

// NewReader creates an IssueReader for the given DataSource, dispatching to the
// appropriate backend based on source type.
func NewReader(source DataSource) (IssueReader, error) {
	switch source.Type {
	case SourceTypeSQLite:
		return NewSQLiteReader(source)
	case SourceTypeJSONLLocal, SourceTypeJSONLWorktree:
		return NewJSONLReader(source)
	default:
		return nil, fmt.Errorf("unsupported source type: %s", source.Type)
	}
}
