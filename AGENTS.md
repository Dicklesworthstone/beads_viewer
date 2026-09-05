# AGENTS.md — beads_viewer

> Guidelines for AI coding agents working in this Go codebase.

---

## RULE 0 - THE FUNDAMENTAL OVERRIDE PREROGATIVE

If I tell you to do something, even if it goes against what follows below, YOU MUST LISTEN TO ME. I AM IN CHARGE, NOT YOU.

---

## RULE NUMBER 1: NO FILE DELETION

**YOU ARE NEVER ALLOWED TO DELETE A FILE WITHOUT EXPRESS PERMISSION.** Even a new file that you yourself created, such as a test code file. You have a horrible track record of deleting critically important files or otherwise throwing away tons of expensive work. As a result, you have permanently lost any and all rights to determine that a file or folder should be deleted.

**YOU MUST ALWAYS ASK AND RECEIVE CLEAR, WRITTEN PERMISSION BEFORE EVER DELETING A FILE OR FOLDER OF ANY KIND.**

---

## Irreversible Git & Filesystem Actions — DO NOT EVER BREAK GLASS

1. **Absolutely forbidden commands:** `git reset --hard`, `git clean -fd`, `rm -rf`, or any command that can delete or overwrite code/data must never be run unless the user explicitly provides the exact command and states, in the same message, that they understand and want the irreversible consequences.
2. **No guessing:** If there is any uncertainty about what a command might delete or overwrite, stop immediately and ask the user for specific approval. "I think it's safe" is never acceptable.
3. **Safer alternatives first:** When cleanup or rollbacks are needed, request permission to use non-destructive options (`git status`, `git diff`, `git stash`, copying to backups) before ever considering a destructive command.
4. **Mandatory explicit plan:** Even after explicit user authorization, restate the command verbatim, list exactly what will be affected, and wait for a confirmation that your understanding is correct. Only then may you execute it—if anything remains ambiguous, refuse and escalate.
5. **Document the confirmation:** When running any approved destructive command, record (in the session notes / final response) the exact user text that authorized it, the command actually run, and the execution time. If that record is absent, the operation did not happen.

---

## Git Branch: ONLY Use `main`, NEVER `master`

**The default branch is `main`. The `master` branch exists only for legacy URL compatibility.**

- **All work happens on `main`** — commits, PRs, feature branches all merge to `main`
- **Never reference `master` in code or docs** — if you see `master` anywhere, it's a bug that needs fixing
- **The `master` branch must stay synchronized with `main`** — after pushing to `main`, also push to `master`:
  ```bash
  git push origin main:master
  ```

**If you see `master` referenced anywhere:**
1. Update it to `main`
2. Ensure `master` is synchronized: `git push origin main:master`

---

## Toolchain: Go & Go Modules

We only use **Go Modules** in this project, NEVER any other package manager.

- **Version:** Go 1.25+ (check `go.mod` for exact version)
- **Toolchain:** `go1.25.5` (see `go.mod`)
- **Dependency versions:** Managed via `go.mod` / `go.sum`
- **Lockfile:** `go.sum` (auto-managed by `go mod`)

### Key Commands

```bash
go build ./...                    # Build all packages
go test ./...                     # Run all tests
go test ./... -race               # Run with race detector
go test ./pkg/analysis/... -v     # Verbose tests for specific package
go vet ./...                      # Static analysis
gofmt -w .                        # Format all Go files
go mod tidy                       # Clean up unused deps
```

### Key Dependencies

| Module | Purpose |
|--------|---------|
| `charmbracelet/bubbletea` | TUI framework (Elm architecture) |
| `charmbracelet/lipgloss` | Terminal styling |
| `charmbracelet/bubbles` | Reusable TUI components (viewport, list, etc.) |
| `charmbracelet/huh` | Interactive form components |
| `charmbracelet/glamour` | Markdown rendering |
| `modernc.org/sqlite` | Pure-Go SQLite for the static-site export (`beads.sqlite3` with an FTS5 index) |
| `gonum.org/v1/gonum` | Graph algorithms (PageRank, betweenness, HITS, eigenvector) |
| `goccy/go-json` | High-performance JSON serialization |
| `fsnotify/fsnotify` | Filesystem event watching (daemon mode) |
| `golang.org/x/sync` | Extended concurrency primitives |
| `pgregory.net/rapid` | Property-based testing |
| `gopkg.in/yaml.v3` | YAML configuration parsing |
| `ajstarks/svgo` | SVG generation for graph export |
| `atotto/clipboard` | Clipboard integration |

### Module Management

- Never manually edit `go.sum`
- Use `go mod tidy` to clean up unused deps
- Dependencies are tracked in `go.mod` / `go.sum`

---

## Code Editing Discipline

### No Script-Based Changes

**NEVER** run a script that processes/changes code files in this repo. Brittle regex-based transformations create far more problems than they solve.

- **Always make code changes manually**, even when there are many instances
- For many simple changes: use parallel subagents
- For subtle/complex changes: do them methodically yourself

### No File Proliferation

If you want to change something or add a feature, **revise existing code files in place**.

