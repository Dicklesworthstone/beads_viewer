// Package export provides SQLite-based data export for static viewer deployment.
//
// This file implements the SQLiteExporter which exports bv's issue data to a SQLite
// database optimized for client-side querying with sql.js WASM.
package export

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	_ "modernc.org/sqlite"
)

// SQLiteExporter exports bv data to a SQLite database for static deployment.
type SQLiteExporter struct {
	Issues  []*model.Issue
	Deps    []*model.Dependency
	Metrics map[string]*model.IssueMetrics
	Stats   *analysis.GraphStats
	Triage  *analysis.TriageResult
	Config  SQLiteExportConfig
	gitHash string
}

// NewSQLiteExporter creates a new exporter with the given data.
// The third parameter may be either:
//   - map[string]*model.IssueMetrics (explicit metrics)
//   - *analysis.GraphStats (for computed metrics)
//
// This keeps backward compatibility with legacy call sites/tests.
func NewSQLiteExporter(issues []*model.Issue, deps []*model.Dependency, metricsOrStats interface{}, triage *analysis.TriageResult) *SQLiteExporter {
	exp := &SQLiteExporter{
		Issues: issues,
		Deps:   deps,
		Triage: triage,
		Config: DefaultSQLiteExportConfig(),
	}
	switch v := metricsOrStats.(type) {
	case map[string]*model.IssueMetrics:
		exp.Metrics = v
	case *analysis.GraphStats:
		exp.Stats = v
	case nil:
		// nothing
	default:
		// ignore unsupported type to avoid panics in callers
	}
	return exp
}

// SetGitHash sets the git commit hash for export metadata.
func (e *SQLiteExporter) SetGitHash(hash string) {
	e.gitHash = hash
}

// Export writes the SQLite database and supporting files to the output directory.
func (e *SQLiteExporter) Export(outputDir string) error {
	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// Create data subdirectory for JSON outputs
	dataDir := filepath.Join(outputDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(outputDir, "beads.sqlite3")

	// Remove existing database if present
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing database: %w", err)
	}

	// Open database
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	dbClosed := false
	defer func() {
		if !dbClosed {
			db.Close()
		}
	}()

	// Create schema
	if err := CreateSchema(db); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Insert issues
	if err := e.insertIssues(db); err != nil {
		return fmt.Errorf("insert issues: %w", err)
	}

	// Insert dependencies
	if err := e.insertDependencies(db); err != nil {
		return fmt.Errorf("insert dependencies: %w", err)
	}

	// Insert comments (bv-52)
	if err := e.insertComments(db); err != nil {
		return fmt.Errorf("insert comments: %w", err)
	}

	// Insert metrics
	if err := e.insertMetrics(db); err != nil {
		return fmt.Errorf("insert metrics: %w", err)
	}

	// Insert triage recommendations
	if err := e.insertTriageRecommendations(db); err != nil {
		return fmt.Errorf("insert triage: %w", err)
	}

	// Create FTS index (modernc.org/sqlite has FTS5 built-in)
	if err := CreateFTSIndex(db); err != nil {
		// Defensive: log but continue if FTS5 creation fails for any reason
		fmt.Printf("Warning: FTS5 not available: %v\n", err)
	}

	// Create materialized views
	if err := CreateMaterializedViews(db); err != nil {
		return fmt.Errorf("create materialized views: %w", err)
	}

	// Populate additional overview metrics (cycle flags)
	if err := e.populateOverviewMetrics(db); err != nil {
		return fmt.Errorf("populate overview metrics: %w", err)
	}

	// Insert metadata
	if err := e.insertMeta(db); err != nil {
		return fmt.Errorf("insert meta: %w", err)
	}

	// Optimize database
	if err := OptimizeDatabase(db, e.Config.PageSize); err != nil {
		return fmt.Errorf("optimize database: %w", err)
	}

	// Close database before chunking (mark as closed so defer doesn't double-close)
	if err := db.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	dbClosed = true

	// Write robot JSON outputs
	if e.Config.IncludeRobotOutputs {
		if err := e.writeRobotOutputs(dataDir); err != nil {
			return fmt.Errorf("write robot outputs: %w", err)
		}
	}

	// Write pre-computed graph layout for fast client-side rendering
	if err := e.writeGraphLayout(dataDir); err != nil {
		return fmt.Errorf("write graph layout: %w", err)
	}

	// Chunk if needed
	if err := e.chunkIfNeeded(outputDir, dbPath); err != nil {
		return fmt.Errorf("chunk database: %w", err)
	}

	return nil
}

