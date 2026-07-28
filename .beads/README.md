# Beads with br (beads_rust)

This repository uses **Beads** for issue tracking through the Rust `br` CLI. Issues live beside the code in `.beads/issues.jsonl`.

**Important:** `br` is non-invasive and never executes git commands. After `br sync --flush-only`, stage and commit `.beads/` through the repository's normal git workflow.

## What is Beads?

Beads is a dependency-aware, CLI-first issue tracker designed for developers and coding agents. The local SQLite database provides fast queries; the tracked JSONL export carries issue history through git.

**Learn more:** [github.com/Dicklesworthstone/beads_rust](https://github.com/Dicklesworthstone/beads_rust)

## Quick Start

### Essential Commands

```bash
# Create a new issue
br create --title="Add user authentication" --type=task --priority=2

# View and inspect issues
br list
br show <issue-id>

# Update and complete work
br update <issue-id> --status=in_progress
br close <issue-id> --reason="Completed"

# Export database changes to tracked JSONL
br sync --flush-only

# Then stage and commit through normal git workflow
git add .beads/
git commit -m "sync beads"
```

### Working with Issues

Issues in Beads are:

- **Git-native**: Exported to `.beads/issues.jsonl` and committed like code
- **AI-friendly**: CLI-first design works well for coding agents
- **Dependency-aware**: `br ready` shows open work with no active blockers
- **Explicitly synchronized**: `br` never commits or pushes behind your back

## Get Started with Beads

To initialize another repository after installing `br`:

```bash
br init --prefix "<lowercase-project-name>"
br create --title="Try out Beads" --type=task --priority=2
br sync --flush-only
```

## Learn More

- **Documentation**: [github.com/Dicklesworthstone/beads_rust](https://github.com/Dicklesworthstone/beads_rust)
- **Agent guide**: Run `br robot-docs guide`
- **Workspace diagnostics**: Run `br doctor`

---

*Beads: durable project memory beside the code.*
