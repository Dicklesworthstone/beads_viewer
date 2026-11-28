#!/usr/bin/env bash
# robot_smoke.sh: build bv and run every documented robot command against a
# repository, failing on a non-zero exit, empty output, or (for JSON commands)
# output that does not parse. Used by scripts/release_gate.sh; runnable alone.
#
# Usage: scripts/robot_smoke.sh [repo-dir]   (default: this repository)
#        ROBOT_SMOKE_BV=/path/to/bv skips the build.
#        ROBOT_SMOKE_SYNTHETIC=0 skips the synthetic-fixture pass.
#        ROBOT_SMOKE_TIMING_JSON=path writes per-command wall-clock ms (target
#        repository pass only) as JSON, e.g. tests/artifacts/perf/robot_wall.json.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
target="${1:-$root}"
export BV_NO_BROWSER=1 BV_TEST_MODE=1 BV_NO_SAVED_CONFIG=1 BV_NO_UPDATE_CHECK=1

tmp="$(mktemp -d "${TMPDIR:-/tmp}/bv-robot-smoke.XXXXXX")"
trap 'printf "robot_smoke: retained artifacts at %s\n" "$tmp"' EXIT

bv="${ROBOT_SMOKE_BV:-}"
if [ -z "$bv" ]; then
  bv="$tmp/bv"
  (cd "$root" && go build -o "$bv" ./cmd/bv)
fi
command -v jq >/dev/null || { echo "INCOMPLETE: robot_smoke: jq is required" >&2; exit 2; }

# Non-mutating commands executed against both the target repository and the
# synthetic fixture. Format: command_name|args|json?(1/0)
base_commands=(
  "robot-triage|--robot-triage|1"
  "robot-next|--robot-next|1"
  "robot-plan|--robot-plan|1"
  "robot-insights|--robot-insights|1"
  "robot-priority|--robot-priority|1"
  "robot-alerts|--robot-alerts|1"
  "robot-suggest|--robot-suggest|1"
  "robot-recipes|--robot-recipes|1"
  "robot-label-health|--robot-label-health|1"
  "robot-label-flow|--robot-label-flow|1"
  "robot-label-attention|--robot-label-attention|1"
  "robot-graph|--robot-graph|1"
  "robot-graph|--robot-graph --graph-format=dot|1"
  "robot-graph|--robot-graph --graph-format=mermaid|1"
  "robot-forecast|--robot-forecast all|1"
  "robot-capacity|--robot-capacity|1"
  "robot-sprint-list|--robot-sprint-list|1"
  "robot-history|--robot-history|1"
  "robot-orphans|--robot-orphans|1"
  "robot-file-hotspots|--robot-file-hotspots|1"
  "robot-correlation-stats|--robot-correlation-stats|1"
  "robot-capabilities|--robot-capabilities|1"
  "robot-schema|--robot-schema|1"
  "robot-docs|--robot-docs guide|1"
  "robot-metrics|--robot-metrics|1"
  "robot-triage|--robot-triage --format toon|0"
  "robot-triage-by-track|--robot-triage --robot-triage-by-track|1"
  "robot-triage-by-label|--robot-triage --robot-triage-by-label|1"
  "robot-plan|--recipe actionable --robot-plan|1"
  "robot-search|--search issue --robot-search|1"
  "robot-help|--robot-help|0"
)

failures=0
timing_rows=()
covered_cmds_file="$tmp/covered_commands.txt"
: > "$covered_cmds_file"

run_entries() {
  local dir="$1" label="$2"; shift 2
  local entries=("$@")
  for entry in "${entries[@]}"; do
    IFS='|' read -r name args wantjson <<<"$entry"
    # shellcheck disable=SC2206
    local argv=($args)
    local start end ms out rc
    start=$(date +%s%N)
    set +e
    out=$(cd "$dir" && timeout 180 "$bv" "${argv[@]}" 2>"$tmp/stderr")
    rc=$?
    set -e
    end=$(date +%s%N); ms=$(( (end - start) / 1000000 ))
    local verdict="ok"
    if [ "$rc" -ne 0 ]; then
      verdict="EXIT $rc"
    elif [ -z "$out" ]; then
      verdict="EMPTY OUTPUT"
    elif [ "$wantjson" = "1" ] && ! printf '%s' "$out" | jq -e . >/dev/null 2>&1; then
      verdict="NOT JSON"
    fi
    printf '  %-28s %-52s %6dms %s\n' "$name" "$args" "$ms" "$verdict"
    if [ "$label" = "target" ]; then
      timing_rows+=("{\"command\":\"$args\",\"ms\":$ms,\"bytes\":${#out},\"ok\":$([ "$verdict" = ok ] && echo true || echo false)}")
    fi
    if [ "$verdict" != "ok" ]; then
      failures=$((failures + 1))
      sed 's/^/      stderr: /' "$tmp/stderr" | head -5
    else
      printf '%s\n' "$name" >> "$covered_cmds_file"
    fi
  done
}

echo "== robot smoke: target ($target)"
run_entries "$target" "target" "${base_commands[@]}"

if [ "${ROBOT_SMOKE_SYNTHETIC:-1}" != "0" ]; then
  fixture="$tmp/fixture"
  mkdir -p "$fixture/.beads"
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  start_date=$(date -u -d '7 days ago' +%Y-%m-%dT00:00:00Z 2>/dev/null || date -u -v-7d +%Y-%m-%dT00:00:00Z)
  end_date=$(date -u -d '7 days' +%Y-%m-%dT00:00:00Z 2>/dev/null || date -u -v+7d +%Y-%m-%dT00:00:00Z)

  cat >"$fixture/README.md" <<'EOF'