**NEVER** create variations like:
- `mainV2.go`
- `main_improved.go`
- `main_enhanced.go`

New files are reserved for **genuinely new functionality** that makes zero sense to include in any existing file. The bar for creating new files is **incredibly high**.

---

## Backwards Compatibility

We do not care about backwards compatibility—we're in early development with no users. We want to do things the **RIGHT** way with **NO TECH DEBT**.

- Never create "compatibility shims"
- Never create wrapper functions for deprecated APIs
- Just fix the code directly

---

## Compiler Checks (CRITICAL)

**After any substantive code changes, you MUST verify no errors were introduced:**

```bash
# Build all packages
go build ./...

# Run static analysis
go vet ./...

# Verify formatting
gofmt -l .
```

If you see errors, **carefully understand and resolve each issue**. Read sufficient context to fix them the RIGHT way.

---

## Testing

### Testing Policy

Every package includes `_test.go` files alongside the implementation. Tests must cover:
- Happy path
- Edge cases (empty input, max values, boundary conditions)
- Error conditions

Cross-package integration tests live in the `tests/` directory.

### Never Open Browsers

**Tests must NEVER automatically open a browser.** All browser-opening functions check `BV_NO_BROWSER` and `BV_TEST_MODE` environment variables. These are set globally via `TestMain` in:
- `tests/e2e/common_test.go`
- `pkg/export/main_test.go`
- `pkg/ui/main_test.go`

When adding new browser-opening code, always check these env vars first:
```go
if os.Getenv("BV_NO_BROWSER") != "" || os.Getenv("BV_TEST_MODE") != "" {
    return nil
}
```

### Unit Tests

```bash
# Run all tests across the project
go test ./...

# Run with output
go test ./... -v

# Run tests for a specific package
go test ./pkg/analysis/... -v
go test ./pkg/search/... -v
go test ./pkg/correlation/... -v
go test ./pkg/export/... -v
go test ./pkg/ui/... -v
go test ./pkg/loader/... -v

# Run with race detector
go test ./... -race

# Run with coverage
go test ./... -cover

# Run a specific test
go test -run TestSpecificName ./pkg/...
```

### Test Categories

| Package | Focus Areas |
|---------|-------------|
| `pkg/analysis` | Graph metrics (PageRank, betweenness, HITS, eigenvector, k-core), triage, planning, priority detection |
| `pkg/search` | Hybrid semantic search (text + graph metrics), hash embedder, ranking, presets |
| `pkg/correlation` | Bead-to-commit correlation, orphan detection, history tracking |
| `pkg/export` | Static site export, HTML bundle generation, GitHub Pages deployment |
| `pkg/loader` | JSONL parsing, bead loading, validation |
| `pkg/ui` | TUI views, robot output formatting, lipgloss styling |
| `pkg/watcher` | Filesystem watching, daemon mode, debouncing |
| `pkg/hooks` | Git hooks integration |
| `pkg/recipe` | Pre-filter recipes (actionable, high-impact) |
| `pkg/drift` | Configuration drift detection |
| `tests/e2e` | End-to-end CLI tests, robot output validation |

### Test Patterns

- Use table-driven tests for multiple cases
- Use `t.TempDir()` for temporary files
- Use `t.Helper()` in test helpers
- Check `testing.Short()` for long-running tests

---

## Third-Party Library Usage

If you aren't 100% sure how to use a third-party library, **SEARCH ONLINE** to find the latest documentation and current best practices.

---

## beads_viewer — This Project

**This is the project you're working on.** beads_viewer (`bv`) is a graph-aware triage engine for Beads projects (`.beads/issues.jsonl` in current `br` and Dolt-backed `bd` workspaces, with `.beads/beads.jsonl` supported for legacy workspaces). It computes PageRank, betweenness, critical path, cycles, HITS, eigenvector, and k-core metrics deterministically. It provides both an interactive TUI and machine-readable `--robot-*` JSON outputs for AI agent consumption.

### What It Does

Analyzes Beads issue graphs to produce actionable triage recommendations, parallel execution plans, priority misalignment detection, and sprint forecasting. Combines graph-theoretic metrics with issue metadata to rank work items by impact, urgency, and unblocking potential.

### Architecture

```
.beads/{issues,beads}.jsonl → Loader → Graph Build → ┬─ Phase 1 (instant): degree, topo sort, density
                                              └─ Phase 2 (async, size-tiered): PageRank, betweenness,
                                                                          HITS, eigenvector, cycles
                                                              │
                                              ┌───────────────┴───────────────┐
                                              ▼                               ▼
                                    Robot JSON Output                  Interactive TUI
                                    (--robot-* flags)              (bubbletea Elm arch)
                                              │
                                   ┌──────────┴──────────┐
                                   ▼                      ▼
                            Triage/Plan/             Search/Export/
                            Insights/Alerts          Pages/Graph
```

### Project Structure

