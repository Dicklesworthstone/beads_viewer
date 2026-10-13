package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/internal/datasource"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// buildContextLocked is called under w.mu. Stop cancels the worker context,
// recovery cancels the old loop context, and the existing processing budget
// limits each admitted build. No mutable "current read" context is shared by
// overlapping old/new generations. Callers must release the returned context.
func (w *BackgroundWorker) buildContextLocked() (context.Context, context.CancelFunc) {
	parent := w.loopCtx
	if parent == nil {
		parent = w.ctx
	}
	if parent == nil {
		parent = context.Background()
	}
	if w.processingTimeout > 0 {
		return context.WithTimeout(parent, w.processingTimeout)
	}
	return context.WithCancel(parent)
}

func isSQLiteReloadPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".db", ".sqlite", ".sqlite3":
		return true
	default:
		return false
	}
}

func openSQLiteReloadContext(ctx context.Context, path string) (*datasource.SQLiteReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, ok, err := datasource.SourceFromFile(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("unsupported SQLite source path: %s", path)
	}
	return datasource.NewSQLiteReaderContext(ctx, source)
}

// Keep SQLite's complete authority and filtering contract from the synchronous
// reload path, but carry this build's cancellation through every SQL statement.
// JSONL still uses its existing parser; cancellation there is checked before
// and after parsing, not claimed to interrupt filesystem reads or the parser.
func loadIssuesForReloadContext(ctx context.Context, path string, opts loader.ParseOptions) (reloadIssueData, error) {
	if err := ctx.Err(); err != nil {
		return reloadIssueData{}, err
	}
	if !isSQLiteReloadPath(path) {
		loaded, err := loadIssuesForReload(path, opts)
		if ctxErr := ctx.Err(); ctxErr != nil {
			loader.ReturnIssuePtrsToPool(loaded.PoolRefs)
			return reloadIssueData{}, ctxErr
		}
		return loaded, err
	}
	reader, err := openSQLiteReloadContext(ctx, path)
	if err != nil {
		return reloadIssueData{}, err
	}
	defer reader.Close()
	issues, err := reader.LoadIssueAuthorityContext(ctx)
	if err != nil {
		return reloadIssueData{}, err
	}
	// Build authority before hiding satisfied tombstones or recipe-filtered
	// blockers. The read transaction is already closed before graph work.
	result := reloadIssueData{
		Authority:     model.NewReadinessIndex(issues),
		AuthorityHash: analysis.ComputeDataHash(issues),
	}
	visible := make([]model.Issue, 0, len(issues))
	for i := range issues {
		if err := ctx.Err(); err != nil {
			return reloadIssueData{}, err
		}
		if !issues[i].Status.IsTombstone() && (opts.IssueFilter == nil || opts.IssueFilter(&issues[i])) {
			visible = append(visible, issues[i])
		}
	}
	if err := ctx.Err(); err != nil {
		return reloadIssueData{}, err
	}
	result.Issues = visible
	return result, nil
}

func countIssuesForReloadContext(ctx context.Context, path string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !isSQLiteReloadPath(path) {
		count, err := countIssuesForReload(path)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return count, err
	}
	reader, err := openSQLiteReloadContext(ctx, path)
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	return reader.CountIssuesContext(ctx)
}
