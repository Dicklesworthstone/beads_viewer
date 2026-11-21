#!/bin/bash
# Coverage script for bv.
#
# Notes:
# - Must work on macOS default bash (3.2), so avoid associative arrays.
# - Uses the coverprofile to compute per-package coverage (statement-weighted).
#
# Usage:
#   ./scripts/coverage.sh            # Run coverage + summary
#   ./scripts/coverage.sh html       # Generate + open HTML report
#   ./scripts/coverage.sh check      # Enforce thresholds (CI mode)
#   ./scripts/coverage.sh pkg        # Per-package breakdown
#   ./scripts/coverage.sh uncovered  # Uncovered sections (sample)

set -euo pipefail

export BV_NO_BROWSER=1 BV_TEST_MODE=1 BV_NO_SAVED_CONFIG=1 BV_NO_UPDATE_CHECK=1

COVERAGE_DIR="${COVERAGE_DIR:-coverage}"
COVERAGE_FILE="${COVERAGE_DIR}/coverage.out"
HTML_FILE="${COVERAGE_DIR}/coverage.html"

# Project threshold is computed over `pkg/**` only (aligned with ci.yml).
PROJECT_THRESHOLD=71

# Space-separated list of packages to test for coverage.
# Default excludes slow E2E suites while still covering the code that matters.
COVER_PACKAGES="${COVER_PACKAGES:-./pkg/...}"

mkdir -p "$COVERAGE_DIR"

threshold_for_pkg() {
	case "$1" in
		github.com/Dicklesworthstone/beads_viewer/pkg/analysis) echo 75 ;;
		github.com/Dicklesworthstone/beads_viewer/pkg/export) echo 69 ;;
		github.com/Dicklesworthstone/beads_viewer/pkg/recipe) echo 90 ;;
		github.com/Dicklesworthstone/beads_viewer/pkg/ui) echo 55 ;;
		github.com/Dicklesworthstone/beads_viewer/pkg/loader) echo 80 ;;
		github.com/Dicklesworthstone/beads_viewer/pkg/updater) echo 55 ;;
		github.com/Dicklesworthstone/beads_viewer/pkg/watcher) echo 60 ;;
		github.com/Dicklesworthstone/beads_viewer/pkg/workspace) echo 85 ;;
		*) echo "" ;;
	esac
}

run_coverage() {
    echo "Running tests with coverage..."
    # shellcheck disable=SC2086
    go test -covermode=atomic -coverprofile="$COVERAGE_FILE" ${COVER_PACKAGES}
    echo ""
}

show_summary() {
    echo "=== Coverage Summary ==="
    go tool cover -func="$COVERAGE_FILE" | tail -1
    pkg_total="$(pkg_total_coverage)"
    echo "pkg/*:											(statements)					${pkg_total}%"
    echo ""
}

per_package_coverage() {
	awk '
		NR == 1 { next } # mode line
		{
			file = $1
			sub(/:.*/, "", file)          # strip :line.col,line.col
			pkg = file
			sub(/\/[^\/]+$/, "", pkg)     # strip /file.go
			stmts = $2
			count = $3
			total[pkg] += stmts
			if (count > 0) covered[pkg] += stmts
		}
		END {
			for (pkg in total) {
				pct = (covered[pkg] / total[pkg]) * 100
				printf "%s %.2f\n", pkg, pct
			}
		}
	' "$COVERAGE_FILE" | sort
}

pkg_total_coverage() {
	awk '
		NR == 1 { next } # mode line
		{
			file = $1
			sub(/:.*/, "", file)          # strip :line.col,line.col
			if (file !~ /^github.com\/Dicklesworthstone\/beads_viewer\/pkg\//) {
				next
			}
			stmts = $2
			count = $3
			total += stmts
			if (count > 0) covered += stmts
		}
		END {
			if (total == 0) {
				printf "0.00"
			} else {
				printf "%.2f", (covered / total) * 100
			}
		}
	' "$COVERAGE_FILE"
}

show_per_package() {
	echo "=== Per-Package Coverage (statement-weighted) ==="
	per_package_coverage
	echo ""
}

show_detailed() {
    echo "=== Detailed Function Coverage ==="
    go tool cover -func="$COVERAGE_FILE" | head -50
    echo "..."
    echo "(Use './scripts/coverage.sh html' for full report)"
    echo ""
}

show_uncovered() {
    echo "=== Uncovered Lines ==="
    echo "Generating uncovered lines report..."
	awk '
		NR == 1 { next } # mode line
		{
			# Line format: file:start.end,start.end stmts count
			loc = $1
			stmts = $2
			count = $3
			if (count == 0) {
				print loc " (stmts=" stmts ")"
			}
		}
	' "$COVERAGE_FILE" | head -30

    echo ""
    echo "(Showing first 30 uncovered sections)"
}

generate_html() {
    echo "Generating HTML coverage report..."
    go tool cover -html="$COVERAGE_FILE" -o "$HTML_FILE"
    echo "Report generated: $HTML_FILE"

    # Open in browser unless BV_NO_BROWSER / BV_TEST_MODE is set
    if [ -n "${BV_NO_BROWSER:-}" ] || [ -n "${BV_TEST_MODE:-}" ]; then
        echo "Browser launch suppressed (BV_NO_BROWSER/BV_TEST_MODE): $HTML_FILE"
    elif command -v open &> /dev/null; then
        open "$HTML_FILE"
    elif command -v xdg-open &> /dev/null; then
        xdg-open "$HTML_FILE"
    else
        echo "Open $HTML_FILE in your browser"
    fi
}

check_thresholds() {
    echo "=== Checking Coverage Thresholds ==="
	failed=0

	# Project threshold (pkg/* only, statement-weighted).
	total="$(pkg_total_coverage)"
	if awk -v c="$total" -v t="$PROJECT_THRESHOLD" 'BEGIN { exit !(c < t) }'; then
		echo "FAIL: pkg/* coverage ${total}% < ${PROJECT_THRESHOLD}%"
		failed=1
	else
		echo "PASS: pkg/* coverage ${total}% >= ${PROJECT_THRESHOLD}%"
	fi

	# Per-package thresholds (statement-weighted).
	while read -r pkg pct; do
		req="$(threshold_for_pkg "$pkg")"
		if [ -z "$req" ]; then
			continue
		fi
		if awk -v p="$pct" -v r="$req" 'BEGIN { exit !(p < r) }'; then
			echo "FAIL: $pkg ${pct}% < ${req}%"
			failed=1
		else
			echo "PASS: $pkg ${pct}% >= ${req}%"
		fi
	done < <(per_package_coverage)

	if [ "$failed" -eq 0 ]; then
		echo ""
		echo "All coverage thresholds passed!"
		return 0
	fi

	echo ""
	echo "Some coverage thresholds failed!"
	return 1
}

# Main
case "${1:-summary}" in
    html)
        run_coverage
        generate_html
        ;;
    check)
        run_coverage
        check_thresholds
        ;;
    pkg|package)
        run_coverage
        show_per_package
        ;;
    uncovered)
        run_coverage
        show_uncovered
        ;;
    detailed)
        run_coverage
        show_detailed
        ;;
    summary|*)
        run_coverage
        show_summary
        show_per_package
        ;;
esac
