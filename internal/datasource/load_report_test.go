package datasource

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #190: an issue failing model validation (updated_at < created_at) must not be
// silently dropped — the load still succeeds, but its returned report records the
// drop with a count and a per-line reason.
func TestLoadReport_TimestampInvertedIssueIsCounted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")

	// 10 valid issues keep the error rate (1/11 ≈ 9.1%) under the smart
	// loader's 10% gate, so this exercises the fused load path.
	var b strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, `{"id":"bd-ok%d","title":"valid issue %d","status":"open","issue_type":"task","priority":2,"created_at":"2026-07-06T10:00:00Z","updated_at":"2026-07-06T11:00:00Z"}`+"\n", i, i)
	}
	b.WriteString(`{"id":"bd-test1","title":"timestamp-inverted issue","status":"open","issue_type":"task","priority":2,"created_at":"2026-07-06T17:42:31Z","updated_at":"2026-07-06T10:43:40Z"}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	loaded, err := loadAndValidateJSONL(DataSource{
		Type:     SourceTypeJSONLLocal,
		Path:     path,
		Priority: PriorityJSONLLocal,
	})
	issues := loaded.Issues
	if err != nil {
		t.Fatalf("loadAndValidateJSONL: %v", err)
	}
	if len(issues) != 10 {
		t.Fatalf("expected the 10 valid issues to load, got %d", len(issues))
	}

	rep := &loaded.Report
	if rep.Path != path {
		t.Errorf("report path = %q, want %q", rep.Path, path)
	}
	if rep.Valid != 10 {
		t.Errorf("report valid = %d, want 10", rep.Valid)
	}
	if rep.Errors != 1 {
		t.Errorf("report errors = %d, want 1 (the timestamp-inverted issue must be counted, not silently dropped)", rep.Errors)
	}
	if len(rep.Warnings) != 1 {
		t.Fatalf("report warnings = %v, want exactly one skip reason", rep.Warnings)
	}
	warning := rep.Warnings[0]
	if !strings.Contains(warning, "line 11") {
		t.Errorf("warning %q should name the file line (line 11)", warning)
	}
	if !strings.Contains(warning, "created_at") {
		t.Errorf("warning %q should carry the validation reason (updated_at before created_at)", warning)
	}
}

// #190: when the error rate trips the smart loader's gate (e.g. a tiny JSONL
// whose only record fails validation — the original repro), the legacy
// fallback load must still publish the drop accounting.
func TestLoadReport_LegacyFallbackRecordsDrops(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")
	content := `{"id":"bd-test1","title":"timestamp-inverted issue","status":"open","issue_type":"task","priority":2,"created_at":"2026-07-06T17:42:31Z","updated_at":"2026-07-06T10:43:40Z"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	loaded, err := loadLegacyJSONL(dir)
	issues := loaded.Issues
	if err != nil {
		t.Fatalf("loadLegacyJSONL: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected the invalid issue to be skipped by the tolerant parse, got %d issues", len(issues))
	}

	rep := &loaded.Report
	if rep.Valid != 0 || rep.Errors != 1 {
		t.Errorf("report valid/errors = %d/%d, want 0/1", rep.Valid, rep.Errors)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "created_at") {
		t.Errorf("report warnings = %v, want one reason mentioning created_at", rep.Warnings)
	}
}

// Each load keeps its own report, so later loads cannot replace earlier facts.
func TestLoadReport_CleanLoadDoesNotReplacePreviousAccounting(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "issues.jsonl")
	bad := `{"id":"bd-bad","title":"inverted","status":"open","issue_type":"task","created_at":"2026-07-06T17:00:00Z","updated_at":"2026-07-06T10:00:00Z"}` + "\n"
	if err := os.WriteFile(badPath, []byte(bad), 0o644); err != nil {
		t.Fatalf("write bad fixture: %v", err)
	}
	badLoad, err := loadLegacyJSONL(dir)
	if err != nil {
		t.Fatalf("load bad fixture: %v", err)
	}
	if rep := badLoad.Report; rep.Errors != 1 {
		t.Fatalf("expected errors=1 after bad load, got %+v", rep)
	}

	goodDir := t.TempDir()
	goodPath := filepath.Join(goodDir, "issues.jsonl")
	good := `{"id":"bd-ok","title":"valid","status":"open","issue_type":"task"}` + "\n"
	if err := os.WriteFile(goodPath, []byte(good), 0o644); err != nil {
		t.Fatalf("write good fixture: %v", err)
	}
	goodLoad, err := loadAndValidateJSONL(DataSource{Type: SourceTypeJSONLLocal, Path: goodPath, Priority: PriorityJSONLLocal})
	if err != nil {
		t.Fatalf("load good fixture: %v", err)
	}
	rep := &goodLoad.Report
	if rep.Errors != 0 || rep.Valid != 1 || len(rep.Warnings) != 0 {
		t.Errorf("clean load should overwrite the previous report, got %+v", rep)
	}
	if rep.Path != goodPath {
		t.Errorf("report path = %q, want %q", rep.Path, goodPath)
	}
	if badLoad.Report.Errors != 1 || badLoad.Report.Path != badPath {
		t.Fatalf("later load changed the earlier snapshot accounting: %+v", badLoad.Report)
	}
}
