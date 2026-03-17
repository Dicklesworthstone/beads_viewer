package datasource

import (
	"bufio"
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	json "github.com/goccy/go-json"
	_ "modernc.org/sqlite"
)

// ValidationOptions configures source validation behavior
type ValidationOptions struct {
	// MaxJSONLErrorRate is the maximum fraction of parse errors tolerated (0.0-1.0)
	// Default: 0.10 (10%)
	MaxJSONLErrorRate float64
	// RequiredFields specifies fields that must be present in JSONL issues
	// Default: ["id", "title", "status"]
	RequiredFields []string
	// CountIssues whether to count issues during validation
	CountIssues bool
	// Verbose enables detailed logging during validation
	Verbose bool
	// Logger receives log messages when Verbose is true
	Logger func(msg string)
}

// DefaultValidationOptions returns sensible default validation options
func DefaultValidationOptions() ValidationOptions {
	return ValidationOptions{
		MaxJSONLErrorRate: 0.10,
		RequiredFields:    []string{"id", "title", "status"},
		CountIssues:       true,
		Verbose:           false,
		Logger:            func(string) {},
	}
}

// ValidateSource validates a data source and updates its Valid field
func ValidateSource(source *DataSource) error {
	return ValidateSourceWithOptions(source, DefaultValidationOptions())
}

// ValidateSourceWithOptions validates a data source with custom options
func ValidateSourceWithOptions(source *DataSource, opts ValidationOptions) error {
	if opts.Logger == nil {
		opts.Logger = func(string) {}
	}
	if opts.MaxJSONLErrorRate == 0 {
		opts.MaxJSONLErrorRate = 0.10
	}
	if len(opts.RequiredFields) == 0 {
		opts.RequiredFields = []string{"id", "title", "status"}
	}

	var err error
	switch source.Type {
	case SourceTypeSQLite:
		err = validateSQLite(source, opts)
	case SourceTypeJSONLLocal, SourceTypeJSONLWorktree:
		err = validateJSONL(source, opts)
	case SourceTypeDolt:
		err = validateDolt(source, opts)
	default:
		err = fmt.Errorf("unknown source type: %s", source.Type)
	}

	if err != nil {
		source.Valid = false
		source.ValidationError = err.Error()
		return err
	}

	source.Valid = true
	source.ValidationError = ""
	return nil
}

