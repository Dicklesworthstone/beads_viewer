package loader

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestLoadSprintsMissingFileIsOK(t *testing.T) {
	tmp := t.TempDir()
	// RCH may put TMPDIR beneath a real repository. This fixture explicitly
	// has no ancestor tracker; do not discover the checkout's sprint file.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(tmp))
	t.Setenv("BEADS_DB", "")
	t.Setenv("BEADS_DIR", "")
	got, err := LoadSprints(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no sprints, got %d", len(got))
	}
}

func TestParseSprints(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	later := now.AddDate(0, 0, 14)

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "empty input",
			input: "",
			want:  0,
		},
		{
			name:  "valid sprint",
			input: `{"id":"sprint-1","name":"Sprint 1","start_date":"` + now.Format(time.RFC3339) + `","end_date":"` + later.Format(time.RFC3339) + `","bead_ids":["bv-1","bv-2"],"velocity_target":10}`,
			want:  1,
		},
		{
			name:  "missing id (skipped)",
			input: `{"name":"Sprint 1","start_date":"2025-01-01T00:00:00Z","end_date":"2025-01-14T00:00:00Z"}`,
			want:  0,
		},
		{
			name: "skip malformed json",
			input: `{"id":"sprint-1","name":"Sprint 1","start_date":"2025-01-01T00:00:00Z","end_date":"2025-01-14T00:00:00Z"}
{invalid json}
{"id":"sprint-2","name":"Sprint 2","start_date":"2025-01-15T00:00:00Z","end_date":"2025-01-28T00:00:00Z"}`,
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSprints(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("ParseSprints() error = %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("ParseSprints() got %d sprints, want %d", len(got), tt.want)
			}
		})
	}
}

func TestSaveAndLoadSprintsRoundTrip(t *testing.T) {
	tmp := t.TempDir()

	start := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 12, 14, 0, 0, 0, 0, time.UTC)
	in := []model.Sprint{
		{
			ID:             "sprint-1",
			Name:           "Sprint 1",
			StartDate:      start,
			EndDate:        end,
			BeadIDs:        []string{"bv-1", "bv-2"},
			VelocityTarget: 12.5,
		},
	}

	if err := SaveSprints(tmp, in); err != nil {
		t.Fatalf("SaveSprints: %v", err)
	}

	out, err := LoadSprints(tmp)
	if err != nil {
		t.Fatalf("LoadSprints: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 sprint, got %d", len(out))
	}
	if out[0].ID != in[0].ID || out[0].Name != in[0].Name {
		t.Fatalf("mismatch: got %+v want %+v", out[0], in[0])
	}
	if !out[0].StartDate.Equal(start) || !out[0].EndDate.Equal(end) {
		t.Fatalf("dates mismatch: got %v-%v want %v-%v", out[0].StartDate, out[0].EndDate, start, end)
	}
	if len(out[0].BeadIDs) != 2 || out[0].BeadIDs[0] != "bv-1" || out[0].BeadIDs[1] != "bv-2" {
		t.Fatalf("bead IDs mismatch: %+v", out[0].BeadIDs)
	}
}
