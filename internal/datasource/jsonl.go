package datasource

import (
	"fmt"
	"os"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// JSONLReader implements IssueReader for JSONL files.
type JSONLReader struct {
	path string
}

// NewJSONLReader creates a reader for a JSONL data source.
func NewJSONLReader(source DataSource) (*JSONLReader, error) {
	if source.Type != SourceTypeJSONLLocal && source.Type != SourceTypeJSONLWorktree {
		return nil, fmt.Errorf("source is not JSONL: %s", source.Type)
	}
	return &JSONLReader{path: source.Path}, nil
}

// LoadIssues returns all issues from the JSONL file.
func (r *JSONLReader) LoadIssues() ([]model.Issue, error) {
	return loader.LoadIssuesFromFile(r.path)
}

// LoadIssuesFiltered returns issues matching the filter.
func (r *JSONLReader) LoadIssuesFiltered(filter func(*model.Issue) bool) ([]model.Issue, error) {
	all, err := r.LoadIssues()
	if err != nil {
		return nil, err
	}
	if filter == nil {
		return all, nil
	}
	var out []model.Issue
	for i := range all {
		if filter(&all[i]) {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// CountIssues returns the total number of issues.
func (r *JSONLReader) CountIssues() (int, error) {
	issues, err := r.LoadIssues()
	if err != nil {
		return 0, err
	}
	return len(issues), nil
}

// GetIssueByID retrieves a single issue by ID.
func (r *JSONLReader) GetIssueByID(id string) (*model.Issue, error) {
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

// GetLastModified returns the most recent update time, falling back to file mod time.
func (r *JSONLReader) GetLastModified() (time.Time, error) {
	issues, err := r.LoadIssues()
	if err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	for _, iss := range issues {
		if iss.UpdatedAt.After(latest) {
			latest = iss.UpdatedAt
		}
	}
	if latest.IsZero() {
		// Fall back to file modification time
		info, err := os.Stat(r.path)
		if err != nil {
			return time.Time{}, err
		}
		return info.ModTime(), nil
	}
	return latest, nil
}

// Close is a no-op for JSONL (no persistent resources).
func (r *JSONLReader) Close() error {
	return nil
}
