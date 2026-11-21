#!/bin/bash
# scripts/verify_isomorphic.sh
#
# Isomorphic Verification Script
# ==============================
# Verifies that optimized implementations produce identical outputs to the baseline.
#
# This script:
# 1. Builds the baseline version (from a reference branch)
# 2. Builds the current version
# 3. Runs both against test fixtures
# 4. Compares outputs and reports any differences
#
# Usage:
#   ./scripts/verify_isomorphic.sh [baseline_ref]
#
# Arguments:
#   baseline_ref  Git ref to compare against (default: main)
#
# Environment Variables:
#   VERBOSE=1     Show detailed output
#   KEEP_TEMPS=1  Don't delete temporary files on exit
#   SKIP_BUILD=1  Skip building (use existing binaries)

set -euo pipefail
export BV_NO_BROWSER=1 BV_TEST_MODE=1 BV_NO_SAVED_CONFIG=1 BV_NO_UPDATE_CHECK=1
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-1788220800}"

# Configuration
BASELINE_REF="${1:-main}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
VERBOSE="${VERBOSE:-0}"
KEEP_TEMPS="${KEEP_TEMPS:-0}"
SKIP_BUILD="${SKIP_BUILD:-0}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[PASS]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[FAIL]${NC} $*"; }
log_verbose() { [[ "$VERBOSE" == "1" ]] && echo -e "${BLUE}[DEBUG]${NC} $*" || true; }

# Cleanup function: removes the temp dir and the detached baseline worktree.
# The caller's worktree, index, and stash are never touched by this script.
# shellcheck disable=SC2329 # invoked indirectly via trap
cleanup() {
    local exit_code=$?
    if [[ -n "${BASELINE_WORKTREE:-}" ]] && [[ -d "$BASELINE_WORKTREE" ]]; then
        log_verbose "Removing baseline worktree $BASELINE_WORKTREE"
        git -C "$PROJECT_ROOT" worktree remove --force "$BASELINE_WORKTREE" >/dev/null 2>&1 || rm -rf "$BASELINE_WORKTREE"
        git -C "$PROJECT_ROOT" worktree prune >/dev/null 2>&1 || true
    fi
    if [[ "$KEEP_TEMPS" != "1" ]] && [[ -n "${TEST_DIR:-}" ]]; then
        log_verbose "Cleaning up $TEST_DIR"
        rm -rf "$TEST_DIR"
    fi
    exit "$exit_code"
}
trap cleanup EXIT INT TERM

# Validate the baseline ref before anything else runs. --end-of-options stops
# an option-looking argument (e.g. "--output=x") from being parsed as a flag.
if ! BASELINE_SHA=$(git -C "$PROJECT_ROOT" rev-parse --verify --quiet --end-of-options "${BASELINE_REF}^{commit}"); then
    log_error "Baseline ref '$BASELINE_REF' does not resolve to a commit"
    exit 2
fi

# Create temporary directory
TEST_DIR=$(mktemp -d)
log_verbose "Using temporary directory: $TEST_DIR"

# Binary paths
BASELINE_BIN="$TEST_DIR/bv_baseline"
CURRENT_BIN="$TEST_DIR/bv_current"
BASELINE_WORKTREE=""

# Current tree description (informational only; never modified)
ORIGINAL_BRANCH=$(git -C "$PROJECT_ROOT" branch --show-current || git -C "$PROJECT_ROOT" rev-parse HEAD)

echo "=============================================="
echo "  Isomorphic Verification"
echo "=============================================="
echo ""
echo "  Baseline:  $BASELINE_REF"
echo "  Current:   $ORIGINAL_BRANCH"
echo "  Test Dir:  $TEST_DIR"
echo ""

# Build baseline version
build_baseline() {
    log_info "Building baseline version from $BASELINE_REF ($BASELINE_SHA)..."

    # Build in a detached temporary worktree so the caller's checkout, index,
    # and stash are never modified, and an interrupted run leaves nothing to
    # restore (the EXIT trap removes the worktree).
    BASELINE_WORKTREE="$TEST_DIR/baseline_worktree"
    log_verbose "Adding detached worktree at $BASELINE_WORKTREE..."
    git -C "$PROJECT_ROOT" worktree add --detach -q "$BASELINE_WORKTREE" "$BASELINE_SHA"

    log_verbose "Building baseline binary..."
    (cd "$BASELINE_WORKTREE" && go build -o "$BASELINE_BIN" ./cmd/bv)

    log_success "Baseline built: $BASELINE_BIN"
}

# Build current version
build_current() {
    log_info "Building current version..."
    (cd "$PROJECT_ROOT" && go build -o "$CURRENT_BIN" ./cmd/bv)
    log_success "Current built: $CURRENT_BIN"
}

