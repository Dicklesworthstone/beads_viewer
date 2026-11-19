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
	silent   bool
	reportMu sync.Mutex
	report   LoadReport
	closed   bool
}

// NewJSONLReader creates a reader for a JSONL data source.
func NewJSONLReader(source DataSource) (*JSONLReader, error) {
	return newJSONLReader(source, false)
}

func newJSONLReader(source DataSource, silent bool) (*JSONLReader, error) {
	if source.Type != SourceTypeJSONLLocal && source.Type != SourceTypeJSONLWorktree {
		return nil, fmt.Errorf("source is not JSONL: %s", source.Type)
	}
	return &JSONLReader{path: source.Path, silent: silent}, nil
}

func fusedLoadJSONL(path string, opts ValidationOptions, silent bool) ([]model.Issue, LoadReport, error) {
	rec := newLoadRecorderWithSilence(path, silent)
	all, err := loader.LoadIssuesFromFileWithOptions(path, rec.options())
	if err != nil {
		return nil, rec.finish(all, false), err
	}
	stats := rec.stats

	maxRate := opts.MaxJSONLErrorRate
	if rate := stats.ErrorRate(); rate > maxRate {
		return nil, rec.finish(all, false), fmt.Errorf("%s: too many errors: %.1f%% (max %.1f%%)", path, rate*100, maxRate*100)
	}
	if stats.Valid == 0 && stats.Errors+stats.Skipped > 0 {
		return nil, rec.finish(all, false), fmt.Errorf("%s: no issue records (%d non-issue/error lines, 0 valid issues)", path, stats.Errors+stats.Skipped)
	}

	report := rec.finish(all, true)
	out := make([]model.Issue, 0, len(all))
	for i := range all {
		if !all[i].Status.IsTombstone() {
			out = append(out, all[i])
		}
	}
	return out, report, nil
}

// LoadIssues returns all non-tombstone issues from the JSONL file after
// enforcing the fused corruption/non-issue validation gates.
// Parse accounting is retained by this reader so records dropped during
// load (malformed JSON, failed validation) stay visible to callers (#190).
func (r *JSONLReader) LoadIssues() ([]model.Issue, error) {
	r.reportMu.Lock()
	closed := r.closed
	r.reportMu.Unlock()
	if closed {
		return nil, fmt.Errorf("JSONL reader is closed")
	}

	issues, report, err := fusedLoadJSONL(r.path, DefaultValidationOptions(), r.silent)
	r.reportMu.Lock()
	if r.closed {
		r.reportMu.Unlock()
		return nil, fmt.Errorf("JSONL reader is closed")
	}
	r.report = report
	r.reportMu.Unlock()
	if err != nil {
		return nil, err
	}
	return issues, nil
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

// Close marks the JSONL reader closed so subsequent reads fail consistently.
func (r *JSONLReader) Close() error {
	r.reportMu.Lock()
	r.closed = true
	r.reportMu.Unlock()
	return nil
}