// insertIssues inserts all issues into the database.
func (e *SQLiteExporter) insertIssues(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, labels, created_at, updated_at, closed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, issue := range e.Issues {
		labels := "[]"
		if len(issue.Labels) > 0 {
			labelsJSON, _ := json.Marshal(issue.Labels)
			labels = string(labelsJSON)
		}

		var closedAt *string
		if issue.ClosedAt != nil {
			s := issue.ClosedAt.Format(time.RFC3339)
			closedAt = &s
		}

		_, err := stmt.Exec(
			issue.ID,
			issue.Title,
			issue.Description,
			issue.Design,
			issue.AcceptanceCriteria,
			issue.Notes,
			string(issue.Status),
			issue.Priority,
			string(issue.IssueType),
			issue.Assignee,
			labels,
			issue.CreatedAt.Format(time.RFC3339),
			issue.UpdatedAt.Format(time.RFC3339),
			closedAt,
		)
		if err != nil {
			return fmt.Errorf("insert issue %s: %w", issue.ID, err)
		}
	}

	return tx.Commit()
}

// insertDependencies inserts all dependencies into the database.
func (e *SQLiteExporter) insertDependencies(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO dependencies (issue_id, depends_on_id, type)
		VALUES (?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, dep := range e.Deps {
		if dep == nil {
			continue
		}
		_, err := stmt.Exec(dep.IssueID, dep.DependsOnID, string(dep.Type))
		if err != nil {
			return fmt.Errorf("insert dependency %s->%s: %w", dep.IssueID, dep.DependsOnID, err)
		}
	}

	return tx.Commit()
}

// insertComments inserts all comments for all issues (bv-52).
func (e *SQLiteExporter) insertComments(db *sql.DB) error {
	// Count total comments first
	totalComments := 0
	for _, issue := range e.Issues {
		totalComments += len(issue.Comments)
	}
	if totalComments == 0 {
		return nil // No comments to insert
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO comments (id, issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, issue := range e.Issues {
		for _, comment := range issue.Comments {
			if comment == nil {
				continue
			}
			// Use composite ID (issue_id:comment_id) to avoid UNIQUE constraint
			// violations when exporting workspaces with multiple repos (bv-76).
			// Comment.ID is a string (UUIDv7 in beads v1.0+, integer-as-string
			// in legacy data), so format with %s.
			compositeID := fmt.Sprintf("%s:%s", issue.ID, comment.ID)
			_, err := stmt.Exec(
				compositeID,
				issue.ID,
				comment.Author,
				comment.Text,
				comment.CreatedAt.Format(time.RFC3339),
			)
			if err != nil {
				return fmt.Errorf("insert comment %s for issue %s: %w", comment.ID, issue.ID, err)
			}
		}
	}

	return tx.Commit()
}

// insertMetrics inserts computed graph metrics for all issues.
func (e *SQLiteExporter) insertMetrics(db *sql.DB) error {
	if e.Stats == nil {
		return nil // No stats available
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO issue_metrics (issue_id, pagerank, betweenness, critical_path_depth, triage_score, blocks_count, blocked_by_count)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	// Build status lookup so dep counts can ignore satisfied (closed/
	// tombstoned) endpoints, and so dangling refs (an edge whose other
	// end isn't in this export's issue universe) are excluded — keeping
	// these counts consistent with the materialized view, which uses
	// `JOIN issues i2 ON ...` for the same lists and therefore drops
	// any edge whose endpoint isn't a known issue (bv-issue#143/#144).
	statusByID := make(map[string]model.Status, len(e.Issues))
	for _, issue := range e.Issues {
		if issue == nil {
			continue
		}
		statusByID[issue.ID] = issue.Status
	}
	// isActiveCounterpart returns true when `id` is a known issue that
	// is neither closed nor tombstoned. Unknown ids return false so a
	// dangling dep edge doesn't get counted on either side.
	isActiveCounterpart := func(id string) bool {
		s, ok := statusByID[id]
		if !ok {
			return false
		}
		return !s.IsClosed() && !s.IsTombstone()
	}

	// Build dependency lookup maps. Count an edge only when *both*
	// endpoints are active issues — closing a blocker drops its
	// dependents' blocked_by_count to zero (the user-visible bug),
	// and a dep whose other end fell out of the export at all
	// shouldn't pad the counts above what the materialized view
	// surfaces.
	blocksCount := make(map[string]int)
	blockedByCount := make(map[string]int)
	for _, dep := range e.Deps {
		if dep == nil || !dep.Type.IsBlocking() {
			continue
		}
		// dep.IssueID depends on dep.DependsOnID, so:
		// - DependsOnID blocks IssueID
		// - IssueID is blocked by DependsOnID
		if !isActiveCounterpart(dep.DependsOnID) || !isActiveCounterpart(dep.IssueID) {
			continue
		}
		blocksCount[dep.DependsOnID]++
		blockedByCount[dep.IssueID]++
	}

	// Get triage scores if available
	triageScores := make(map[string]float64)
	if e.Triage != nil {
		for _, rec := range e.Triage.Recommendations {
			triageScores[rec.ID] = rec.Score
		}
	}

	// Get metrics maps from stats
	pageRankMap := e.Stats.PageRank()
	betweennessMap := e.Stats.Betweenness()
	criticalPathMap := e.Stats.CriticalPathScore()

	for _, issue := range e.Issues {
		id := issue.ID
		pr := pageRankMap[id]
		bw := betweennessMap[id]
		cp := int(criticalPathMap[id])
		score := triageScores[id]
		blocks := blocksCount[id]
		blockedBy := blockedByCount[id]

		_, err := stmt.Exec(id, pr, bw, cp, score, blocks, blockedBy)
		if err != nil {
			return fmt.Errorf("insert metrics for %s: %w", id, err)
		}
	}

	return tx.Commit()
}

// insertTriageRecommendations inserts triage recommendations.
func (e *SQLiteExporter) insertTriageRecommendations(db *sql.DB) error {
	if e.Triage == nil || len(e.Triage.Recommendations) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO triage_recommendations (issue_id, score, action, reasons, unblocks_ids, blocked_by_ids)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, rec := range e.Triage.Recommendations {
		reasonsJSON, _ := json.Marshal(rec.Reasons)
		unblocksJSON, _ := json.Marshal(rec.UnblocksIDs)
		blockedByJSON, _ := json.Marshal(rec.BlockedBy)

		_, err := stmt.Exec(
			rec.ID,
			rec.Score,
			rec.Action,
			string(reasonsJSON),
			string(unblocksJSON),
			string(blockedByJSON),
		)
		if err != nil {
			return fmt.Errorf("insert triage for %s: %w", rec.ID, err)
		}
	}

	return tx.Commit()
}

// populateOverviewMetrics updates issue_overview_mv with metrics derived from graph analysis.
func (e *SQLiteExporter) populateOverviewMetrics(db *sql.DB) error {
	if e.Stats == nil {
		return nil
	}

	cycles := e.Stats.Cycles()
	if len(cycles) == 0 {
		return nil
	}

	cycleNodes := make(map[string]struct{})
	for _, cycle := range cycles {
		for _, id := range cycle {
			cycleNodes[id] = struct{}{}
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`UPDATE issue_overview_mv SET in_cycle = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for id := range cycleNodes {
		if _, err := stmt.Exec(1, id); err != nil {
			return fmt.Errorf("update in_cycle for %s: %w", id, err)
		}
	}

	return tx.Commit()
}

// insertMeta inserts export metadata.
func (e *SQLiteExporter) insertMeta(db *sql.DB) error {
	meta := map[string]string{
		"version":          "1.0.0",
		"generated_at":     time.Now().UTC().Format(time.RFC3339),
		"issue_count":      fmt.Sprintf("%d", len(e.Issues)),
		"dependency_count": fmt.Sprintf("%d", len(e.Deps)),
		"schema_version":   fmt.Sprintf("%d", SchemaVersion),
	}

	if e.gitHash != "" {
		meta["git_commit"] = e.gitHash
	}
	if e.Config.Title != "" {
		meta["title"] = e.Config.Title
	}

	for key, value := range meta {
		if err := InsertMetaValue(db, key, value); err != nil {
			return fmt.Errorf("insert meta %s: %w", key, err)
		}
	}

	return nil
}

// writeRobotOutputs writes JSON files for robot outputs.
func (e *SQLiteExporter) writeRobotOutputs(dataDir string) error {
	// Write triage output
	if e.Triage != nil {
		if err := e.writeRobotJSON(filepath.Join(dataDir, "triage.json"), e.Triage); err != nil {
			return fmt.Errorf("write triage.json: %w", err)
		}

		// Also emit a compact project_health.json for fast robot consumption
		if err := e.writeRobotJSON(filepath.Join(dataDir, "project_health.json"), e.Triage.ProjectHealth); err != nil {
			return fmt.Errorf("write project_health.json: %w", err)
		}
	}

	// Write export metadata
	meta := ExportMeta{
		Version:     "1.0.0",
		GeneratedAt: time.Now().UTC(),
		GitCommit:   e.gitHash,
		IssueCount:  len(e.Issues),
		DepCount:    len(e.Deps),
		Title:       e.Config.Title,
	}
	if err := e.writeRobotJSON(filepath.Join(dataDir, "meta.json"), meta); err != nil {
		return fmt.Errorf("write meta.json: %w", err)
	}

	return nil
}

func (e *SQLiteExporter) writeRobotJSON(path string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding export payload: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("reading export payload fields: %w", err)
	}
	for key, value := range e.Config.RobotEnvelope {
		fields[key] = value
	}
	return writeJSON(path, fields)
}

// chunkIfNeeded splits the database into chunks if it exceeds the threshold.
func (e *SQLiteExporter) chunkIfNeeded(outputDir, dbPath string) error {
	info, err := os.Stat(dbPath)
	if err != nil {
		return err
	}

	// Write chunk config regardless of whether we chunk
	config := ChunkConfig{
		TotalSize: info.Size(),
	}

	// Always compute hash for cache invalidation (bv-pages-cache-fix)
	// The hash is used by viewer.js OPFS caching to determine if cached data is stale.
	// Without this, all deployments use the same "default" cache key and old data persists.
	f, err := os.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database for hashing: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return fmt.Errorf("hash database: %w", err)
	}
	config.Hash = hex.EncodeToString(hasher.Sum(nil))

	if info.Size() < e.Config.ChunkThreshold {
		config.Chunked = false
		// A previous export of a larger DB may have left a chunks/ directory
		// behind. The config we are about to write says chunked=false, but the
		// stale chunk files would still be served (HTTP 200), so a client/cache
		// holding the prior chunked config would reassemble a malformed DB image
		// ("database disk image is malformed", #175). Remove the chunk artifacts
		// this exporter owns so the unchunked export leaves nothing behind.
		if err := removeChunkArtifacts(filepath.Join(outputDir, "chunks")); err != nil {
			return err
		}
		return writeJSON(filepath.Join(outputDir, "beads.sqlite3.config.json"), config)
	}

	// Reset file position for chunking
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek database for chunking: %w", err)
	}

	// Chunk the database (file f is already open and seeked to start)

	chunksDir := filepath.Join(outputDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return fmt.Errorf("create chunks dir: %w", err)
	}

	// Split into chunks
	chunkNum := 0
	buf := make([]byte, e.Config.ChunkSize)

	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunkPath := filepath.Join(chunksDir, fmt.Sprintf("%05d.bin", chunkNum))
			if err := os.WriteFile(chunkPath, buf[:n], 0644); err != nil {
				return fmt.Errorf("write chunk %d: %w", chunkNum, err)
			}
			chunkNum++
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read for chunk: %w", err)
		}
	}

	// Remove any orphaned chunks from a previous, larger export so chunks/ holds
	// exactly the chunks this export produced. Without this, a DB that shrinks
	// across a chunk boundary (e.g. after a recovery/rebuild/VACUUM) leaves stale
	// higher-numbered chunk files behind; a client/cache still holding the prior
	// chunk config then splices new[0..N-1] + stale[N..M-1] into a malformed
	// SQLite image (#175). Pruning happens after the new chunks are written so a
	// reader is never left without the low-numbered chunks mid-export.
	if err := pruneStaleChunks(chunksDir, chunkNum); err != nil {
		return err
	}

	// Populate chunk metadata
	config.Chunked = true
	config.ChunkCount = chunkNum
	config.ChunkSize = e.Config.ChunkSize
	config.Chunks = make([]ChunkInfo, 0, chunkNum)

	// Re-read chunks to record paths and hashes
	for i := 0; i < chunkNum; i++ {
		name := fmt.Sprintf("%05d.bin", i)
		path := filepath.Join("chunks", name)
		fullPath := filepath.Join(outputDir, path)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return fmt.Errorf("hash chunk %d: %w", i, err)
		}
		h := sha256.Sum256(data)
		config.Chunks = append(config.Chunks, ChunkInfo{
			Path: path,
			Hash: hex.EncodeToString(h[:]),
			Size: int64(len(data)),
		})
	}

	return writeJSON(filepath.Join(outputDir, "beads.sqlite3.config.json"), config)
}

// chunkFileIndex returns the chunk index encoded in a "NNNNN.bin" file name and
// whether the name matches the exact chunk-file pattern this exporter produces
// (five digits + ".bin"). This is deliberately strict so chunk cleanup only ever
// touches files the exporter itself created, never any other file a user may
// have placed under the export's chunks/ directory.
func chunkFileIndex(name string) (int, bool) {
	stem, ok := strings.CutSuffix(name, ".bin")
	if !ok || len(stem) != 5 {
		return 0, false
	}
	for _, r := range stem {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	idx, err := strconv.Atoi(stem)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// pruneStaleChunks removes "NNNNN.bin" chunk files in chunksDir whose index is
// >= keep. It only removes files matching the exporter's own chunk naming
// (chunkFileIndex), so it never deletes unrelated content. A missing chunks
// directory is not an error.
func pruneStaleChunks(chunksDir string, keep int) error {
	entries, err := os.ReadDir(chunksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("list chunks dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		idx, ok := chunkFileIndex(entry.Name())
		if !ok || idx < keep {
			continue
		}
		if err := os.Remove(filepath.Join(chunksDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale chunk %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// removeChunkArtifacts removes every chunk file this exporter owns from chunksDir
// and then removes the directory itself if it is left empty. Used when an export
// falls below the chunk threshold so a prior chunked export leaves nothing
// behind (#175). os.Remove on the directory only succeeds when it is empty, so
// any unrelated user file keeps both the file and the directory intact — this
// never does a recursive delete.
func removeChunkArtifacts(chunksDir string) error {
	if err := pruneStaleChunks(chunksDir, 0); err != nil {
		return err
	}
	// Best-effort: drop the directory only if our chunks were the only contents.
	_ = os.Remove(chunksDir)
	return nil
}

// writeJSON writes data as JSON to a file.
func writeJSON(path string, data interface{}) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	err = enc.Encode(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// GetExportedIssues converts issues to ExportIssue format.
func (e *SQLiteExporter) GetExportedIssues() []ExportIssue {
	// Build dependency lookup maps
	blocksIDs := make(map[string][]string)
	blockedByIDs := make(map[string][]string)
	for _, dep := range e.Deps {
		if dep != nil && dep.Type.IsBlocking() {
			// dep.IssueID depends on dep.DependsOnID, so:
			// - DependsOnID blocks IssueID
			// - IssueID is blocked by DependsOnID
			blocksIDs[dep.DependsOnID] = append(blocksIDs[dep.DependsOnID], dep.IssueID)
			blockedByIDs[dep.IssueID] = append(blockedByIDs[dep.IssueID], dep.DependsOnID)
		}
	}

	// Get triage scores
	triageScores := make(map[string]float64)
	if e.Triage != nil {
		for _, rec := range e.Triage.Recommendations {
			triageScores[rec.ID] = rec.Score
		}
	}

	result := make([]ExportIssue, len(e.Issues))
	for i, issue := range e.Issues {
		exp := ExportIssue{
			ID:                 issue.ID,
			Title:              issue.Title,
			Description:        issue.Description,
			Design:             issue.Design,
			AcceptanceCriteria: issue.AcceptanceCriteria,
			Notes:              issue.Notes,
			Status:             issue.Status,
			Priority:           issue.Priority,
			IssueType:          issue.IssueType,
			Assignee:           issue.Assignee,
			Labels:             issue.Labels,
			CreatedAt:          issue.CreatedAt,
			UpdatedAt:          issue.UpdatedAt,
			ClosedAt:           issue.ClosedAt,
		}

		if m := e.Metrics; m != nil {
			if mm, ok := m[issue.ID]; ok && mm != nil {
				exp.PageRank = mm.PageRank
				exp.Betweenness = mm.Betweenness
				exp.CriticalPath = mm.CriticalPathDepth
				exp.TriageScore = mm.TriageScore
				exp.BlocksCount = mm.BlocksCount
				exp.BlockedByCount = mm.BlockedByCount
			}
		} else if e.Stats != nil {
			exp.PageRank = e.Stats.GetPageRankScore(issue.ID)
			exp.Betweenness, _ = e.Stats.BetweennessValue(issue.ID) // O(1) lookup via bv-77ec
			exp.CriticalPath = int(e.Stats.GetCriticalPathScore(issue.ID))
		}

		// Fallback triage score from recommendations map
		if exp.TriageScore == 0 {
			exp.TriageScore = triageScores[issue.ID]
		}
		// Fallback blocker counts
		if exp.BlocksCount == 0 {
			exp.BlocksIDs = blocksIDs[issue.ID]
			exp.BlocksCount = len(exp.BlocksIDs)
		}
		if exp.BlockedByCount == 0 {
			exp.BlockedByIDs = blockedByIDs[issue.ID]
			exp.BlockedByCount = len(exp.BlockedByIDs)
		}
		// Always set IDs for downstream UI
		if exp.BlocksIDs == nil {
			exp.BlocksIDs = blocksIDs[issue.ID]
		}
		if exp.BlockedByIDs == nil {
			exp.BlockedByIDs = blockedByIDs[issue.ID]
		}

		result[i] = exp
	}

	return result
}

// ExportToJSON exports issues to a JSON file (alternative to SQLite).
func (e *SQLiteExporter) ExportToJSON(path string) error {
	issues := e.GetExportedIssues()

	// Use Config.Title or fallback to default
	title := e.Config.Title
	if title == "" {
		title = "Beads Export"
	}

	output := struct {
		Meta   ExportMeta    `json:"meta"`
		Issues []ExportIssue `json:"issues"`
	}{
		Meta: ExportMeta{
			Version:     "1.0.0",
			GeneratedAt: time.Now().UTC(),
			GitCommit:   e.gitHash,
			IssueCount:  len(issues),
			DepCount:    len(e.Deps),
			Title:       title,
		},
		Issues: issues,
	}

	return e.writeRobotJSON(path, output)
}

// stringSliceContains checks if a string slice contains a value.
func stringSliceContains(slice []string, val string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, val) {
			return true
		}
	}
	return false
}

// GraphLayout is a compact representation of pre-computed graph layout data.
// This is much smaller than full node data (~30KB vs ~200KB) for fast initial load.
type GraphLayout struct {
	Positions   map[string][2]float64 `json:"positions"`
	Metrics     map[string][5]float64 `json:"metrics"`
	Links       [][2]string           `json:"links"`
	Cycles      [][]string            `json:"cycles,omitempty"`
	Version     string                `json:"version"`
	GeneratedAt string                `json:"generated_at"`
	NodeCount   int                   `json:"node_count"`
	EdgeCount   int                   `json:"edge_count"`
}

// writeGraphLayout generates compact pre-computed graph layout data.
func (e *SQLiteExporter) writeGraphLayout(dataDir string) error {
	blockedBy := make(map[string][]string)
	blocks := make(map[string][]string)

	for _, dep := range e.Deps {
		if dep != nil && dep.Type.IsBlocking() {
			blockedBy[dep.IssueID] = append(blockedBy[dep.IssueID], dep.DependsOnID)
			blocks[dep.DependsOnID] = append(blocks[dep.DependsOnID], dep.IssueID)
		}
	}

	depth := make(map[string]int)
	if e.Stats != nil && len(e.Stats.TopologicalOrder) > 0 {
		// Use topological order (dependencies first) to compute longest depth deterministically.
		for _, id := range e.Stats.TopologicalOrder {
			if _, ok := depth[id]; !ok {
				depth[id] = 0
			}
			for _, child := range blocks[id] {
				if depth[child] < depth[id]+1 {
					depth[child] = depth[id] + 1
				}
			}
		}
	} else {
		var roots []string
		for _, issue := range e.Issues {
			if len(blockedBy[issue.ID]) == 0 {
				roots = append(roots, issue.ID)
				depth[issue.ID] = 0
			}
		}

		queue := roots
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			currentDepth := depth[current]
			for _, child := range blocks[current] {
				if _, visited := depth[child]; !visited {
					depth[child] = currentDepth + 1
					queue = append(queue, child)
				} else if depth[child] < currentDepth+1 {
					depth[child] = currentDepth + 1
				}
			}
		}
	}

	for _, issue := range e.Issues {
		if _, visited := depth[issue.ID]; !visited {
			depth[issue.ID] = 0
		}
	}

	depthGroups := make(map[int][]string)
	maxDepth := 0
	for id, d := range depth {
		depthGroups[d] = append(depthGroups[d], id)
		if d > maxDepth {
			maxDepth = d
		}
	}

	positions := make(map[string][2]float64)
	xSpacing := 200.0
	ySpacing := 80.0

	for d := 0; d <= maxDepth; d++ {
		nodesAtDepth := depthGroups[d]
		count := len(nodesAtDepth)
		startY := -float64(count-1) * ySpacing / 2
		for i, id := range nodesAtDepth {
			positions[id] = [2]float64{
				float64(d) * xSpacing,
				startY + float64(i)*ySpacing,
			}
		}
	}

	cycleNodes := make(map[string]bool)
	var cycles [][]string
	if e.Stats != nil {
		cycles = e.Stats.Cycles()
		for _, cycle := range cycles {
			for _, id := range cycle {
				cycleNodes[id] = true
			}
		}
	}

	metrics := make(map[string][5]float64)
	for _, issue := range e.Issues {
		var pr, bt float64
		if e.Stats != nil {
			pr = e.Stats.GetPageRankScore(issue.ID)
			bt = e.Stats.GetBetweennessScore(issue.ID)
		}
		inCycle := 0.0
		if cycleNodes[issue.ID] {
			inCycle = 1.0
		}
		metrics[issue.ID] = [5]float64{
			pr, bt,
			float64(len(blockedBy[issue.ID])),
			float64(len(blocks[issue.ID])),
			inCycle,
		}
	}

	var links [][2]string
	for _, dep := range e.Deps {
		if dep != nil && dep.Type.IsBlocking() {
			links = append(links, [2]string{dep.DependsOnID, dep.IssueID})
		}
	}

	layout := GraphLayout{
		Positions:   positions,
		Metrics:     metrics,
		Links:       links,
		Cycles:      cycles,
		Version:     "1.0.0",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		NodeCount:   len(e.Issues),
		EdgeCount:   len(links),
	}

	return e.writeRobotJSON(filepath.Join(dataDir, "graph_layout.json"), layout)
}