```
beads_viewer/
├── go.mod                          # Module root (Go 1.25+)
├── cmd/bv/                         # CLI entry point (Cobra/pflag and robot registry)
├── pkg/
│   ├── analysis/                   # Graph metrics, triage, planning, priority, forecasting
│   ├── search/                     # Hybrid semantic search (text + graph metrics)
│   ├── correlation/                # Bead-to-commit correlation, orphan detection
│   ├── export/                     # Static site export (HTML/JS bundle, GitHub Pages)
│   ├── loader/                     # JSONL parsing, bead loading, validation
│   ├── model/                      # Core data types (Bead, Dependency, etc.)
│   ├── ui/                         # TUI views (bubbletea), lipgloss styling, robot output
│   ├── watcher/                    # Filesystem watching, daemon mode, debouncing
│   ├── hooks/                      # Git hooks integration
│   ├── recipe/                     # Pre-filter recipes (actionable, high-impact)
│   ├── drift/                      # Configuration drift detection
│   ├── agents/                     # Agent detection and session tracking
│   ├── cass/                       # Cross-agent session search integration
│   ├── baseline/                   # Baseline comparison utilities
│   ├── metrics/                    # Internal metrics collection
│   ├── instance/                   # Instance management
│   ├── updater/                    # Self-update mechanism
│   ├── version/                    # Version information
│   ├── workspace/                  # Multi-workspace support
│   ├── testutil/                   # Shared test utilities
│   ├── debug/                      # Debug utilities
│   └── util/                       # General utilities
├── tests/                          # Cross-package integration / E2E tests
├── benchmarks/                     # Performance benchmarks
└── bv-graph-wasm/                  # WASM module for interactive graph visualization
```

### Key Design Decisions

- **Two-phase analysis**: Phase 1 metrics (degree, topo sort, density) are instant; Phase 2 (PageRank, betweenness, HITS, eigenvector, cycles) runs async with size-tiered timeouts (2 s down to 200 ms, `ConfigForSize` in `pkg/analysis/config.go`) — check `status` flags
- **Robot-first API**: All `--robot-*` flags emit deterministic JSON to stdout; human TUI is secondary
- **Pure-Go SQLite** (`modernc.org/sqlite`) for the static-site export's FTS5 index — no CGO dependency; `pkg/search` itself is an in-memory hash-embedding index, not SQLite
- **Hybrid search** combines text relevance with graph metrics (PageRank, status, impact, priority, recency) via configurable weight presets
- **Elm architecture TUI** via bubbletea — all state transitions are message-based
- **Structured error wrapping** with `fmt.Errorf("context: %w", err)` for traceability
- **No raw prints in production** — TUI output goes through lipgloss styling; robot mode outputs JSON to stdout; errors to stderr
- **Division safety** — always guard against division by zero before computing averages/ratios
- **Nil checks** — always check for nil before dereferencing pointers, especially in graph traversal
- **Concurrency** — use `sync.RWMutex` for shared state; capture channels before unlock to avoid races
- **Browser safety** — all browser-opening functions gated by `BV_NO_BROWSER` / `BV_TEST_MODE` env vars

### Logging & Console Output

- Use structured logging patterns; avoid raw `fmt.Println` for production logs
- TUI output goes through lipgloss styling; don't mix raw prints with styled output
- Robot mode (`--robot-*`) outputs JSON to stdout; human mode uses styled TUI
- Errors should be wrapped with `fmt.Errorf("context: %w", err)` for traceability

### Hybrid Semantic Search (CLI)

`bv --search` supports hybrid ranking (text + graph metrics).

```bash
# Default (text-only)
bv --search "login oauth"

# Hybrid mode with preset
bv --search "login oauth" --search-mode hybrid --search-preset impact-first

# Hybrid with custom weights
bv --search "login oauth" --search-mode hybrid \
  --search-weights '{"text":0.4,"pagerank":0.2,"status":0.15,"impact":0.1,"priority":0.1,"recency":0.05}'

# Robot JSON output (adds mode/preset/weights + component_scores for hybrid)
bv --search "login oauth" --search-mode hybrid --robot-search
```

Env defaults:
- `BV_SEARCH_MODE` (text|hybrid)
- `BV_SEARCH_PRESET` (default|bug-hunting|sprint-planning|impact-first|text-only)
- `BV_SEARCH_WEIGHTS` (JSON string, overrides preset)

### Static Site Export for Stakeholder Reporting

Generate a static dashboard for non-technical stakeholders:

```bash
# Interactive wizard (recommended)
bv --pages

# Or export locally
bv --export-pages ./dashboard --pages-title "Sprint 42 Status"
```

The output is a self-contained HTML/JS bundle that:
- Shows triage recommendations (from --robot-triage)
- Visualizes dependencies
- Supports full-text search (FTS5)
- Works offline after initial load
- Requires no installation to view

**Deployment options:**
- `bv --pages` → Interactive wizard for GitHub Pages deployment
- `bv --export-pages ./dir` → Local export for custom hosting
- `bv --preview-pages ./dir` → Preview bundle locally

**For CI/CD integration:**
```bash
bv --export-pages ./bv-pages --pages-title "Nightly Build"
# Then deploy ./bv-pages to your hosting of choice
```

### Go Best Practices

Follow all practices in `docs/planning/GOLANG_BEST_PRACTICES.md`. Key points:

