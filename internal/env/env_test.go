package env_test

import (
	"go/ast"
	"go/parser"
	gotok "go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
)

func TestEnv_AllVariablesRegistered(t *testing.T) {
	vars := env.All()
	if len(vars) < 30 {
		t.Fatalf("expected at least 30 registered environment variables, found %d", len(vars))
	}
	seen := make(map[string]bool)
	for _, v := range vars {
		if v.Name == "" {
			t.Errorf("found environment variable with empty name")
		}
		if v.Description == "" {
			t.Errorf("variable %s has empty description", v.Name)
		}
		if seen[v.Name] {
			t.Errorf("duplicate environment variable registered: %s", v.Name)
		}
		seen[v.Name] = true
	}
}

// TestEnv_NoRawGetenvInProductionCode: Vet-style test that fails when any
// non-test Go code in cmd/, pkg/, or internal/ (outside internal/env itself)
// calls os.Getenv or os.LookupEnv with a BV_* or BEADS_* variable directly
// instead of using internal/env.
func TestEnv_NoRawGetenvInProductionCode(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := gotok.NewFileSet()

	var violations []string

	for _, dir := range []string{"cmd", "pkg", "internal"} {
		searchDir := filepath.Join(root, dir)
		err := filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				// Skip internal/env itself and vendor
				if filepath.Clean(path) == filepath.Clean(filepath.Join(root, "internal", "env")) ||
					strings.Contains(path, "vendor") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			node, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}

			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				// Check for os.Getenv(...) or os.LookupEnv(...)
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "os" {
					return true
				}
				if sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv" {
					return true
				}

				if len(call.Args) == 0 {
					return true
				}

				// Check argument: string literal starting with BV_ or BEADS_
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && isStringLit(lit.Kind) {
					rawVal := strings.Trim(lit.Value, `"`+"`")
					if strings.HasPrefix(rawVal, "BV_") || strings.HasPrefix(rawVal, "BEADS_") {
						pos := fset.Position(call.Pos())
						violations = append(violations, pos.String()+": raw os."+sel.Sel.Name+"("+lit.Value+") must use internal/env")
					}
				}

				return true
			})

			return nil
		})

		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}

	if len(violations) > 0 {
		t.Errorf("found %d raw os.Getenv/LookupEnv call(s) for BV_*/BEADS_* outside internal/env:\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

func isStringLit(k gotok.Token) bool {
	return k == gotok.STRING
}
