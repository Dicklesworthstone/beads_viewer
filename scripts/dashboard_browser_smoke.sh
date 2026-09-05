#!/usr/bin/env bash
# Explicit opt-in real Chromium desktop/mobile/offline regression journeys.
#
# Usage: BV_HEADLESS_BROWSER=/path/to/chrome scripts/dashboard_browser_smoke.sh [repo-dir]
# BV_BIN=/path/to/bv skips the build. Artifacts are retained outside the tree.
# The optional repo-dir receives an additional real bundle boot check; the
# adversarial four-issue fixture always supplies known journey expectations.
# The remaining CSP unsafe-eval is required by Alpine; this test does not
# claim its removal or substitute for a future CSP migration review.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
target="${1:-}"
browser="${BV_HEADLESS_BROWSER:-}"
if [ -z "$browser" ] || [ ! -x "$browser" ]; then
  echo "INCOMPLETE: set BV_HEADLESS_BROWSER to a Chromium/Chrome binary" >&2
  exit 2
fi
for tool in node python3; do
  command -v "$tool" >/dev/null || { echo "INCOMPLETE: $tool is required" >&2; exit 2; }
done
node -e 'if (typeof WebSocket !== "function") process.exit(2)' || { echo "INCOMPLETE: Node with native WebSocket is required" >&2; exit 2; }
export BV_NO_BROWSER=1 BV_TEST_MODE=1 BV_NO_UPDATE_CHECK=1 BV_NO_SAVED_CONFIG=1

tmp="$(mktemp -d "${TMPDIR:-/tmp}/bv-browser-smoke.XXXXXX")"
echo "dashboard_browser_smoke artifacts: $tmp"
trap 'echo "Retained browser artifacts: $tmp"' EXIT

bv="${BV_BIN:-}"
if [ -z "$bv" ]; then
  bv="$tmp/bv"
  (cd "$root" && go build -o "$bv" ./cmd/bv)
fi
mkdir -p "$tmp/fixture/.beads" "$tmp/updated-fixture/.beads"
python3 - "$tmp/fixture/.beads/issues.jsonl" "$tmp/updated-fixture/.beads/issues.jsonl" <<'PY'
import json, pathlib, sys
base = dict(status='open', priority=1, issue_type='task',
            created_at='2026-09-01T12:00:00Z', updated_at='2026-09-03T12:00:00Z')
issues = [
    dict(base, id='browser-root', title='Orchid root infrastructure', labels=['backend']),
    dict(base, id='browser-detail', title='Orchid searchable detail', labels=['frontend'],
         description='Visible **markdown**.\n\n<script>window.__unsafeDisplay=true</script>\n<img src=x onerror="window.__unsafeDisplay=true">',
         comments=[dict(id='c1', issue_id='browser-detail', author='Reviewer', text='Verified comment violet', created_at='2026-09-03T12:00:00Z')],
         dependencies=[dict(issue_id='browser-detail', depends_on_id='browser-root', type='blocks')]),
    dict(base, id='browser-closed', title='Orchid completed', status='closed', closed_at='2026-09-03T12:00:00Z'),
    dict(base, id='browser-other', title='Unrelated saffron task', priority=2),
]
pathlib.Path(sys.argv[1]).write_text(''.join(json.dumps(issue) + '\n' for issue in issues))
issues[1]['title'] = 'Orchid searchable detail updated'
pathlib.Path(sys.argv[2]).write_text(''.join(json.dumps(issue) + '\n' for issue in issues))
PY
(cd "$tmp/fixture" && "$bv" --export-pages "$tmp/bundle" --pages-title "Browser journeys" >"$tmp/export.log" 2>&1) || { cat "$tmp/export.log"; exit 1; }
(cd "$tmp/updated-fixture" && "$bv" --export-pages "$tmp/updated-bundle" --pages-title "Browser journeys" >"$tmp/updated-export.log" 2>&1) || { cat "$tmp/updated-export.log"; exit 1; }
if [ -n "$target" ]; then
  (cd "$target" && "$bv" --export-pages "$tmp/project-bundle" --pages-title "Project browser check" >"$tmp/project-export.log" 2>&1) || { cat "$tmp/project-export.log"; exit 1; }
  project_bundle="$tmp/project-bundle"
else
  project_bundle=""
fi
node "$root/tests/scripts/dashboard_browser_test.mjs" "$browser" "$tmp/bundle" "$tmp/browser" journeys "$tmp/updated-bundle" "$project_bundle"