// validateSQLite validates a SQLite database
func validateSQLite(source *DataSource, opts ValidationOptions) error {
	// Check file exists and is readable
	info, err := os.Stat(source.Path)
	if err != nil {
		return fmt.Errorf("cannot access file: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("path is a directory, not a file")
	}

	// Open database
	db, err := sql.Open("sqlite", source.Path)
	if err != nil {
		return fmt.Errorf("cannot open database: %w", err)
	}
	defer db.Close()

	// Run integrity check
	var integrity string
	err = db.QueryRow("PRAGMA integrity_check").Scan(&integrity)
	if err != nil {
		return fmt.Errorf("integrity check failed: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("database corrupt: %s", integrity)
	}

	// Check for issues table
	var tableName string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='issues'").Scan(&tableName)
	if err == sql.ErrNoRows {
		return fmt.Errorf("missing issues table")
	}
	if err != nil {
		return fmt.Errorf("cannot query schema: %w", err)
	}

	// Check required columns exist
	rows, err := db.Query("PRAGMA table_info(issues)")
	if err != nil {
		return fmt.Errorf("cannot query table info: %w", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("cannot scan column info: %w", err)
		}
		columns[strings.ToLower(name)] = true
	}

	requiredCols := []string{"id", "title", "status"}
	for _, col := range requiredCols {
		if !columns[col] {
			return fmt.Errorf("missing required column: %s", col)
		}
	}

	// Count issues if requested
	if opts.CountIssues {
		var count int
		err = db.QueryRow("SELECT COUNT(*) FROM issues WHERE (tombstone IS NULL OR tombstone = 0)").Scan(&count)
		if err != nil {
			// Try without tombstone filter (column might not exist)
			err = db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&count)
			if err != nil {
				return fmt.Errorf("cannot count issues: %w", err)
			}
		}
		source.IssueCount = count
	}

	if opts.Verbose {
		opts.Logger(fmt.Sprintf("SQLite validation passed: %s (%d issues)", source.Path, source.IssueCount))
	}

	return nil
}

// validateJSONL validates a JSONL file
func validateJSONL(source *DataSource, opts ValidationOptions) error {
	// Check file exists and is readable
	info, err := os.Stat(source.Path)
	if err != nil {
		return fmt.Errorf("cannot access file: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("path is a directory, not a file")
	}

	// Empty file is valid (0 issues)
	if info.Size() == 0 {
		source.IssueCount = 0
		if opts.Verbose {
			opts.Logger(fmt.Sprintf("JSONL validation passed (empty): %s", source.Path))
		}
		return nil
	}

	// Open file
	file, err := os.Open(source.Path)
	if err != nil {
		return fmt.Errorf("cannot open file: %w", err)
	}
	defer file.Close()

	// Parse and validate each line
	reader := bufio.NewReaderSize(file, 1024*1024) // 1MB buffer
	lineNum := 0
	validLines := 0
	errorLines := 0

	for {
		lineNum++
		line, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read error at line %d: %w", lineNum, err)
		}

		// Handle long lines by reading the rest
		if isPrefix {
			var fullLine []byte
			fullLine = append(fullLine, line...)
			for isPrefix {
				line, isPrefix, err = reader.ReadLine()
				if err != nil && err != io.EOF {
					return fmt.Errorf("read error at line %d: %w", lineNum, err)
				}
				fullLine = append(fullLine, line...)
				if err == io.EOF {
					break
				}
			}
			line = fullLine
		}

		// Skip empty lines
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		// Strip BOM from first line
		if lineNum == 1 && bytes.HasPrefix(line, []byte{0xEF, 0xBB, 0xBF}) {
			line = line[3:]
		}

		// Parse JSON
		var issue map[string]interface{}
		if err := json.Unmarshal(line, &issue); err != nil {
			errorLines++
			if opts.Verbose {
				opts.Logger(fmt.Sprintf("Parse error at line %d: %v", lineNum, err))
			}
			continue
		}

		// Check required fields
		missingField := false
		for _, field := range opts.RequiredFields {
			if _, ok := issue[field]; !ok {
				missingField = true
				if opts.Verbose {
					opts.Logger(fmt.Sprintf("Missing field '%s' at line %d", field, lineNum))
				}
				break
			}
		}
		if missingField {
			errorLines++
			continue
		}

		validLines++
	}

	// Check error rate
	totalLines := validLines + errorLines
	if totalLines > 0 {
		errorRate := float64(errorLines) / float64(totalLines)
		if errorRate > opts.MaxJSONLErrorRate {
			return fmt.Errorf("too many errors: %.1f%% (max %.1f%%)", errorRate*100, opts.MaxJSONLErrorRate*100)
		}
	}

	if opts.CountIssues {
		source.IssueCount = validLines
	}

	if opts.Verbose {
		opts.Logger(fmt.Sprintf("JSONL validation passed: %s (%d issues, %d errors)", source.Path, validLines, errorLines))
	}

	return nil
}

// validateDolt validates a Dolt data source by connecting and checking schema.
func validateDolt(source *DataSource, opts ValidationOptions) error {
	db, err := sql.Open("mysql", source.Path)
	if err != nil {
		return fmt.Errorf("cannot open Dolt connection: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("cannot connect to Dolt server: %w", err)
	}

	// Check issues table exists with required columns
	var count int
	for _, col := range []string{"id", "title", "status"} {
		err := db.QueryRow(`
			SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_NAME = 'issues' AND COLUMN_NAME = ?
		`, col).Scan(&count)
		if err != nil || count == 0 {
			return fmt.Errorf("missing required column: %s", col)
		}
	}

	if opts.CountIssues {
		err = db.QueryRow("SELECT COUNT(*) FROM issues WHERE (tombstone IS NULL OR tombstone = 0)").Scan(&count)
		if err != nil {
			return fmt.Errorf("cannot count issues: %w", err)
		}
		source.IssueCount = count
	}

	if opts.Verbose {
		opts.Logger(fmt.Sprintf("Dolt validation passed: %s (%d issues)", source.Path, source.IssueCount))
	}
	return nil
}

// IsSourceAccessible quickly checks if a source file is accessible
func IsSourceAccessible(source *DataSource) bool {
	_, err := os.Stat(source.Path)
	return err == nil
}

// RefreshSourceInfo updates the ModTime and Size of a source from disk
func RefreshSourceInfo(source *DataSource) error {
	info, err := os.Stat(source.Path)
	if err != nil {
		return fmt.Errorf("cannot access file: %w", err)
	}
	source.ModTime = info.ModTime()
	source.Size = info.Size()
	return nil
}
