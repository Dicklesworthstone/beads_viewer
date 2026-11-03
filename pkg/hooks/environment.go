package hooks

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// inheritedHookEnvKeys is the operational environment inherited without an
// explicit user grant. All other names are denied, regardless of whether they
// look like credentials. Keep this list exact: prefixes such as LC_, XDG_, or
// tool-specific namespaces can carry arbitrary credentials or startup code.
// The list is also part of inspection and the approval digest.
func inheritedHookEnvKeys(windows bool) []string {
	names := []string{
		"HOME", "LANG", "LANGUAGE", "LC_ADDRESS", "LC_ALL", "LC_COLLATE",
		"LC_CTYPE", "LC_IDENTIFICATION", "LC_MEASUREMENT", "LC_MESSAGES",
		"LC_MONETARY", "LC_NAME", "LC_NUMERIC", "LC_PAPER", "LC_TELEPHONE",
		"LC_TIME", "PATH", "TEMP", "TERM", "TMP", "TMPDIR", "TZ",
		"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
	}
	if windows {
		names = append(names, "APPDATA", "COMSPEC", "HOMEDRIVE", "HOMEPATH",
			"LOCALAPPDATA", "PATHEXT", "SYSTEMDRIVE", "SYSTEMROOT", "USERPROFILE", "WINDIR")
		// Windows uses these hidden entries for per-drive working directories.
		// Only correctly formed drive entries are accepted from the parent.
		for drive := 'A'; drive <= 'Z'; drive++ {
			names = append(names, "="+string(drive)+":")
		}
	}
	sort.Strings(names)
	return names
}

// buildHookEnvironment constructs the one environment visible to both YAML
// expansion and the child process. Only the invoking user's grants can restore
// a non-operational parent variable; declaring an alias in YAML cannot do so.
// windows is explicit so Windows case-insensitive semantics are testable on all
// platforms. The returned environment has no duplicate variable names.
func buildHookEnvironment(parent, contextEnv []string, overrides map[string]string, grants []string, windows bool) ([]string, error) {
	if err := validateEnvGrants(grants); err != nil {
		return nil, err
	}
	if err := validateHookEnv(overrides, windows); err != nil {
		return nil, err
	}

	keyFor := func(name string) string {
		if windows {
			// Match os/exec's Windows environment deduplication.
			return strings.ToLower(name)
		}
		return name
	}
	allowed := make(map[string]bool, len(grants))
	for _, name := range grants {
		allowed[keyFor(name)] = true
	}
	for _, name := range inheritedHookEnvKeys(windows) {
		allowed[keyFor(name)] = true
	}

	type binding struct{ name, value string }
	values := make(map[string]binding, len(parent)+len(contextEnv)+len(overrides))
	for _, entry := range parent {
		name, value, ok := splitEnvironmentEntry(entry, windows)
		if !ok {
			continue
		}
		key := keyFor(name)
		if !allowed[key] {
			continue
		}
		values[key] = binding{name, value}
	}
	for _, entry := range contextEnv {
		name, value, ok := splitEnvironmentEntry(entry, false)
		if !ok {
			return nil, fmt.Errorf("invalid export context environment entry")
		}
		values[keyFor(name)] = binding{name, value}
	}

	// Preserve sorted YAML expansion order, while looking up the effective
	// child values using the target platform's variable-name semantics.
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := os.Expand(overrides[name], func(ref string) string {
			return values[keyFor(ref)].value
		})
		values[keyFor(name)] = binding{name, value}
	}

	names = names[:0]
	for key := range values {
		names = append(names, key)
	}
	sort.Strings(names)
	env := make([]string, 0, len(names))
	for _, key := range names {
		entry := values[key]
		env = append(env, entry.name+"="+entry.value)
	}
	return env, nil
}

// validateEnvGrants accepts names, never values, patterns, or shell syntax.
// The portable identifier grammar is shared by Unix and Windows CLI grants.
func validateEnvGrants(grants []string) error {
	for i, name := range grants {
		if !portableEnvName(name) {
			// A mistaken NAME=secret argument must not echo its value into logs.
			return fmt.Errorf("invalid hook environment grant %d: expected a variable name (letters, digits and underscores; no leading digit)", i+1)
		}
	}
	return nil
}

func portableEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func validateHookEnv(overrides map[string]string, windows bool) error {
	seen := make(map[string]string, len(overrides))
	// Sort before validation so ambiguous declarations always produce the same
	// diagnostic, regardless of Go map iteration order.
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, "=\x00") || !utf8.ValidString(name) {
			return fmt.Errorf("invalid hook environment variable name %q", name)
		}
		if strings.ContainsRune(overrides[name], '\x00') {
			return fmt.Errorf("hook environment variable %q contains a NUL byte", name)
		}
		if !utf8.ValidString(overrides[name]) {
			return fmt.Errorf("hook environment variable %q contains invalid UTF-8", name)
		}
		if windows {
			key := strings.ToLower(name)
			if previous, exists := seen[key]; exists {
				return fmt.Errorf("hook environment variables %q and %q refer to the same Windows variable", previous, name)
			}
			seen[key] = name
		}
	}
	return nil
}

// splitEnvironmentEntry drops malformed inherited entries. Windows also uses
// hidden per-drive working-directory variables such as "=C:=C:\\work". They
// are retained from the parent only, and cannot be injected by YAML or grants.
func splitEnvironmentEntry(entry string, windows bool) (string, string, bool) {
	if strings.ContainsRune(entry, '\x00') {
		return "", "", false
	}
	if windows && len(entry) >= 4 && entry[0] == '=' && entry[2] == ':' && entry[3] == '=' &&
		(entry[1] >= 'a' && entry[1] <= 'z' || entry[1] >= 'A' && entry[1] <= 'Z') {
		return entry[:3], entry[4:], true
	}
	name, value, ok := strings.Cut(entry, "=")
	return name, value, ok && name != ""
}
