package hooks

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime"
)

// ErrUntrustedHooks means the invoking user has not approved these declarations.
// It is an authorization error, independent of a hook's on_error policy.
var ErrUntrustedHooks = errors.New("project hooks are not trusted")

// ExecutionOptions must come from the invoking user, never project configuration.
// Approval is scoped to one invocation. No project file or environment variable
// can enable hooks, and a generic confirmation such as --yes is not approval.
type ExecutionOptions struct {
	TrustDigest string
	AllowEnv    []string // Exact parent environment variable names, not assignments.
}

// Inspection describes the declarations to review without executing or expanding
// them. JSON encoding escapes terminal control characters in project-authored text.
type Inspection struct {
	ProjectDir string       `json:"project_dir"`
	ConfigPath string       `json:"config_path"`
	Platform   string       `json:"platform"`
	Digest     string       `json:"digest"`
	Hooks      HooksByPhase `json:"hooks"`
	Warnings   []string     `json:"warnings,omitempty"`
}

// Digest identifies the complete hook snapshot and its interpretation. It covers
// the original YAML bytes as well as the effective, normalized declarations, so
// neither edits on disk nor mutations through Config() retain old approval.
// Version this domain when execution or authorization semantics change.
// Absolute paths are deliberately excluded: a fixed, explicitly supplied digest
// can be used in ephemeral CI checkouts. There is no ambient cross-project trust.
func (c *Config) Digest() (string, error) {
	if c == nil {
		return "", nil
	}
	payload, err := json.Marshal(struct {
		Policy     string       `json:"policy"`
		Platform   string       `json:"platform"`
		SourceHash string       `json:"source_hash"`
		Hooks      HooksByPhase `json:"hooks"`
	}{"bv-project-hooks-v1", runtime.GOOS, c.sourceHash, c.Hooks})
	if err != nil {
		return "", fmt.Errorf("fingerprinting hooks: %w", err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), nil
}

// Inspect returns the loaded snapshot. It never reads ambient credentials or
// grants authorization; the caller must review it before supplying its digest.
func (l *Loader) Inspect() (Inspection, error) {
	if l.config == nil {
		return Inspection{}, errors.New("hooks must be loaded before inspection")
	}
	snapshot := cloneConfig(l.config)
	digest, err := snapshot.Digest()
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{
		ProjectDir: snapshot.projectDir,
		ConfigPath: snapshot.configPath,
		Platform:   runtime.GOOS,
		Digest:     digest,
		Hooks:      snapshot.Hooks,
		Warnings:   append([]string(nil), l.warnings...),
	}, nil
}

func (c *Config) hasHooks() bool {
	return c != nil && (len(c.Hooks.PreExport) > 0 || len(c.Hooks.PostExport) > 0)
}

func (c *Config) authorize(digest string) error {
	if !c.hasHooks() {
		return nil
	}
	expected, err := c.Digest()
	if err != nil {
		return err
	}
	if digest == "" || digest != expected {
		return fmt.Errorf("%w: review bv --hooks-info, then supply --trust-hooks with the reviewed digest, or use --no-hooks to export without running hooks", ErrUntrustedHooks)
	}
	return nil
}

func cloneConfig(c *Config) *Config {
	if c == nil {
		return nil
	}
	snapshot := *c
	snapshot.Hooks.PreExport = cloneHooks(c.Hooks.PreExport)
	snapshot.Hooks.PostExport = cloneHooks(c.Hooks.PostExport)
	return &snapshot
}

func cloneHooks(hooks []Hook) []Hook {
	if hooks == nil {
		return nil
	}
	cloned := make([]Hook, len(hooks))
	for i, hook := range hooks {
		cloned[i] = hook
		cloned[i].Env = maps.Clone(hook.Env)
	}
	return cloned
}
