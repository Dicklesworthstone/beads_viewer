// Package hooks provides a hook system for bv export automation.
// Hooks are configured via .bv/hooks.yaml and run at specific points
// in the export pipeline (pre-export, post-export).
package hooks

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// HookPhase represents when a hook runs
type HookPhase string

const (
	// PreExport runs before export generation. Failure cancels export.
	PreExport HookPhase = "pre-export"
	// PostExport runs after export is written. Failure is logged but doesn't break export.
	PostExport HookPhase = "post-export"
)

// Hook defines a single hook configuration
type Hook struct {
	Name    string            `yaml:"name" json:"name"`                             // Human-readable name
	Command string            `yaml:"command" json:"command"`                       // Shell command to run
	Argv    []string          `yaml:"argv,omitempty" json:"argv,omitempty"`         // Literal program and arguments; mutually exclusive with Command
	Timeout time.Duration     `yaml:"timeout,omitempty" json:"timeout,omitempty"`   // Execution timeout (default: 30s)
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`           // Additional environment variables
	OnError string            `yaml:"on_error,omitempty" json:"on_error,omitempty"` // "fail" (default for pre) or "continue" (default for post)
}

// Config holds all hook configurations
type Config struct {
	Hooks HooksByPhase `yaml:"hooks" json:"hooks"`
	// IgnoredSelfTrust absorbs any self-declared `trusted:` key in project YAML
	// without ever granting trust, so trust verification still runs and emits
	// ErrUntrustedHooks guidance while KnownFields(true) rejects schema typos.
	IgnoredSelfTrust any `yaml:"trusted,omitempty" json:"-"`

	// Provenance belongs to the loader, never to project-controlled YAML.
	projectDir string
	configPath string
	sourceHash string
}

// HooksByPhase organizes hooks by their execution phase
type HooksByPhase struct {
	PreExport  []Hook `yaml:"pre-export,omitempty" json:"pre-export,omitempty"`
	PostExport []Hook `yaml:"post-export,omitempty" json:"post-export,omitempty"`
}

// ExportContext contains information passed to hooks via environment variables
type ExportContext struct {
	ExportPath   string    // BV_EXPORT_PATH: Output file path
	ExportFormat string    // BV_EXPORT_FORMAT: 'markdown' or 'json'
	IssueCount   int       // BV_ISSUE_COUNT: Number of issues exported
	Timestamp    time.Time // BV_TIMESTAMP: Export timestamp (RFC3339)
}

// ToEnv converts export context to environment variables
func (c ExportContext) ToEnv() []string {
	return []string{
		fmt.Sprintf("BV_EXPORT_PATH=%s", c.ExportPath),
		fmt.Sprintf("BV_EXPORT_FORMAT=%s", c.ExportFormat),
		fmt.Sprintf("BV_ISSUE_COUNT=%d", c.IssueCount),
		fmt.Sprintf("BV_TIMESTAMP=%s", c.Timestamp.Format(time.RFC3339)),
	}
}

// DefaultTimeout is the default hook execution timeout
const DefaultTimeout = 30 * time.Second

// Loader loads hook configuration from .bv/hooks.yaml
type Loader struct {
	projectDir string
	config     *Config
	warnings   []string
}

// LoaderOption configures the loader
type LoaderOption func(*Loader)

// WithProjectDir sets the project directory (default: current directory)
func WithProjectDir(dir string) LoaderOption {
	return func(l *Loader) {
		l.projectDir = dir
	}
}

// NewLoader creates a new hook loader with options
func NewLoader(opts ...LoaderOption) *Loader {
	l := &Loader{}

	for _, opt := range opts {
		opt(l)
	}

	if l.projectDir == "" {
		l.projectDir, _ = os.Getwd()
	}

	return l
}

// Load loads hook configuration from .bv/hooks.yaml
func (l *Loader) Load() error {
	l.warnings = nil
	l.config = nil // A failed reload must not leave a previously approved config usable.
	projectDir, err := filepath.Abs(l.projectDir)
	if err != nil {
		return fmt.Errorf("resolving hooks project directory: %w", err)
	}
	projectDir, err = filepath.EvalSymlinks(projectDir)
	if err != nil {
		return fmt.Errorf("resolving hooks project directory: %w", err)
	}
	projectDir = resolveHooksProjectDir(projectDir)
	l.projectDir = projectDir
	configPath := filepath.Join(l.projectDir, ".bv", "hooks.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No config file means no hooks - this is OK
			l.config = &Config{projectDir: projectDir, configPath: configPath}
			return nil
		}
		return fmt.Errorf("reading hooks config: %w", err)
	}

	config, err := ParseConfig(data)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", configPath, err)
	}

	// Apply defaults and validate
	l.normalizeConfig(&config)
	for _, phase := range [][]Hook{config.Hooks.PreExport, config.Hooks.PostExport} {
		for _, hook := range phase {
			if err := validateHookExecution(hook, runtime.GOOS); err != nil {
				return fmt.Errorf("hook %q in %s: %w", hook.Name, configPath, err)
			}
		}
	}
	config.projectDir = projectDir
	config.configPath = configPath
	config.sourceHash = fmt.Sprintf("%x", sha256.Sum256(data))

	l.config = &config
	return nil
}

// normalizeConfig applies defaults and validates hooks
func (l *Loader) normalizeConfig(config *Config) {
	config.Hooks.PreExport, l.warnings = normalizeHooks(config.Hooks.PreExport, PreExport, l.warnings)
	config.Hooks.PostExport, l.warnings = normalizeHooks(config.Hooks.PostExport, PostExport, l.warnings)
}

// normalizeHooks applies defaults, drops empty legacy shell declarations, and
// accumulates warnings. Invalid argv declarations are retained for validation.
func normalizeHooks(hooks []Hook, phase HookPhase, warnings []string) ([]Hook, []string) {
	var out []Hook
	for i := range hooks {
		hook := hooks[i]
		if hook.Argv == nil && strings.TrimSpace(hook.Command) == "" {
			warnings = append(warnings, fmt.Sprintf("%s hook %d has empty command; skipping", phase, i+1))
			continue
		}
		if hook.Timeout == 0 {
			hook.Timeout = DefaultTimeout
		}
		onError := strings.ToLower(strings.TrimSpace(hook.OnError))
		switch onError {
		case "":
			hook.OnError = defaultOnError(phase)
		case "fail", "continue":
			hook.OnError = onError
		default:
			fallback := defaultOnError(phase)
			warnings = append(warnings, fmt.Sprintf("%s hook %d has invalid on_error %q; using %q", phase, i+1, hook.OnError, fallback))
			hook.OnError = fallback
		}
		if hook.Name == "" {
			hook.Name = fmt.Sprintf("%s-%d", phase, i+1)
		}
		out = append(out, hook)
	}
	return out, warnings
}

func defaultOnError(phase HookPhase) string {
	if phase == PreExport {
		return "fail"
	}
	return "continue"
}

// Config returns the loaded configuration (or empty if not loaded)
func (l *Loader) Config() *Config {
	if l.config == nil {
		return &Config{}
	}
	return l.config
}

// HasHooks returns true if any hooks are configured
func (l *Loader) HasHooks() bool {
	if l.config == nil {
		return false
	}
	return len(l.config.Hooks.PreExport) > 0 || len(l.config.Hooks.PostExport) > 0
}

// GetHooks returns hooks for a specific phase
func (l *Loader) GetHooks(phase HookPhase) []Hook {
	if l.config == nil {
		return nil
	}

	switch phase {
	case PreExport:
		return l.config.Hooks.PreExport
	case PostExport:
		return l.config.Hooks.PostExport
	default:
		return nil
	}
}

// Warnings returns any warnings from loading
func (l *Loader) Warnings() []string {
	return l.warnings
}

// LoadDefault creates a loader and loads with default settings
func LoadDefault() (*Loader, error) {
	loader := NewLoader()
	if err := loader.Load(); err != nil {
		return nil, err
	}
	return loader, nil
}

// ParseConfig strictly decodes raw .bv/hooks.yaml bytes, rejecting unknown or
// misspelled keys at every level while treating empty or comment-only files as
// an empty configuration.
func ParseConfig(data []byte) (Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Config{}, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var config Config
	if err := dec.Decode(&config); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, err
	}

	var extra yaml.Node
	if err := dec.Decode(&extra); err != nil {
		if !errors.Is(err, io.EOF) {
			return Config{}, err
		}
	} else {
		return Config{}, fmt.Errorf("line %d: multiple YAML documents are not supported", extra.Line)
	}

	config.IgnoredSelfTrust = nil
	return config, nil
}

func resolveHooksProjectDir(projectDir string) string {
	cleanDir := filepath.Clean(projectDir)
	if _, err := os.Lstat(filepath.Join(cleanDir, ".bv", "hooks.yaml")); err == nil {
		return cleanDir
	}
	if info, err := os.Stat(filepath.Join(cleanDir, ".beads")); err == nil && info.IsDir() {
		return cleanDir
	}

	stopDirs := make(map[string]bool)
	addStop := func(p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			stopDirs[filepath.Clean(abs)] = true
		}
		if eval, err := filepath.EvalSymlinks(p); err == nil {
			stopDirs[filepath.Clean(eval)] = true
		}
	}
	addStop(os.TempDir())
	for _, ceiling := range filepath.SplitList(os.Getenv("GIT_CEILING_DIRECTORIES")) {
		addStop(ceiling)
	}

	curr := cleanDir
	for {
		parent := filepath.Dir(curr)
		if parent == curr || stopDirs[curr] || stopDirs[parent] {
			break
		}
		if info, err := os.Stat(filepath.Join(parent, ".beads")); err == nil && info.IsDir() {
			if eval, evalErr := filepath.EvalSymlinks(parent); evalErr == nil {
				return filepath.Clean(eval)
			}
			return parent
		}
		if _, gitErr := os.Stat(filepath.Join(parent, ".git")); gitErr == nil {
			if _, hooksErr := os.Lstat(filepath.Join(parent, ".bv", "hooks.yaml")); hooksErr == nil {
				if eval, evalErr := filepath.EvalSymlinks(parent); evalErr == nil {
					return filepath.Clean(eval)
				}
				return parent
			}
		}
		curr = parent
	}
	return cleanDir
}

// UnmarshalYAML parses durations and the strict literal-argument declaration.
func (h *Hook) UnmarshalYAML(node *yaml.Node) error {
	// Keep this DTO aligned with Hook; Timeout and Argv need custom decoding.
	// If you add a field to Hook, you MUST add it here and to validateHookMappingKeys.
	type hookDTO struct {
		Name    string            `yaml:"name"`
		Command string            `yaml:"command"`
		Argv    yaml.Node         `yaml:"argv,omitempty"`
		Timeout string            `yaml:"timeout,omitempty"`
		Env     map[string]string `yaml:"env,omitempty"`
		OnError string            `yaml:"on_error,omitempty"`
	}

	if err := validateHookMappingKeys(node); err != nil {
		return err
	}

	var dto hookDTO
	if err := node.Decode(&dto); err != nil {
		return err
	}

	h.Name = dto.Name
	h.Command = dto.Command
	argv, err := decodeHookArgv(&dto.Argv)
	if err != nil {
		return err
	}
	h.Argv = argv
	h.Env = dto.Env
	h.OnError = dto.OnError

	timeout := strings.TrimSpace(dto.Timeout)
	if timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err == nil {
			if d < 0 {
				return fmt.Errorf("invalid timeout %q: must be non-negative", timeout)
			}
			h.Timeout = d
		} else {
			// Fallback: try numeric value (assumed seconds)
			// This handles cases like "timeout: 30" which YAML decodes as string "30"
			// but time.ParseDuration rejects (missing unit).
			const maxDurationSeconds = float64(1<<63-1) / float64(time.Second)
			seconds, scanErr := strconv.ParseFloat(timeout, 64)
			if scanErr == nil && seconds >= 0 && seconds <= maxDurationSeconds && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
				h.Timeout = time.Duration(seconds * float64(time.Second))
			} else {
				return fmt.Errorf("invalid timeout %q: %w", timeout, err)
			}
		}
	}

	return nil
}

func validateHookMappingKeys(node *yaml.Node) error {
	target := node
	seenAliases := make(map[*yaml.Node]bool)
	for target != nil && target.Kind == yaml.AliasNode {
		if seenAliases[target] {
			return fmt.Errorf("line %d: hook entry contains a cyclic YAML alias", target.Line)
		}
		seenAliases[target] = true
		target = target.Alias
	}
	if target == nil {
		return fmt.Errorf("hook entry is nil")
	}
	if target.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: hook entry must be a YAML mapping", target.Line)
	}

	seenKeys := make(map[string]bool, len(target.Content)/2)
	for i := 0; i+1 < len(target.Content); i += 2 {
		keyNode := target.Content[i]
		key := keyNode.Value
		switch key {
		case "name", "command", "argv", "timeout", "env", "on_error":
		default:
			return fmt.Errorf("line %d: field %s not found in type hooks.Hook (valid fields: name, command, argv, timeout, env, on_error)", keyNode.Line, key)
		}
		if seenKeys[key] {
			return fmt.Errorf("line %d: duplicate field %q in hook definition", keyNode.Line, key)
		}
		seenKeys[key] = true
	}
	return nil
}

// Decode argv strictly rather than allowing YAML's scalar-to-string coercion.
// Missing argv retains the legacy command form; an explicit null or empty
// list is a declaration error, never an instruction to silently skip a hook.
func decodeHookArgv(node *yaml.Node) ([]string, error) {
	if node.Kind == 0 {
		return nil, nil
	}
	resolve := func(node *yaml.Node) (*yaml.Node, error) {
		seen := make(map[*yaml.Node]bool)
		for node != nil && node.Kind == yaml.AliasNode {
			if seen[node] {
				return nil, fmt.Errorf("argv contains a cyclic YAML alias")
			}
			seen[node] = true
			node = node.Alias
		}
		if node == nil {
			return nil, fmt.Errorf("argv contains an invalid YAML alias")
		}
		return node, nil
	}
	list, err := resolve(node)
	if err != nil {
		return nil, err
	}
	if list.Kind != yaml.SequenceNode || len(list.Content) == 0 {
		return nil, fmt.Errorf("argv must be a nonempty list of strings")
	}
	argv := make([]string, len(list.Content))
	for i, item := range list.Content {
		value, err := resolve(item)
		if err != nil {
			return nil, err
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return nil, fmt.Errorf("argv element %d must be a string", i+1)
		}
		argv[i] = value.Value
	}
	return argv, nil
}
