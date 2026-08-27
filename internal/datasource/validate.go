package datasource

import (
	"bufio"
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
	"time"

	json "github.com/goccy/go-json"
	_ "modernc.org/sqlite"
)

// validationCacheEntry records a prior validation result for a specific file
// identity. It is keyed so that observable on-disk changes invalidate the
// cache and force a fresh validation.
type validationCacheEntry struct {
	identity   validationFileIdentity
	valid      bool
	validErr   string
	issueCount int
}

type validationCacheKey struct {
	path       string
	sourceType SourceType
}

type validationFileIdentity struct {
	main validationPathIdentity
	wal  validationPathIdentity
}

func (identity validationFileIdentity) equal(other validationFileIdentity) bool {
	return identity.main.equal(other.main) && identity.wal.equal(other.wal)
}

type validationPathIdentity struct {
	exists      bool
	modTime     time.Time
	size        int64
	info        os.FileInfo
	changeSec   int64
	changeNsec  int64
	hasChangeAt bool
}

func (identity validationPathIdentity) equal(other validationPathIdentity) bool {
	if identity.exists != other.exists {
		return false
	}
	if !identity.exists {
		return true
	}
	if !identity.modTime.Equal(other.modTime) || identity.size != other.size {
		return false
	}
	if identity.info != nil && other.info != nil && !os.SameFile(identity.info, other.info) {
		return false
	}
	return !identity.hasChangeAt || !other.hasChangeAt ||
		(identity.changeSec == other.changeSec && identity.changeNsec == other.changeNsec)
}

// validationCache memoizes validation results within a single process. The hot
// robot CLI paths (LoadIssues + resolveSingleRepoWatchFile) each discover and
// validate the same source files; without this cache the 1.9MB issues.jsonl is
// fully re-parsed 2-3x per invocation. Keyed by absolute path with metadata,
// filesystem identity, change-time, and SQLite WAL guards.
var (
	validationCacheMu sync.Mutex
	validationCache   = map[validationCacheKey]validationCacheEntry{}
)

func sourceValidationCacheKey(source *DataSource) validationCacheKey {
	return validationCacheKey{path: source.Path, sourceType: source.Type}
}

// lookupValidationCache returns a cached validation result for source if one
// exists and the complete file identity still matches.
func lookupValidationCache(source *DataSource, opts ValidationOptions) (bool, error) {
	identity, err := sourceValidationIdentity(source)
	if err != nil {
		return false, nil
	}
	validationCacheMu.Lock()
	entry, ok := validationCache[sourceValidationCacheKey(source)]
	validationCacheMu.Unlock()
	if !ok || !entry.identity.equal(identity) {
		return false, nil
	}
	// Cache hit: replay the recorded result onto the source.
	source.Valid = entry.valid
	source.ValidationError = entry.validErr
	if opts.CountIssues {
		source.IssueCount = entry.issueCount
	}
	if entry.valid {
		return true, nil
	}
	return true, fmt.Errorf("%s", entry.validErr)
}

// storeValidationCache records the outcome of a validation for later reuse.
func storeValidationCache(source *DataSource) {
	identity, err := sourceValidationIdentity(source)
	if err != nil {
		return
	}
	storeValidationCacheWithIdentity(source, identity)
}

func storeValidationCacheWithIdentity(source *DataSource, identity validationFileIdentity) {
	validationCacheMu.Lock()
	validationCache[sourceValidationCacheKey(source)] = validationCacheEntry{
		identity:   identity,
		valid:      source.Valid,
		validErr:   source.ValidationError,
		issueCount: source.IssueCount,
	}
	validationCacheMu.Unlock()
}

func storeValidationCacheIfUnchanged(source *DataSource, before validationFileIdentity, haveBefore bool) {
	if !haveBefore {
		return
	}
	after, err := sourceValidationIdentity(source)
	if err != nil || !before.equal(after) {
		return
	}
	storeValidationCacheWithIdentity(source, after)
}

func sourceValidationIdentity(source *DataSource) (validationFileIdentity, error) {
	info, err := os.Stat(source.Path)
	if err != nil {
		return validationFileIdentity{}, err
	}
	identity := validationFileIdentity{main: validationPathIdentityFromInfo(info)}
	if source.Type != SourceTypeSQLite {
		return identity, nil
	}

	walInfo, err := os.Stat(source.Path + "-wal")
	if err != nil {
		if os.IsNotExist(err) {
			return identity, nil
		}
		return validationFileIdentity{}, err
	}
	identity.wal = validationPathIdentityFromInfo(walInfo)
	return identity, nil
}

