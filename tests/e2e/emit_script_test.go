package main_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestEmitScript_BashAndFish(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// JSONL alone supplies recommendations, but cannot prove a live tracker route.
	writeBeads(t, env, `{"id":"A","title":"Unblocker","status":"open","priority":1,"issue_type":"task"}
{"id":"B","title":"Blocked","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"B","depends_on_id":"A","type":"blocks"}]}`)

	tests := []struct {
		name        string
		formatFlag  string
		wantShebang string
		wantExtra   string
	}{
		{name: "bash", formatFlag: "bash", wantShebang: "#!/usr/bin/env bash", wantExtra: "set -euo pipefail"},
		{name: "fish", formatFlag: "fish", wantShebang: "#!/usr/bin/env fish", wantExtra: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(bv, "--emit-script", "--script-limit=1", "--script-format="+tt.formatFlag)
			cmd.Dir = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("run failed: %v\n%s", err, out)
			}
			s := string(out)
			if !strings.Contains(s, tt.wantShebang) {
				t.Fatalf("missing shebang %q:\n%s", tt.wantShebang, s)
			}
			if tt.wantExtra != "" && !strings.Contains(s, tt.wantExtra) {
				t.Fatalf("missing %q:\n%s", tt.wantExtra, s)
			}
			if !strings.Contains(s, "# 1. A: Unblocker (score:") || !strings.Contains(s, "# No verified live tracker route") {
				t.Fatalf("unbound script lost the useful issue identity or route diagnostic:\n%s", s)
			}
			for _, line := range strings.Split(s, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") && line != tt.wantExtra {
					t.Fatalf("unbound script contains an executable action %q:\n%s", line, s)
				}
			}
			if !strings.Contains(s, "# Data hash:") {
				t.Fatalf("missing data hash header:\n%s", s)
			}
		})
	}
}