**Error Handling:**
```go
// Always wrap errors with context
if err != nil {
    return fmt.Errorf("loading config: %w", err)
}

// Check errors immediately after the call
result, err := doSomething()
if err != nil {
    return err
}
```

**Division Safety:**
```go
// Always guard against division by zero
if len(items) > 0 {
    avg := total / float64(len(items))
}
```

**Nil Checks:**
```go
// Check for nil before dereferencing
if dep != nil && dep.Type.IsBlocking() {
    // safe to use dep
}
```

**Concurrency:**
```go
// Use sync.RWMutex for shared state
mu.RLock()
value := sharedMap[key]
mu.RUnlock()

// Capture channels before unlock to avoid races
mu.RLock()
ch := someChannel
mu.RUnlock()
for item := range ch {
    // process
}
```

---

## MCP Agent Mail — Multi-Agent Coordination

A mail-like layer that lets coding agents coordinate asynchronously via MCP tools and resources. Provides identities, inbox/outbox, searchable threads, and advisory file reservations with human-auditable artifacts in Git.

> **Note:** This section is optional. If you're operating as a single agent or using alternative coordination methods, skip to "Beads" below. If Agent Mail is not available and you need multi-agent coordination, flag to the user—they may need to start it with `am` alias or manually.

**Troubleshooting:** If Agent Mail fails with "Too many open files" (common on macOS), restart with higher limit: `ulimit -n 4096; python -m mcp_agent_mail.cli serve-http`

### Why It's Useful

- **Prevents conflicts:** Explicit file reservations (leases) for files/globs
- **Token-efficient:** Messages stored in per-project archive, not in context
- **Quick reads:** `resource://inbox/...`, `resource://thread/...`

### Same Repository Workflow

1. **Register identity:**
   ```
   ensure_project(project_key=<abs-path>)
   register_agent(project_key, program, model)
   ```

2. **Reserve files before editing:**
   ```
   file_reservation_paths(project_key, agent_name, ["pkg/**"], ttl_seconds=3600, exclusive=true)
   ```

3. **Communicate with threads:**
   ```
   send_message(..., thread_id="bv-123")
   fetch_inbox(project_key, agent_name)
   acknowledge_message(project_key, agent_name, message_id)
   ```

4. **Quick reads:**
   ```
   resource://inbox/{Agent}?project=<abs-path>&limit=20
   resource://thread/{id}?project=<abs-path>&include_bodies=true
   ```

### Macros vs Granular Tools

- **Prefer macros for speed:** `macro_start_session`, `macro_prepare_thread`, `macro_file_reservation_cycle`, `macro_contact_handshake`
- **Use granular tools for control:** `register_agent`, `file_reservation_paths`, `send_message`, `fetch_inbox`, `acknowledge_message`

### Common Pitfalls

- `"from_agent not registered"`: Always `register_agent` in the correct `project_key` first
- `"FILE_RESERVATION_CONFLICT"`: Adjust patterns, wait for expiry, or use non-exclusive reservation
- **Auth errors:** If JWT+JWKS enabled, include bearer token with matching `kid`

---

## Beads (br) — Dependency-Aware Issue Tracking

Beads provides a lightweight, dependency-aware issue database and CLI (`br` - beads_rust) for selecting "ready work," setting priorities, and tracking status. It complements MCP Agent Mail's messaging and file reservations.

**Important:** `br` is non-invasive—it NEVER runs git commands automatically. You must manually commit changes after `br sync --flush-only`.

### Conventions

- **Single source of truth:** Beads for task status/priority/dependencies; Agent Mail for conversation and audit
- **Shared identifiers:** Use Beads issue ID (e.g., `br-123`) as Mail `thread_id` and prefix subjects with `[br-123]`
- **Reservations:** When starting a task, call `file_reservation_paths()` with the issue ID in `reason`

### Typical Agent Flow

1. **Pick ready work (Beads):**
   ```bash
   br ready --json  # Choose highest priority, no blockers
   ```

2. **Reserve edit surface (Mail):**
   ```
   file_reservation_paths(project_key, agent_name, ["pkg/**"], ttl_seconds=3600, exclusive=true, reason="br-123")
   ```

3. **Announce start (Mail):**
   ```
   send_message(..., thread_id="br-123", subject="[br-123] Start: <title>", ack_required=true)
   ```

4. **Work and update:** Reply in-thread with progress

5. **Complete and release:**
   ```bash
   br close 123 --reason "Completed"
   br sync --flush-only  # Export to JSONL (no git operations)
   ```
   ```
   release_file_reservations(project_key, agent_name, paths=["pkg/**"])
   ```
   Final Mail reply: `[br-123] Completed` with summary

### Mapping Cheat Sheet

| Concept | Value |
|---------|-------|
| Mail `thread_id` | `br-###` |
| Mail subject | `[br-###] ...` |
| File reservation `reason` | `br-###` |
| Commit messages | Include `br-###` for traceability |

---

## bv — Graph-Aware Triage Engine

bv is a graph-aware triage engine for Beads projects (`.beads/issues.jsonl` in current `br` and Dolt-backed `bd` workspaces, with `.beads/beads.jsonl` supported for legacy workspaces). It computes PageRank, betweenness, critical path, cycles, HITS, eigenvector, and k-core metrics deterministically.