func validationPathIdentityFromInfo(info os.FileInfo) validationPathIdentity {
	if info == nil {
		return validationPathIdentity{}
	}
	changeSec, changeNsec, hasChangeAt := validationFileChangeTime(info)
	return validationPathIdentity{
		exists:      true,
		modTime:     info.ModTime(),
		size:        info.Size(),
		info:        info,
		changeSec:   changeSec,
		changeNsec:  changeNsec,
		hasChangeAt: hasChangeAt,
	}
}

// validationFileChangeTime extracts the inode metadata-change timestamp used
// by Unix os.FileInfo implementations. It supplements size/mtime and file
// identity so restoring an old mtime after an in-place rewrite cannot replay a
// stale validation verdict. Platforms without this metadata retain the
// identity plus size/mtime fallback.
func validationFileChangeTime(info os.FileInfo) (seconds, nanoseconds int64, ok bool) {
	if info == nil || info.Sys() == nil {
		return 0, 0, false
	}
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, 0, false
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	for _, fieldName := range []string{"Ctim", "Ctimespec"} {
		stamp := value.FieldByName(fieldName)
		if !stamp.IsValid() || stamp.Kind() != reflect.Struct {
			continue
		}
		seconds, secondsOK := validationSignedIntegerField(stamp, "Sec")
		nanoseconds, nanosOK := validationSignedIntegerField(stamp, "Nsec")
		if secondsOK && nanosOK {
			return seconds, nanoseconds, true
		}
	}
	seconds, secondsOK := validationSignedIntegerField(value, "Ctime")
	nanoseconds, nanosOK := validationSignedIntegerField(value, "Ctimensec")
	if secondsOK {
		return seconds, nanoseconds, !nanosOK || nanoseconds >= 0
	}
	return 0, 0, false
}

func validationSignedIntegerField(value reflect.Value, name string) (int64, bool) {
	field := value.FieldByName(name)
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return field.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		unsigned := field.Uint()
		if unsigned > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(unsigned), true
	default:
		return 0, false
	}
}

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
	if opts.MaxJSONLErrorRate < 0 {
		opts.MaxJSONLErrorRate = 0.10
	}
	if len(opts.RequiredFields) == 0 {
		opts.RequiredFields = []string{"id", "title", "status"}
	}

	// Reuse a prior in-process validation of the same unchanged file. Only the
	// default-semantics path is cached: custom RequiredFields/error-rate or a
	// no-count request would change what "valid"/IssueCount mean, so those
	// always validate freshly. This collapses the redundant 2nd/3rd full parse
	// of the same source on the warm robot path.
	cacheable := opts.CountIssues &&
		opts.MaxJSONLErrorRate == DefaultValidationOptions().MaxJSONLErrorRate &&
		isDefaultRequiredFields(opts.RequiredFields)
	var validationStart validationFileIdentity
	haveValidationStart := false
	if cacheable {
		var identityErr error
		validationStart, identityErr = sourceValidationIdentity(source)
		haveValidationStart = identityErr == nil
		if hit, hitErr := lookupValidationCache(source, opts); hit {
			return hitErr
		}
	}

	var err error
	switch source.Type {
	case SourceTypeSQLite:
		err = validateSQLite(source, opts)
	case SourceTypeJSONLLocal, SourceTypeJSONLWorktree:
		err = validateJSONL(source, opts)
	default:
		err = fmt.Errorf("unknown source type: %s", source.Type)
	}

	if err != nil {
		source.Valid = false
		source.ValidationError = err.Error()
		if cacheable {
			storeValidationCacheIfUnchanged(source, validationStart, haveValidationStart)
		}
		return err
	}

	source.Valid = true
	source.ValidationError = ""
	if cacheable {
		storeValidationCacheIfUnchanged(source, validationStart, haveValidationStart)
	}
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
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(source.Path))
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

	// Check required columns exist. tableColumns closes and checks the metadata
	// rows before the validation proceeds to another query.
	columns, err := tableColumns(db, "issues")
	if err != nil {
		return fmt.Errorf("cannot inspect issues schema: %w", err)
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
		query := "SELECT COUNT(*) FROM issues " + liveIssueWhereClause(columns, "")
		if err := db.QueryRow(query).Scan(&count); err != nil {
			return fmt.Errorf("cannot count issues: %w", err)
		}
		source.IssueCount = count
	}

	if opts.Verbose {
		opts.Logger(fmt.Sprintf("SQLite validation passed: %s (%d issues)", source.Path, source.IssueCount))
	}

	return nil
}

