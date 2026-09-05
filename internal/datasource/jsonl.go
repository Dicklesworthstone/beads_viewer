package datasource

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// JSONLReader implements IssueReader for JSONL files on disk.
type JSONLReader struct {
	path     string
	reportMu sync.Mutex
	report   LoadReport
}

// NewJSONLReader creates a reader for a JSONL data source.
func NewJSONLReader(source DataSource) (*JSONLReader, error) {
	if source.Type != SourceTypeJSONLLocal && source.Type != SourceTypeJSONLWorktree {
		return nil, fmt.Errorf("source is not JSONL: %s", source.Type)
	}
	return &JSONLReader{path: source.Path}, nil
}

// LoadIssues returns all non-tombstone issues from the JSONL file.
// Parse accounting is retained by this reader so records dropped during
// load (malformed JSON, failed validation) stay visible to callers (#190).
func (r *JSONLReader) LoadIssues() ([]model.Issue, error) {
	rec := newLoadRecorder(r.path)
	all, err := loader.LoadIssuesFromFileWithOptions(r.path, rec.options())
	r.reportMu.Lock()
	r.report = rec.finish(all, err == nil)
	r.reportMu.Unlock()
	if err != nil {
		return nil, err
	}
	// Filter out tombstone issues to match the IssueReader contract.
	out := make([]model.Issue, 0, len(all))
	for i := range all {
		if !all[i].Status.IsTombstone() {
			out = append(out, all[i])
		}
	}
	return out, nil
}

func (r *JSONLReader) LoadReport() LoadReport {
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	return cloneLoadReport(r.report)
}

// LoadIssuesFiltered returns issues matching the provided filter.
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

// CountIssues returns the total number of issues in the file.
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

// GetLastModified returns the most recent update time, falling back to the
// file modification time when no issues have an UpdatedAt timestamp.
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

// Close is a no-op for JSONL (no persistent resources to release).
func (r *JSONLReader) Close() error {
	return nil
}