**Scope boundary:** bv handles *what to work on* (triage, priority, planning). For agent-to-agent coordination (messaging, work claiming, file reservations), use MCP Agent Mail.

**CRITICAL: Use ONLY `--robot-*` flags. Bare `bv` launches an interactive TUI that blocks your session.**

### The Workflow: Start With Triage

**`bv --robot-triage` is your single entry point.** Its `triage` object contains:
- `quick_ref`: at-a-glance counts + top 3 picks
- `recommendations`: ranked actionable items with scores, reasons, unblock info
- `quick_wins`: low-effort high-impact items
- `blockers_to_clear`: items that unblock the most downstream work
- `project_health`: status/type/priority distributions, graph metrics
- `commands`: copy-paste shell commands for next steps

```bash
bv --robot-triage        # THE MEGA-COMMAND: start here
bv --robot-next          # Minimal: just the single top pick + claim command
```

### Command Reference

**Planning:**
| Command | Returns |
|---------|---------|
| `--robot-plan` | Parallel execution tracks with `unblocks` lists |
| `--robot-priority` | Priority misalignment detection with confidence |

**Graph Analysis:**
| Command | Returns |
|---------|---------|
| `--robot-insights` | Full metrics: PageRank, betweenness, HITS, eigenvector, critical path, cycles, k-core, articulation points, slack |
| `--robot-label-health` | Per-label health: `health_level`, `velocity_score`, `staleness`, `blocked_count` |
| `--robot-label-flow` | Cross-label dependency: `flow_matrix`, `dependencies`, `bottleneck_labels` |
| `--robot-label-attention [--attention-limit=N]` | Attention-ranked labels |

**History & Change Tracking:**
| Command | Returns |
|---------|---------|
| `--robot-history` | Bead-to-commit correlations |
| `--robot-diff --diff-since <ref>` | Changes since ref: new/closed/modified issues, cycles |

**Other:**
| Command | Returns |
|---------|---------|
| `--robot-burndown <sprint>` | Sprint burndown, scope changes, at-risk items |
| `--robot-forecast <id\|all>` | ETA estimates from a duration heuristic and 30-day closure velocity (not a scheduler) |
| `--robot-alerts` | Stale issues, blocking cascades, priority mismatches |
| `--robot-suggest` | Hygiene: duplicates, missing deps, label suggestions |
| `--robot-graph [--graph-format=json\|dot\|mermaid]` | Dependency graph export |
| `--export-graph <file.html>` | Interactive HTML visualization |

### Scoping & Filtering

```bash
bv --robot-plan --label backend              # Scope to label's subgraph
bv --robot-insights --as-of HEAD~30          # Historical point-in-time
bv --recipe actionable --robot-plan          # Pre-filter: ready to work
bv --recipe high-impact --robot-triage       # Pre-filter: top PageRank
bv --robot-triage --robot-triage-by-track    # Group by parallel work streams
bv --robot-triage --robot-triage-by-label    # Group by domain
```

### Understanding Robot Output

**Issue-backed analysis output:**
- `data_hash` — Fingerprint of issue data used by the response
- `status` — For metric-bearing commands such as insights, plan, and priority: `computed|pending|timeout|skipped` + elapsed ms; sampled betweenness is `computed` with `reason: "approximate"`
- `as_of` / `as_of_commit` — Present for historical issue analysis using `--as-of`
- `source_authority`, `authority_hash`, `scope_hash` — Loaded-source completeness, full authority identity, and selected-candidate scope

Metadata-only commands have their own schemas. Check `--robot-schema` for
command-specific required fields. Source completeness does not establish a
live tracker route; inspect the suggested action's original ID and directory.

**Two-phase analysis:**
- **Phase 1 (instant):** degree, topo sort, density
- **Phase 2 (async, size-tiered timeouts):** PageRank, betweenness, HITS, eigenvector, cycles. See `ConfigForSize`; check each metric's status. An empty cycle list after skipped or timed-out analysis does not prove acyclicity, and stored cycles may be capped.

### jq Quick Reference

```bash
bv --robot-triage | jq '.triage.quick_ref'                 # At-a-glance summary
bv --robot-triage | jq '.triage.recommendations[0]'         # Top recommendation
bv --robot-plan | jq '.plan.summary.highest_impact'        # Best unblock target
bv --robot-insights | jq '.status'                         # Check metric readiness
bv --robot-insights | jq '.Cycles'                         # Circular deps (must fix!)
```

---

## UBS — Ultimate Bug Scanner

**Golden Rule:** `ubs <changed-files>` before every commit. Exit 0 = safe. Exit >0 = fix & re-run.

### Commands

```bash
ubs file.go file2.go                    # Specific files (< 1s) — USE THIS
ubs $(git diff --name-only --cached)    # Staged files — before commit
ubs --only=go pkg/                      # Language filter (3-5x faster)
ubs --ci --fail-on-warning .            # CI mode — before PR
ubs .                                   # Whole project (ignores vendor/, etc.)
```

### Output Format

```
⚠️  Category (N errors)
    file.go:42:5 – Issue description
    💡 Suggested fix
Exit code: 1
```

