# Recursive Workspace Discovery

## Overview

This feature enables `bv` to discover and aggregate beads from multiple `.beads` directories within a workspace, rather than limiting itself to a single repository's beads.

## Motivation

When working on multi-repository projects, developers often need to view and manage beads across all related repositories from a single location. The previous behavior required manually navigating to each repository to view its beads, which was inefficient for distributed systems spanning multiple repos.

## Usage

### Enable Recursive Discovery

Use the `--discover-recursive` flag to enable recursive workspace discovery:

```bash
# Discover all .beads directories in current folder and children
bv --discover-recursive

# Specify maximum search depth (default: 3)
bv --discover-recursive --discover-depth=2
```

### Default Behavior

By default, `bv` searches only the current directory (and git worktree root if applicable). The recursive discovery feature is opt-in via the `--discover-recursive` flag.

### Search Depth

The `--discover-depth` flag controls how many directory levels deep to search:

- Default: 3 levels
- Example: With depth=3, will search `./`, `./*/`, `./*/*/`, and `./*/*/*/`

### Ignored Directories

The following directories are automatically skipped during discovery to improve performance:

- `.git`
- `node_modules`
- `vendor`
- `dist`
- `build`
- `target`
- `.cache`
- `.bv`
- `.idea`
- `.vscode`
- `.venv` / `venv`
- `__pycache__`
- `.cargo`

## How It Works

### Discovery Algorithm

1. Start from the current working directory (or specified path)
2. Walk the directory tree up to `maxDepth` levels
3. Skip ignored directories (e.g., `.git`, `node_modules`)
4. When a `.beads` directory is found:
   - Record its path
   - Identify the parent directory as the repository root
   - Skip descending into the `.beads` directory itself

### Issue Aggregation

When multiple `.beads` directories are discovered:

1. Issues are loaded from each workspace
2. Each issue is tagged with repository metadata:
   - `repo:<reponame>` label added to identify source repository
   - `_repo_path:<path>` tag added with full repository path
3. All issues are aggregated into a single list
4. The TUI displays them together with repository labels visible

### Repository Metadata

Each issue from a discovered workspace automatically receives:

- **Label**: `repo:<repository-name>` - The folder name containing the `.beads` directory
- **Tag**: `_repo_path:<full-path>` - The absolute path to the repository

Example:
```
Repository structure:
/home/user/workspace/
├── .beads/         → repo:workspace
├── api/.beads/     → repo:api
└── web/.beads/     → repo:web
```

## Example Workflows

### Multi-Repository Project

```bash
# Project structure:
# /home/coder/
# ├── botburrow-hub/.beads/
# ├── botburrow-agents/.beads/
# ├── agent-definitions/.beads/
# └── ardenone-cluster/.beads/

cd /home/coder
bv --discover-recursive

# Output shows beads from all 4 repositories, labeled:
# [repo:botburrow-hub] bd-1xy - Test worker system
# [repo:botburrow-agents] bd-1pg - Implement multi-repo support
# [repo:agent-definitions] bd-2vn - Document agent registration
# [repo:ardenone-cluster] bd-3km - Configure ingress
```

### Limit Search Depth

```bash
# Only search 1 level deep (immediate children only)
cd /home/coder
bv --discover-recursive --discover-depth=1

# Will find:
# - /home/coder/botburrow-hub/.beads/
# - /home/coder/botburrow-agents/.beads/
# But not:
# - /home/coder/deep/nested/repo/.beads/ (too deep)
```

### Filter by Repository

Once beads are loaded with repository labels, you can filter by repository using label filters in the TUI or via command-line (if supported):

```bash
# Filter to show only beads from botburrow-hub
bv --discover-recursive --robot-by-label=repo:botburrow-hub
```

## Implementation Details

### New Functions

**`internal/datasource/workspace.go`**:
- `DiscoverBeadsWorkspaces(root string, maxDepth int) []WorkspaceInfo`
  - Walks directory tree to find all `.beads` directories
  - Returns metadata for each discovered workspace

- `LoadIssuesRecursive(repoPath string, maxDepth int) ([]model.Issue, error)`
  - Discovers workspaces
  - Loads issues from each
  - Tags with repository metadata
  - Returns aggregated issues

- `LoadIssuesWithDiscovery(repoPath string, enableRecursive bool, maxDepth int) ([]model.Issue, error)`
  - Wrapper function that enables/disables recursive discovery
  - Used by main.go to support the `--discover-recursive` flag

### Command-Line Flags

**`cmd/bv/main.go`**:
- `--discover-recursive`: Enable recursive workspace discovery (default: false)
- `--discover-depth`: Maximum depth for recursive search (default: 3)

### Testing

Unit tests are provided in `internal/datasource/workspace_test.go`:
- `TestDiscoverBeadsWorkspaces`: Basic discovery functionality
- `TestDiscoverBeadsWorkspaces_MaxDepth`: Depth limiting
- `TestDiscoverBeadsWorkspaces_IgnorePatterns`: Directory filtering
- `TestDiscoverBeadsWorkspaces_NoBeadsDirectories`: Empty workspaces

## Backward Compatibility

This feature is **fully backward compatible**:
- Default behavior unchanged (single repository mode)
- Opt-in via `--discover-recursive` flag
- Existing flags and functionality unaffected
- All existing commands work as before

## Performance Considerations

- **Directory traversal**: Uses `filepath.WalkDir` for efficient walking
- **Ignored directories**: Skips large directories like `node_modules` to reduce I/O
- **Depth limiting**: `--discover-depth` prevents excessive recursion
- **Memory**: All issues are loaded into memory (same as single-repo mode)

For very large workspaces (100+ repositories), consider:
- Using `--discover-depth=1` or `--discover-depth=2` to limit scope
- Using workspace config files (`.bv/workspace.yaml`) with explicit repository lists
- Filtering by label after loading to focus on specific repositories

## Future Enhancements

Potential improvements for future iterations:
- Environment variable `BV_DISCOVER_RECURSIVE=1` for persistent enabling
- Configuration file to set default discovery behavior
- Parallel loading of issues from multiple repositories
- Repository-specific ignore patterns in workspace config
- Visual grouping by repository in TUI views