# Smoke Fixture
Initial repository content.
EOF
  cat >"$fixture/.beads/issues.jsonl" <<EOF
{"id":"SMOKE-1","title":"Root task","status":"open","priority":1,"issue_type":"task","labels":["core"],"created_at":"$now","updated_at":"$now"}
{"id":"SMOKE-2","title":"Blocked by root","status":"open","priority":2,"issue_type":"task","labels":["core"],"created_at":"$now","updated_at":"$now","dependencies":[{"issue_id":"SMOKE-2","depends_on_id":"SMOKE-1","type":"blocks"}]}
{"id":"SMOKE-3","title":"Also blocked by root","status":"in_progress","priority":2,"issue_type":"bug","labels":["ui"],"assignee":"smoke","created_at":"$now","updated_at":"$now","dependencies":[{"issue_id":"SMOKE-3","depends_on_id":"SMOKE-1","type":"blocks"}]}
{"id":"SMOKE-4","title":"Closed one","status":"closed","priority":3,"issue_type":"task","created_at":"$now","updated_at":"$now","closed_at":"$now"}
EOF
  cat >"$fixture/.beads/sprints.jsonl" <<EOF
{"id":"sprint-smoke","name":"Smoke Sprint","start_date":"$start_date","end_date":"$end_date","bead_ids":["SMOKE-1","SMOKE-2","SMOKE-3","SMOKE-4"]}
EOF
  (
    cd "$fixture"
    git init -q
    git config user.name smoke
    git config user.email smoke@example.com
    git add -A
    git commit -qm "seed smoke fixture"
    printf '\nUpdated for SMOKE-1.\n' >> README.md
    git add README.md
    git commit -qm "implement SMOKE-1 root task"
  )
  head_sha=$(git -C "$fixture" rev-parse HEAD)
  (cd "$fixture" && "$bv" --save-baseline "smoke baseline" >/dev/null 2>"$tmp/stderr") || {
    echo "robot_smoke: failed to save synthetic baseline" >&2
    cat "$tmp/stderr" >&2
    failures=$((failures + 1))
  }

  fixture_extra_commands=(
    "bead-history|--bead-history SMOKE-1|1"
    "robot-diff|--robot-diff --diff-since HEAD~1|1"
    "robot-commit|--robot-commit HEAD|1"
    "robot-drift|--check-drift --robot-drift|1"
    "robot-cass|--robot-cass SMOKE-1|1"
    "robot-blocker-chain|--robot-blocker-chain SMOKE-2|1"
    "robot-whatif|--robot-whatif SMOKE-1|1"
    "robot-check-dep|--robot-check-dep SMOKE-2:SMOKE-1|1"
    "robot-check-dep|--robot-check-dep remove:SMOKE-2:SMOKE-1|1"
    "robot-burndown|--robot-burndown current|1"
    "robot-sprint-show|--robot-sprint-show sprint-smoke|1"
    "robot-related|--robot-related SMOKE-1|1"
    "robot-impact-network|--robot-impact-network SMOKE-1|1"
    "robot-impact|--robot-impact README.md|1"
    "robot-file-beads|--robot-file-beads README.md|1"
    "robot-file-relations|--robot-file-relations README.md|1"
    "robot-causality|--robot-causality SMOKE-1|1"
    "robot-explain-correlation|--robot-explain-correlation ${head_sha}:SMOKE-1|1"
    "robot-confirm-correlation|--robot-confirm-correlation ${head_sha}:SMOKE-1 --correlation-by smoke|1"
    "robot-reject-correlation|--robot-reject-correlation ${head_sha}:SMOKE-1 --correlation-by smoke --correlation-reason smoke-test|1"
    "robot-ignore-correlation|--robot-ignore-correlation ${head_sha}:SMOKE-1 --correlation-by smoke --correlation-reason smoke-test|1"
  )

  echo "== robot smoke: synthetic fixture ($fixture)"
  run_entries "$fixture" "synthetic fixture" "${base_commands[@]}" "${fixture_extra_commands[@]}"

  # Self-enforcing completeness check: every registered command in
  # --robot-capabilities must be covered by the smoke suite.
  expected_cmds="$tmp/expected_commands.txt"
  (cd "$fixture" && "$bv" --robot-capabilities) | jq -r '.commands[].name' | sort -u > "$expected_cmds"
  actual_cmds="$tmp/actual_commands.txt"
  sort -u "$covered_cmds_file" > "$actual_cmds"
  missing=$(comm -23 "$expected_cmds" "$actual_cmds")
  expected_count=$(wc -l < "$expected_cmds" | tr -d ' ')
  actual_count=$(comm -12 "$expected_cmds" "$actual_cmds" | wc -l | tr -d ' ')
  echo "robot_smoke: command coverage ${actual_count}/${expected_count}"
  if [ -n "$missing" ]; then
    echo "robot_smoke: MISSING registered robot commands:" >&2
    printf '  %s\n' "$missing" >&2
    failures=$((failures + 1))
  fi
fi

if [ -n "${ROBOT_SMOKE_TIMING_JSON:-}" ]; then
  {
    printf '{\n  "generated_at": "%s",\n  "target": "%s",\n  "go": "%s",\n  "commands": [\n' \
      "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$target" "$(go version | cut -d' ' -f3)"
    first=1
    for row in "${timing_rows[@]}"; do
      if [ "$first" -eq 1 ]; then first=0; else printf ',\n'; fi
      printf '    %s' "$row"
    done
    printf '\n  ]\n}\n'
  } > "$ROBOT_SMOKE_TIMING_JSON"
  echo "robot_smoke: wall-clock timings written to $ROBOT_SMOKE_TIMING_JSON"
fi

echo "robot_smoke: $failures failure(s)"
[ "$failures" -eq 0 ]