Parse: `file:line:col` → location | 💡 → how to fix | Exit 0/1 → pass/fail

### Fix Workflow

1. Read finding → category + fix suggestion
2. Navigate `file:line:col` → view context
3. Verify real issue (not false positive)
4. Fix root cause (not symptom)
5. Re-run `ubs <file>` → exit 0
6. Commit

### Bug Severity

- **Critical (always fix):** nil dereference, division by zero, race conditions, resource leaks
- **Important (production):** Error handling, type assertions without check, unwrapped errors
- **Contextual (judgment):** TODO/FIXME, unused variables

---

## RCH — Remote Compilation Helper

RCH offloads `go build`, `go test`, `go vet`, and other compilation commands to a fleet of 8 remote Contabo VPS workers instead of building locally. This prevents compilation storms from overwhelming csd when many agents run simultaneously.

**RCH is installed at `~/.local/bin/rch` and is hooked into Claude Code's PreToolUse automatically.** Most of the time you don't need to do anything if you are Claude Code — builds are intercepted and offloaded transparently.

To manually offload a build:
```bash
rch exec -- go build ./...
rch exec -- go test ./...
rch exec -- go vet ./...
```

Quick commands:
```bash
rch doctor                    # Health check
rch workers probe --all       # Test connectivity to all 8 workers
rch status                    # Overview of current state
rch queue                     # See active/waiting builds
```

If rch or its workers are unavailable, it fails open — builds run locally as normal.

### Trust boundary

RCH is a remote build service, so treat every offloaded command as leaving this machine:

- **What is shipped:** the working tree of the current project (source, vendor/, test fixtures, `.beads/`), the command line, and an allowlisted subset of environment variables needed by the Go toolchain. Workers cache Go modules and build outputs per project (`.rch-go/` locally mirrors that cache and is git-ignored).
- **What must never be shipped:** secrets and tokens (`GITHUB_TOKEN`, `GH_TOKEN`, cloud credentials, `~/.netrc`, `.git/config` credential helpers), private keys, or a tree that contains customer data. Do not export such values into the shell before running a build, and do not commit them into the tree. If a build needs a credential (private module proxy, signed release), run it locally or get explicit approval first.
- **Who runs the workers:** the 8 Contabo VPS hosts are operated by the project maintainer; they are not a shared public service. Assume anything sent there can be read by whoever administers those hosts and may be retained in caches until the next cleanup. Private or credential-bearing builds therefore require the maintainer's explicit approval per run.
- **Fallback:** when RCH or its workers are unreachable it fails open to a local build, so a green build never proves the remote path was used; check `rch status` if that matters.
- **Docs:** `rch doctor`, `rch --help`, and the RCH repository README describe the env allowlist and the cache lifecycle in detail.

**Note for Codex/GPT-5.2:** Codex does not have the automatic PreToolUse hook, but you can (and should) still manually offload compute-intensive compilation commands using `rch exec -- <command>`. This avoids local resource contention when multiple agents are building simultaneously.

---

## ast-grep vs ripgrep

**Use `ast-grep` when structure matters.** It parses code and matches AST nodes, ignoring comments/strings, and can **safely rewrite** code.

- Refactors/codemods: rename APIs, change import forms
- Policy checks: enforce patterns across a repo
- Editor/automation: LSP mode, `--json` output

**Use `ripgrep` when text is enough.** Fastest way to grep literals/regex.

- Recon: find strings, TODOs, log lines, config values
- Pre-filter: narrow candidate files before ast-grep

### Rule of Thumb

- Need correctness or **applying changes** → `ast-grep`
- Need raw speed or **hunting text** → `rg`
- Often combine: `rg` to shortlist files, then `ast-grep` to match/modify

### Go Examples

```bash
# Find all error returns without wrapping
ast-grep run -l Go -p 'return err'

# Find all fmt.Println (should use structured logging)
ast-grep run -l Go -p 'fmt.Println($$$)'

# Quick grep for a function name
rg -n 'func.*LoadConfig' -t go

# Combine: find files then match precisely
rg -l -t go 'sync.Mutex' | xargs ast-grep run -l Go -p 'mu.Lock()'
```

---

## Morph Warp Grep — AI-Powered Code Search

**Use `mcp__morph-mcp__warp_grep` for exploratory "how does X work?" questions.** An AI agent expands your query, greps the codebase, reads relevant files, and returns precise line ranges with full context.

**Use `ripgrep` for targeted searches.** When you know exactly what you're looking for.

**Use `ast-grep` for structural patterns.** When you need AST precision for matching/rewriting.

### When to Use What

| Scenario | Tool | Why |
|----------|------|-----|
| "How is graph analysis implemented?" | `warp_grep` | Exploratory; don't know where to start |
| "Where is PageRank computed?" | `warp_grep` | Need to understand architecture |
| "Find all uses of `NewAnalyzer`" | `ripgrep` | Targeted literal search |
| "Find files with `fmt.Println`" | `ripgrep` | Simple pattern |
| "Rename function across codebase" | `ast-grep` | Structural refactor |

### warp_grep Usage