// validationProbe is the minimal view of a JSONL line that validation needs.
// It only records whether each required field is present, never the full value,
// so well-formed lines can be checked with the cheap typed structDecoder path
// instead of the generic interface{}/map reflection path.
type validationProbe interface {
	// has reports whether the named field was present in the decoded line.
	has(field string) bool
}

// defaultProbe captures presence of the default required fields
// (id/title/status). It uses NON-pointer json.RawMessage values: an absent field
// stays nil (len 0), while a PRESENT field — including an explicit `null` — gets
// its raw bytes (e.g. the 4 bytes "null", len 4). So presence == len(raw) > 0.
// This matches the old map[string]interface{} semantics (and the rawProbe path),
// where "id":null counts as present, not missing. goccy decodes this via
// structDecoder, avoiding interfaceDecoder/mapDecoder.
type defaultProbe struct {
	ID     json.RawMessage `json:"id"`
	Title  json.RawMessage `json:"title"`
	Status json.RawMessage `json:"status"`
}

func (p *defaultProbe) has(field string) bool {
	switch field {
	case "id":
		return len(p.ID) > 0
	case "title":
		return len(p.Title) > 0
	case "status":
		return len(p.Status) > 0
	default:
		return false
	}
}

// rawProbe handles arbitrary required-field sets. Decoding into
// map[string]json.RawMessage still avoids interfaceDecoder recursion into nested
// values (object/array values stay as raw bytes), so it is far cheaper than
// map[string]interface{} while remaining correct for any field name.
type rawProbe map[string]json.RawMessage

func (p rawProbe) has(field string) bool {
	_, ok := p[field]
	return ok
}

// isDefaultRequiredFields reports whether fields is exactly the default
// {id, title, status} set (in any order), enabling the typed fast path.
func isDefaultRequiredFields(fields []string) bool {
	if len(fields) != 3 {
		return false
	}
	var id, title, status bool
	for _, f := range fields {
		switch f {
		case "id":
			id = true
		case "title":
			title = true
		case "status":
			status = true
		default:
			return false
		}
	}
	return id && title && status
}

// validationProbeForFields returns the cheapest probe capable of checking the
// given required fields.
func validationProbeForFields(fields []string) validationProbe {
	if isDefaultRequiredFields(fields) {
		return &defaultProbe{}
	}
	return rawProbe{}
}

// unmarshalProbe decodes a single JSONL line into the probe. It dispatches to
// the concrete probe type so goccy can pick the typed structDecoder for the
// common default-field case.
func unmarshalProbe(line []byte, probe validationProbe) error {
	switch p := probe.(type) {
	case *defaultProbe:
		return json.Unmarshal(line, p)
	case rawProbe:
		return json.Unmarshal(line, &p)
	default:
		return json.Unmarshal(line, probe)
	}
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

		// Parse JSON.
		//
		// Validation only needs two guarantees: (1) the line is well-formed
		// JSON, and (2) the required discriminating fields are present. We do
		// NOT need to materialize the entire object, so we avoid the slow
		// generic decode into map[string]interface{} (goccy's
		// interfaceDecoder/mapDecoder reflection path) and instead decode into
		// a small typed struct (goccy's structDecoder) that only captures the
		// required fields. For the default {id,title,status} set, defaultProbe
		// uses json.RawMessage values so a present field — including an explicit
		// `null` — counts as present (len(raw) > 0), matching the old
		// map[string]interface{} semantics; an absent field stays nil (len 0).
		//
		// unmarshalProbe uses json.Unmarshal, which rejects any line that is not a
		// single well-formed JSON value (e.g. trailing garbage), giving the same
		// strictness the corruption-detection tests expect.
		probe := validationProbeForFields(opts.RequiredFields)
		if err := unmarshalProbe(line, probe); err != nil {
			errorLines++
			if opts.Verbose {
				opts.Logger(fmt.Sprintf("Parse error at line %d: %v", lineNum, err))
			}
			continue
		}

		// Check required fields
		missingField := false
		for _, field := range opts.RequiredFields {
			if !probe.has(field) {
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