# Copy test fixtures
setup_fixtures() {
    log_info "Setting up test fixtures..."

    FIXTURE_DIR="$TEST_DIR/fixtures"
    mkdir -p "$FIXTURE_DIR/cases"

    # Create a deterministic synthetic fixture with dependencies and mixed statuses
    SYNTH_CASE="$FIXTURE_DIR/cases/synthetic"
    mkdir -p "$SYNTH_CASE/.beads"
    cat > "$SYNTH_CASE/.beads/issues.jsonl" <<'EOF'
{"id":"ISO-1","title":"Root task","status":"open","priority":1,"issue_type":"task","labels":["core"],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}
{"id":"ISO-2","title":"Blocked child","status":"open","priority":2,"issue_type":"task","labels":["core"],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","dependencies":[{"issue_id":"ISO-2","depends_on_id":"ISO-1","type":"blocks"}]}
{"id":"ISO-3","title":"Closed task","status":"closed","priority":3,"issue_type":"task","labels":["ui"],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","closed_at":"2026-01-02T00:00:00Z"}
EOF

    # Create a default test case from current project if .beads exists
    if [[ -d "$PROJECT_ROOT/.beads" ]]; then
        DEFAULT_CASE="$FIXTURE_DIR/cases/project"
        mkdir -p "$DEFAULT_CASE/.beads"
        for jsonl in issues.jsonl beads.jsonl; do
            cp "$PROJECT_ROOT/.beads/$jsonl" "$DEFAULT_CASE/.beads/" 2>/dev/null || true
        done
        log_verbose "Using project .beads as test case"
    fi

    log_success "Test fixtures ready"
}

# Normalize volatile per-run JSON fields (timestamps, binary version strings)
normalize_json_output() {
    local raw_file="$1"
    local norm_file="$2"
    if command -v jq >/dev/null 2>&1 && jq -e . "$raw_file" >/dev/null 2>&1; then
        jq -S '
          def scrub:
            if type == "object" then
              del(.generated_at, .version, .analysis_time_ms, .compute_time_ms, .ms)
              | with_entries(.value |= scrub)
            elif type == "array" then
              map(scrub)
            else
              .
            end;
          scrub
        ' "$raw_file" > "$norm_file"
    else
        cp "$raw_file" "$norm_file"
    fi
}

# Run a command and capture output (fails closed on non-zero exit)
run_command() {
    local binary="$1"
    local case_dir="$2"
    local command="$3"
    local output_file="$4"
    local raw_file="${output_file}.raw"
    local err_file="${output_file}.err"
    local rc=0
    local -a argv=()
    # shellcheck disable=SC2206
    argv=($command)

    (cd "$case_dir" && env -u BEADS_DIR -u BEADS_DB -u BD_DB -u BEADS_JSONL "$binary" "${argv[@]}" > "$raw_file" 2> "$err_file") || rc=$?
    if [[ "$rc" -ne 0 ]]; then
        log_error "$case_dir ($binary $command) exited with status $rc"
        if [[ -s "$err_file" ]]; then
            sed 's/^/  stderr: /' "$err_file" | head -10
        fi
        return "$rc"
    fi
    normalize_json_output "$raw_file" "$output_file"
}

# Compare outputs
compare_outputs() {
    local baseline_out="$1"
    local current_out="$2"
    local test_name="$3"

    if diff -q "$baseline_out" "$current_out" > /dev/null 2>&1; then
        log_success "$test_name: outputs identical"
        return 0
    else
        log_error "$test_name: OUTPUTS DIFFER"
        if [[ "$VERBOSE" == "1" ]]; then
            echo "--- Diff (first 50 lines) ---"
            diff "$baseline_out" "$current_out" | head -50 || true
            echo "---"
        fi
        return 1
    fi
}

# Run tests for a single command
test_command() {
    local cmd="$1"
    local cmd_name="${cmd// /_}"
    local failures=0

    log_info "Testing: $cmd"

    for case_dir in "$FIXTURE_DIR/cases"/*; do
        [[ -d "$case_dir" ]] || continue
        [[ -d "$case_dir/.beads" ]] || continue

        case_name=$(basename "$case_dir")
        baseline_out="$TEST_DIR/output/${case_name}_${cmd_name}_baseline.json"
        current_out="$TEST_DIR/output/${case_name}_${cmd_name}_current.json"

        mkdir -p "$TEST_DIR/output"

        # Run both versions (non-zero exit counts as failure)
        if ! run_command "$BASELINE_BIN" "$case_dir" "$cmd" "$baseline_out"; then
            ((failures++))
            continue
        fi
        if ! run_command "$CURRENT_BIN" "$case_dir" "$cmd" "$current_out"; then
            ((failures++))
            continue
        fi

        # Compare
        if ! compare_outputs "$baseline_out" "$current_out" "$case_name/$cmd"; then
            ((failures++))
        fi
    done

    return $failures
}

# Main test suite
run_tests() {
    local total_failures=0

    # Robot commands to test
    COMMANDS=(
        "--robot-triage"
        "--robot-insights"
        "--robot-plan"
        "--robot-next"
        "--robot-alerts"
    )

    for cmd in "${COMMANDS[@]}"; do
        if ! test_command "$cmd"; then
            ((total_failures++))
        fi
    done

    return $total_failures
}

# Main execution
main() {
    # Build phase
    if [[ "$SKIP_BUILD" != "1" ]]; then
        build_baseline
        build_current
    else
        log_warn "Skipping build (SKIP_BUILD=1)"
        if [[ ! -f "$BASELINE_BIN" ]] || [[ ! -f "$CURRENT_BIN" ]]; then
            log_error "Binaries not found. Run without SKIP_BUILD=1 first."
            exit 1
        fi
    fi

    # Setup phase
    setup_fixtures

    # Test phase
    echo ""
    echo "=============================================="
    echo "  Running Tests"
    echo "=============================================="
    echo ""

    if run_tests; then
        echo ""
        log_success "ALL TESTS PASSED - Implementations are isomorphic"
        exit 0
    else
        echo ""
        log_error "SOME TESTS FAILED - Implementations differ"
        echo ""
        echo "To investigate:"
        echo "  1. Set VERBOSE=1 to see diffs"
        echo "  2. Set KEEP_TEMPS=1 to preserve output files"
        echo "  3. Output files are in: $TEST_DIR/output/"
        exit 1
    fi
}

main "$@"