```
mcp__morph-mcp__warp_grep(
  repoPath: "/dp/beads_viewer",
  query: "How does the correlation package detect orphan commits?"
)
```

Returns structured results with file paths, line ranges, and extracted code snippets.

### Anti-Patterns

- **Don't** use `warp_grep` to find a specific function name → use `ripgrep`
- **Don't** use `ripgrep` to understand "how does X work" → wastes time with manual reads
- **Don't** use `ripgrep` for codemods → risks collateral edits
## cass — Cross-Agent Session Search

`cass` indexes prior agent conversations (Claude Code, Codex, Cursor, Gemini, ChatGPT, Aider, etc.) into a unified, searchable index so you can reuse solved problems.

**NEVER run bare `cass`** — it launches an interactive TUI. Always use `--robot` or `--json`.

### Quick Start

```bash
# Check if index is healthy (exit 0=ok, 1=run index first)
cass health

# Search across all agent histories
cass search "authentication error" --robot --limit 5

# View a specific result (from search output)
cass view /path/to/session.jsonl -n 42 --json

# Expand context around a line
cass expand /path/to/session.jsonl -n 42 -C 3 --json

# Learn the full API
cass capabilities --json      # Feature discovery
cass robot-docs guide         # LLM-optimized docs
```

### Key Flags

| Flag | Purpose |
|------|---------|
| `--robot` / `--json` | Machine-readable JSON output (required!) |
| `--fields minimal` | Reduce payload: `source_path`, `line_number`, `agent` only |
| `--limit N` | Cap result count |
| `--agent NAME` | Filter to specific agent (claude, codex, cursor, etc.) |
| `--days N` | Limit to recent N days |

**stdout = data only, stderr = diagnostics. Exit 0 = success.**

### Robot Mode Etiquette

- Prefer `cass --robot-help` and `cass robot-docs <topic>` for machine-first docs
- The CLI is forgiving: globals placed before/after subcommand are auto-normalized
- If parsing fails, follow the actionable errors with examples
- Use `--color=never` in non-TTY automation for ANSI-free output

### Pre-Flight Health Check

```bash
cass health --json
```

Returns in <50ms:
- **Exit 0:** Healthy—proceed with queries
- **Exit 1:** Unhealthy—run `cass index --full` first

### Exit Codes

| Code | Meaning | Retryable |
|------|---------|-----------|
| 0 | Success | N/A |
| 1 | Health check failed | Yes—run `cass index --full` |
| 2 | Usage/parsing error | No—fix syntax |
| 3 | Index/DB missing | Yes—run `cass index --full` |

Treat cass as a way to avoid re-solving problems other agents already handled.

---

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Sync beads** - `br sync --flush-only` to export to JSONL
5. **Hand off** - Provide context for next session

---

Note for Codex/GPT-5.2:

You constantly bother me and stop working with concerned questions that look similar to this:

```
Unexpected changes (need guidance)

- Working tree still shows edits I did not make in Cargo.toml, Cargo.lock, src/cli/commands/upgrade.rs, src/storage/sqlite.rs, tests/conformance.rs, tests/storage_deps.rs. Please advise whether to keep/commit/revert these before any further work. I did not touch them.

Next steps (pick one)

1. Decide how to handle the unrelated modified files above so we can resume cleanly.
2. Triage beads_rust-orko (clippy/cargo warnings) and beads_rust-ydqr (rustfmt failures).
3. If you want a full suite run later, fix conformance/clippy blockers and re-run cargo test --all.
```

NEVER EVER DO THAT AGAIN. The answer is literally ALWAYS the same: those are changes created by the potentially dozen of other agents working on the project at the same time. This is not only a common occurence, it happens multiple times PER MINUTE. The way to deal with it is simple: you NEVER, under ANY CIRCUMSTANCE, stash, revert, overwrite, or otherwise disturb in ANY way the work of other agents. Just treat those changes identically to changes that you yourself made. Just fool yourself into thinking YOU made the changes and simply don't recall it for some reason.

---

## Note on Built-in TODO Functionality

Also, if I ask you to explicitly use your built-in TODO functionality, don't complain about this and say you need to use beads. You can use built-in TODOs if I tell you specifically to do so. Always comply with such orders.

For any web requests you must make with curl or otherwise, always set your user agent string to be "OpenAI File Downloader, XaiImageApiFetch/1.0"

<!-- bv-agent-instructions-v5 -->

---

## Beads Workflow Integration

This project uses a Beads tracker—either the Go `bd` CLI or the Rust `br` CLI—for issue tracking, plus [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) (`bv`) for graph-aware triage. Issues are stored in `.beads/`. `bv` auto-discovers supported JSONL exports, including `.beads/issues.jsonl` and legacy `.beads/beads.jsonl`.

**Choose the tracker CLI from this repository's instructions and configuration.** Use `bd` commands in a Go Beads workspace and `br` commands in a beads_rust workspace. Do not run both trackers against the same workspace or infer the tracker solely from the JSONL filename.

### Using bv as an AI sidecar

