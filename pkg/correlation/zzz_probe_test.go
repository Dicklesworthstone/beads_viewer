package correlation

import (
	"testing"

	json "github.com/goccy/go-json"
)

// Verify marshaling a *historyArtifact (pointer, as stored in cache entry) routes
// through the value-receiver MarshalJSON, and full cache file round-trips.
func TestProbeCacheEntryRoundTrip(t *testing.T) {
	art := &historyArtifact{
		Events:  []BeadEvent{{BeadID: "bv-9", EventType: EventCreated}},
		Commits: []CorrelatedCommit{{SHA: "aaa", BeadID: "bv-9"}, {SHA: "bbb", BeadID: ""}},
	}
	// As the size-cap preflight does: json.Marshal(art) where art is *historyArtifact
	pre, err := json.Marshal(art)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("preflight bytes: %s", pre)
	if !contains(string(pre), "commit_bead_ids") {
		t.Errorf("preflight marshal did NOT route through custom codec (no commit_bead_ids): %s", pre)
	}

	// Now the full cache file with pointer entry
	cf := headArtifactCacheFile{Version: 1, Entries: map[string]headArtifactCacheEntry{
		"k": {HeadSHA: "h", OptsHash: "o", Artifact: art},
	}}
	data, err := json.Marshal(cf)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), "commit_bead_ids") {
		t.Errorf("cache-file marshal did NOT route through custom codec: %s", data)
	}
	var back headArtifactCacheFile
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	e := back.Entries["k"]
	if e.Artifact == nil {
		t.Fatal("artifact nil after roundtrip")
	}
	if e.Artifact.Commits[0].BeadID != "bv-9" || e.Artifact.Commits[1].BeadID != "" {
		t.Errorf("BeadID lost: %+v", e.Artifact.Commits)
	}
}

// Nil Commits / nil Events.
func TestProbeNilSlices(t *testing.T) {
	art := &historyArtifact{}
	data, err := json.Marshal(art)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nil-slice marshal: %s", data)
	var back historyArtifact
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Commits != nil {
		t.Logf("Commits non-nil after roundtrip: %#v", back.Commits)
	}
}

// Length mismatch / corrupt cache: more Commits than CommitBeadIDs (shorter)
// must fail closed rather than silently dropping commit-to-bead linkage.
func TestProbeShorterBeadIDs(t *testing.T) {
	raw := `{"events":null,"commits":[{"sha":"a"},{"sha":"b"},{"sha":"c"}],"commit_bead_ids":["x"]}`
	var back historyArtifact
	if err := json.Unmarshal([]byte(raw), &back); err == nil || !contains(err.Error(), "commit_bead_ids length 1 does not match commits length 3") {
		t.Fatalf("mismatched artifact error=%v, want strict length rejection", err)
	}
}

// Longer CommitBeadIDs than commits is likewise corrupt and must fail closed.
func TestProbeLongerBeadIDs(t *testing.T) {
	raw := `{"commits":[{"sha":"a"}],"commit_bead_ids":["x","y","z"]}`
	var back historyArtifact
	if err := json.Unmarshal([]byte(raw), &back); err == nil || !contains(err.Error(), "commit_bead_ids length 3 does not match commits length 1") {
		t.Fatalf("mismatched artifact error=%v, want strict length rejection", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
