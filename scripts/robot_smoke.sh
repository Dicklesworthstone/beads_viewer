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
command -v jq >/dev/null || { echo "robot_smoke: jq is required" >&2; exit 2; }

# name|args|json?(1/0)  -- args are word-split on purpose.
commands=(
  "triage|--robot-triage|1"
  "next|--robot-next|1"
  "plan|--robot-plan|1"
  "insights|--robot-insights|1"
  "priority|--robot-priority|1"
  "alerts|--robot-alerts|1"
  "suggest|--robot-suggest|1"
  "recipes|--robot-recipes|1"
  "label-health|--robot-label-health|1"
  "label-flow|--robot-label-flow|1"
  "label-attention|--robot-label-attention|1"
  "graph-json|--robot-graph|1"
  "graph-dot|--robot-graph --graph-format=dot|1"
  "graph-mermaid|--robot-graph --graph-format=mermaid|1"
  "forecast|--robot-forecast all|1"
  "capacity|--robot-capacity|1"
  "sprint-list|--robot-sprint-list|1"
  "history|--robot-history|1"
  "orphans|--robot-orphans|1"
  "file-hotspots|--robot-file-hotspots|1"
  "correlation-stats|--robot-correlation-stats|1"
  "capabilities|--robot-capabilities|1"
  "schema|--robot-schema|1"
  "triage-toon|--robot-triage --format toon|0"
  "triage-by-track|--robot-triage --robot-triage-by-track|1"
  "triage-by-label|--robot-triage --robot-triage-by-label|1"
  "recipe-plan|--recipe actionable --robot-plan|1"
  "search|--search issue --robot-search|1"
  "help|--robot-help|0"
)

failures=0
timing_rows=()
run_suite() {
  local dir="$1" label="$2"
  echo "== robot smoke: $label ($dir)"
  for entry in "${commands[@]}"; do
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
    printf '  %-18s %-52s %6dms %s\n' "$name" "$args" "$ms" "$verdict"
    if [ "$label" = "target" ]; then
      timing_rows+=("{\"command\":\"$args\",\"ms\":$ms,\"bytes\":${#out},\"ok\":$([ "$verdict" = ok ] && echo true || echo false)}")
    fi
    if [ "$verdict" != "ok" ]; then
      failures=$((failures + 1))
      sed 's/^/      stderr: /' "$tmp/stderr" | head -5
    fi
  done
}

run_suite "$target" "target"

if [ "${ROBOT_SMOKE_SYNTHETIC:-1}" != "0" ]; then
  fixture="$tmp/fixture"
  mkdir -p "$fixture/.beads"
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  cat >"$fixture/.beads/issues.jsonl" <<EOF
{"id":"SMOKE-1","title":"Root task","status":"open","priority":1,"issue_type":"task","labels":["core"],"created_at":"$now","updated_at":"$now"}
{"id":"SMOKE-2","title":"Blocked by root","status":"open","priority":2,"issue_type":"task","labels":["core"],"created_at":"$now","updated_at":"$now","dependencies":[{"issue_id":"SMOKE-2","depends_on_id":"SMOKE-1","type":"blocks"}]}
{"id":"SMOKE-3","title":"Also blocked by root","status":"in_progress","priority":2,"issue_type":"bug","labels":["ui"],"assignee":"smoke","created_at":"$now","updated_at":"$now","dependencies":[{"issue_id":"SMOKE-3","depends_on_id":"SMOKE-1","type":"blocks"}]}
{"id":"SMOKE-4","title":"Closed one","status":"closed","priority":3,"issue_type":"task","created_at":"$now","updated_at":"$now","closed_at":"$now"}
EOF
  (cd "$fixture" && git init -q && git add -A && git -c user.name=smoke -c user.email=smoke@example.com commit -qm "smoke fixture")
  run_suite "$fixture" "synthetic fixture"
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
