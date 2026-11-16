package strategy

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// AllowedImports are the only packages strategy code may import. None of
// them reach the network, files, processes, randomness or orders.
var AllowedImports = []string{
	"cmp", "errors", "fmt", "math", "math/big", "slices", "sort", "strconv", "strings", "time",
	"crypto/sha256", "encoding/hex", "encoding/json", "io",
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata",
	"git.subcult.tv/subculture-collective/mantik/internal/indicators",
	"git.subcult.tv/subculture-collective/mantik/internal/options",
	"git.subcult.tv/subculture-collective/mantik/internal/strategy",
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library",
}

// clockFuncs read or wait on the wall clock. Strategies use View.At instead.
var clockFuncs = []string{"Now", "Since", "Until", "Sleep", "After", "AfterFunc", "Tick", "NewTicker", "NewTimer"}

// Audit checks the non-test Go files of one strategy package directory. It
// refuses any import outside AllowedImports and any call to the time
// package's clock functions, so a strategy cannot use the network, place
// orders or read the clock.
func Audit(dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return err
	}
	problems := []string{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		timeNames := map[string]bool{}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !slices.Contains(AllowedImports, p) {
				problems = append(problems, fmt.Sprintf("%s imports %s", fset.Position(imp.Pos()), p))
				continue
			}
			if p != "time" {
				continue
			}
			name := "time"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if name == "." {
				problems = append(problems, fmt.Sprintf("%s dot-imports time", fset.Position(imp.Pos())))
			}
			timeNames[name] = true
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && timeNames[pkg.Name] && slices.Contains(clockFuncs, sel.Sel.Name) {
				problems = append(problems, fmt.Sprintf("%s uses time.%s", fset.Position(sel.Pos()), sel.Sel.Name))
			}
			return true
		})
	}
	if len(problems) > 0 {
		return fmt.Errorf("strategy code in %s: %s", dir, strings.Join(problems, "; "))
	}
	return nil
}
