#!/bin/bash
# capture_baseline.sh - Capture comprehensive performance baseline for bv
#
# This script measures performance BEFORE implementing background worker changes.
# Run this to establish the baseline against which improvements are measured.
#
# Usage:
#   ./scripts/capture_baseline.sh              # Capture new baseline
#   ./scripts/capture_baseline.sh --compare    # Compare against saved baseline
#   ./scripts/capture_baseline.sh --generate   # Generate test datasets first
#
# Output:
#   benchmarks/baseline_YYYYMMDD_HHMMSS.txt    # Full baseline data
#   benchmarks/baseline_latest.txt             # Symlink to latest baseline

set -e
export BV_NO_BROWSER=1 BV_TEST_MODE=1 BV_NO_SAVED_CONFIG=1 BV_NO_UPDATE_CHECK=1

# Configuration
BENCHMARK_DIR="benchmarks"
TESTDATA_DIR="tests/testdata/benchmark"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
BASELINE_FILE="$BENCHMARK_DIR/baseline_${TIMESTAMP}.txt"
LATEST_LINK="$BENCHMARK_DIR/baseline_latest.txt"

# Packages to benchmark
BENCH_PACKAGES=(
    ./pkg/loader/...
    ./pkg/analysis/...
    ./pkg/ui/...
    ./pkg/export/...
    ./pkg/search/...
    ./pkg/cass/...
)

# Ensure benchmark directory exists
mkdir -p "$BENCHMARK_DIR"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

print_header() {
    echo -e "\n${BLUE}=== $1 ===${NC}\n"
}

print_section() {
    echo ""
    echo "=== $1 ===" >> "$BASELINE_FILE"
    echo "" >> "$BASELINE_FILE"
}

generate_testdata() {
    print_header "Generating Test Datasets"

    if [ ! -f "scripts/generate_testdata.go" ]; then
        echo -e "${RED}Error: scripts/generate_testdata.go not found${NC}"
        exit 1
    fi

    go run scripts/generate_testdata.go

    echo -e "${GREEN}Test datasets generated in $TESTDATA_DIR${NC}"
}

check_testdata() {
    if [ ! -d "$TESTDATA_DIR" ]; then
        echo -e "${YELLOW}Warning: Test datasets not found in $TESTDATA_DIR${NC}"
        echo "Run with --generate flag first, or datasets will be created now."
        generate_testdata
    fi
}