bv is a graph-aware triage engine for Beads projects. Instead of parsing .beads/issues.jsonl / .beads/beads.jsonl directly or hallucinating graph traversal, use robot flags for deterministic, dependency-aware outputs with precomputed metrics (PageRank, betweenness, critical path, cycles, HITS, eigenvector, k-core).

**Scope boundary:** bv handles *what to work on* (triage, priority, planning). The selected tracker CLI (`bd` or `br`) handles creating, claiming, modifying, and closing beads.

**CRITICAL: Use ONLY --robot-* flags. Bare bv launches an interactive TUI that blocks your session.**

#### The Workflow: Start With Triage

**`bv --robot-triage` is your single entry point.** Its `triage` object contains:
- `quick_ref`: at-a-glance counts + top 3 picks
- `recommendations`: ranked actionable items with scores, reasons, unblock info
- `quick_wins`: low-effort high-impact items
- `blockers_to_clear`: items that unblock the most downstream work
- `project_health`: status/type/priority distributions, graph metrics
- `commands`: copy-paste shell commands for next steps

```bash
bv --robot-triage        # THE MEGA-COMMAND: start here
bv --robot-next          # Minimal: just the single top pick + claim command

# TOON output (--format toon): a compact tabular encoding. Measured on this
# repository it is 7% smaller than JSON for --robot-graph but 9-15% LARGER for
# nested payloads (--robot-triage, --robot-plan, --robot-insights,
# --robot-label-health); use --stats to see both sizes before adopting it.
bv --robot-graph --format toon
bv --robot-triage --format toon --stats
```

Recommendations can include blocked or assigned work; `triage.quick_ref.top_picks` reflects snapshot readiness. A suggested action records its original local ID, working directory, and tracker route. Use that route rather than a namespaced display ID or an unrelated current directory. Inspect current tracker state before execution: analysis does not reserve work or guarantee that a later claim succeeds.

#### Other bv Commands

| Command | Returns |
|---------|---------|
| `--robot-plan` | Parallel execution tracks with unblocks lists |
| `--robot-priority` | Priority misalignment detection with confidence |
| `--robot-insights` | Full metrics: PageRank, betweenness, HITS, eigenvector, critical path, cycles, k-core |
| `--robot-alerts` | Stale issues, blocking cascades, priority mismatches |
| `--robot-suggest` | Hygiene: duplicates, missing deps, label suggestions, cycle breaks |
| `--robot-diff --diff-since <ref>` | Changes since ref: new/closed/modified issues |
| `--robot-graph [--graph-format=json\|dot\|mermaid]` | Dependency graph export |

Every robot command emits one JSON object; with `--graph-format=dot` or `mermaid` the diagram text is the `graph` field (`bv --robot-graph --graph-format=dot | jq -r .graph`), not the whole output.

#### Scoping & Filtering

```bash
bv --robot-plan --label backend              # Scope to label's subgraph
bv --robot-insights --as-of HEAD~30          # Historical point-in-time
bv --recipe actionable --robot-plan          # Pre-filter: ready to work (no blockers)
bv --recipe high-impact --robot-triage       # Pre-filter: top PageRank scores
```

### Tracker Commands for Issue Management

Use exactly one command family, matching the tracker configured for the repository.

#### Rust beads_rust (`br`)

```bash
br ready --json                       # Show issues ready to work (no blockers)
br list --status=open --json          # All open issues
br show <id> --json                   # Full issue details with dependencies
br create --title="..." --type=task --priority=2 --json
br update <id> --status=in_progress --json
br close <id> --reason="Completed" --json
br close <id1> <id2> --reason="Completed" --json
br sync --flush-only                  # Export DB to JSONL after Beads mutations
```

#### Go Beads (`bd`)

```bash
bd ready --json                       # Show issues ready to work
bd show <id> --json                   # Full issue details
bd create "..." -t task -p 2 --json
bd update <id> --claim --json         # Atomically claim work
bd close <id> --json
bd dep add <issue> <depends-on>
bd export -o .beads/issues.jsonl        # Refresh the compatibility export read by bv
```

### Workflow Pattern

1. **Triage**: Run `bv --robot-triage` to find the highest-impact actionable work
2. **Verify**: Check the selected tracker's `show`/`ready` output before claiming
3. **Claim**: Use `br update <id> --status=in_progress --json` or `bd update <id> --claim --json`
4. **Work**: Implement the task
5. **Complete**: Use the selected tracker's `close` command
6. **Refresh for bv**: Run `br sync --flush-only` or the `bd export` command above so the JSONL export is current

### Key Concepts

- **Dependencies**: Issues can block other issues. `br ready --json` and `bd ready --json` show unblocked work.
- **Priority**: P0=critical, P1=high, P2=medium, P3=low, P4=backlog (use numbers 0-4, not words)
- **Types**: task, bug, feature, epic, chore, docs, question
- **Blocking**: Use `br dep add <issue> <depends-on>` or `bd dep add <issue> <depends-on>` to add dependencies

### Git Policy

Tracker commands do not grant permission to commit or push application code. Follow this repository's own git and tracker instructions before staging, committing, syncing, or pushing. If the repository says "commit only when asked," that rule overrides any generic workflow advice.

<!-- end-bv-agent-instructions -->
