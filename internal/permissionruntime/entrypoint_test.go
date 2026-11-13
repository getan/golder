package permissionruntime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// This is a startup contract test, not a string snapshot: both real entrypoints
// must call the same initializer, and neither may reimplement grants.Open.
func TestCLIAndGUIHaveTheSamePermissionBootstrap(t *testing.T) {
	for _, path := range []string{"cmd/golder/main.go", "gui/cmd/golder-gui/main.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("../..", path), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		initialize := 0
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "grants" && selector.Sel.Name == "Open" {
				t.Errorf("%s duplicates permission bootstrap", path)
			}
			if pkg.Name == "permissionruntime" && selector.Sel.Name == "Initialize" {
				initialize++
			}
			return true
		})
		if initialize != 1 {
			t.Errorf("%s: shared initializer called %d times", path, initialize)
		}
	}
}