capture_baseline() {
    print_header "Capturing Performance Baseline"

    # Check for test data
    check_testdata

    # Header
    {
        echo "=== BV Performance Baseline ==="
        echo "Date: $(date)"
        echo "Git commit: $(git rev-parse HEAD 2>/dev/null || echo 'not in git repo')"
        echo "Git branch: $(git branch --show-current 2>/dev/null || echo 'unknown')"
        echo "Go version: $(go version)"
        echo "CPU: $(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2 || sysctl -n machdep.cpu.brand_string 2>/dev/null || echo 'unknown')"
        echo "Memory: $(free -h 2>/dev/null | awk '/^Mem:/{print $2}' || sysctl -n hw.memsize 2>/dev/null | awk '{printf "%.0f GB", $1/1024/1024/1024}' || echo 'unknown')"
        echo ""
    } > "$BASELINE_FILE"

    # 1. Loader Benchmarks
    print_section "Loader Benchmarks"
    echo "Running loader benchmarks..."
    go test -bench=. -benchmem -count=3 ./pkg/loader/... >> "$BASELINE_FILE" 2>&1 || true

    # 2. Analysis Benchmarks (core graph algorithms)
    print_section "Analysis Benchmarks"
    echo "Running analysis benchmarks..."
    go test -bench=. -benchmem -count=3 ./pkg/analysis/... >> "$BASELINE_FILE" 2>&1 || true

    # 3. UI Benchmarks
    print_section "UI Benchmarks"
    echo "Running UI benchmarks..."
    go test -bench=. -benchmem -count=3 ./pkg/ui/... >> "$BASELINE_FILE" 2>&1 || true

    # 4. Export Benchmarks
    print_section "Export Benchmarks"
    echo "Running export benchmarks..."
    go test -bench=. -benchmem -count=3 ./pkg/export/... >> "$BASELINE_FILE" 2>&1 || true

    # 5. Search Benchmarks
    print_section "Search Benchmarks"
    echo "Running search benchmarks..."
    go test -bench=. -benchmem -count=3 ./pkg/search/... >> "$BASELINE_FILE" 2>&1 || true

    # 6. Robot Mode Timing (end-to-end)
    print_section "Robot Mode Timing (End-to-End)"
    echo "Running robot-triage timing..."

    tmp_bench_root=$(mktemp -d "${TMPDIR:-/tmp}/bv-baseline-ds.XXXXXX")
    trap 'rm -rf "$tmp_bench_root"' EXIT
    bv_bin="$tmp_bench_root/bv"
    go build -o "$bv_bin" ./cmd/bv

    for dataset in small medium large; do
        datafile="$TESTDATA_DIR/$dataset.jsonl"
        if [ -f "$datafile" ]; then
            echo "Dataset: $dataset" >> "$BASELINE_FILE"
            ds_dir="$tmp_bench_root/$dataset/.beads"
            mkdir -p "$ds_dir"
            cp "$datafile" "$ds_dir/issues.jsonl"
            # Run 3 times and capture timing without masking non-zero exits
            for _ in 1 2 3; do
                local cmd_rc=0
                local time_out=""
                time_out=$({ time env -u BEADS_DB -u BD_DB -u BEADS_JSONL BEADS_DIR="$ds_dir" timeout 60 "$bv_bin" --robot-triage >/dev/null 2>&1; } 2>&1) || cmd_rc=$?
                if [ "$cmd_rc" -eq 0 ]; then
                    printf '%s\n' "$time_out" | grep real >> "$BASELINE_FILE" || echo "timing unavailable" >> "$BASELINE_FILE"
                else
                    echo "timeout or error (exit $cmd_rc)" >> "$BASELINE_FILE"
                fi
            done
            echo "" >> "$BASELINE_FILE"
        fi
    done

    # 7. Memory Profile (single run with stats)
    print_section "Memory Profile"
    echo "Capturing memory profile..."
    if [ -f "$TESTDATA_DIR/medium.jsonl" ]; then
        ds_dir="$tmp_bench_root/medium/.beads"
        mkdir -p "$ds_dir"
        cp "$TESTDATA_DIR/medium.jsonl" "$ds_dir/issues.jsonl"
        env -u BEADS_DB -u BD_DB -u BEADS_JSONL BEADS_DIR="$ds_dir" GODEBUG=gctrace=1 timeout 30 "$bv_bin" --robot-triage 2>&1 | grep -E '^gc|total' | head -20 >> "$BASELINE_FILE" || echo "No GC data captured" >> "$BASELINE_FILE"
    fi

    # 8. Test Dataset Statistics
    print_section "Test Dataset Statistics"
    echo "Capturing dataset statistics..."
    for dataset in small medium large huge; do
        datafile="$TESTDATA_DIR/$dataset.jsonl"
        if [ -f "$datafile" ]; then
            lines=$(wc -l < "$datafile" | tr -d ' ')
            size=$(du -h "$datafile" | cut -f1)
            echo "$dataset: $lines issues, $size" >> "$BASELINE_FILE"
        fi
    done

    # Create symlink to latest
    ln -sf "$(basename "$BASELINE_FILE")" "$LATEST_LINK"

    print_header "Baseline Captured"
    echo -e "${GREEN}Baseline saved to: $BASELINE_FILE${NC}"
    echo -e "${GREEN}Latest link: $LATEST_LINK${NC}"
    echo ""
    echo "Summary:"
    grep -E "^(BenchmarkFull|BenchmarkPageRank|real)" "$BASELINE_FILE" | head -20
}

compare_baseline() {
    print_header "Comparing Against Baseline"

    if [ ! -f "$LATEST_LINK" ]; then
        echo -e "${RED}No baseline found. Run without --compare first.${NC}"
        exit 1
    fi

    # Run current benchmarks
    CURRENT_FILE="$BENCHMARK_DIR/current_${TIMESTAMP}.txt"

    echo "Running current benchmarks..."
    go test -bench=. -benchmem -count=3 "${BENCH_PACKAGES[@]}" 2>&1 | tee "$CURRENT_FILE"

    echo ""
    echo "=== Comparison ==="

    if command -v benchstat &> /dev/null; then
        # Extract just benchmark lines for comparison
        grep -E "^Benchmark" "$LATEST_LINK" > "$BENCHMARK_DIR/baseline_benches.txt" 2>/dev/null || true
        grep -E "^Benchmark" "$CURRENT_FILE" > "$BENCHMARK_DIR/current_benches.txt" 2>/dev/null || true

        if [ -s "$BENCHMARK_DIR/baseline_benches.txt" ] && [ -s "$BENCHMARK_DIR/current_benches.txt" ]; then
            benchstat "$BENCHMARK_DIR/baseline_benches.txt" "$BENCHMARK_DIR/current_benches.txt"
        else
            echo "No benchmark data to compare"
        fi
    else
        echo -e "${YELLOW}benchstat not found. Install with:${NC}"
        echo "  go install golang.org/x/perf/cmd/benchstat@latest"
        echo ""
        echo "Manual comparison:"
        echo "  Baseline: $LATEST_LINK"
        echo "  Current:  $CURRENT_FILE"
    fi
}

show_help() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  --generate    Generate test datasets before capturing baseline"
    echo "  --compare     Compare current performance against saved baseline"
    echo "  --help        Show this help message"
    echo ""
    echo "Without options: Capture a new baseline"
}

# Main
case "${1:-}" in
    --generate)
        generate_testdata
        capture_baseline
        ;;
    --compare)
        compare_baseline
        ;;
    --help|-h)
        show_help
        ;;
    "")
        capture_baseline
        ;;
    *)
        echo -e "${RED}Unknown option: $1${NC}"
        show_help
        exit 1
        ;;
esac
