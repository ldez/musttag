package musttag

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	inspectpass "golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/types/typeutil"
)

var Analyzer = &analysis.Analyzer{
	Name:     "musttag",
	Doc:      "check if struct fields used in Marshal/Unmarshal are annotated with the relevant tag",
	Requires: []*analysis.Analyzer{inspectpass.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	inspect := pass.ResultOf[inspectpass.Analyzer].(*inspector.Inspector)

	filter := []ast.Node{
		(*ast.CallExpr)(nil),
	}

	// do not report the same struct more than once.
	type report struct {
		pos token.Pos
		tag string
	}
	reported := make(map[report]struct{})

	inspect.Preorder(filter, func(n ast.Node) {
		call := n.(*ast.CallExpr)

		tag, expr, ok := tagAndExpr(pass, call)
		if !ok {
			return
		}

		s, pos, ok := structAndPos(pass, expr)
		if !ok {
			return
		}

		if ok := checkStruct(s, tag, &pos); ok {
			return
		}

		r := report{pos, tag}
		if _, ok := reported[r]; !ok {
			reported[r] = struct{}{}
			pass.Reportf(pos, "exported fields should be annotated with the %q tag", tag)
		}
	})

	return nil, nil
}

// tagAndExpr analyses the given function call and returns the struct tag to
// look for and the expression that likely contains the struct to check.
func tagAndExpr(pass *analysis.Pass, call *ast.CallExpr) (string, ast.Expr, bool) {
	const (
		jsonTag = "json"
	)

	fn := typeutil.StaticCallee(pass.TypesInfo, call)
	if fn == nil {
		return "", nil, false
	}

	switch fn.FullName() {
	case "encoding/json.Marshal",
		"encoding/json.MarshalIndent",
		"(*encoding/json.Encoder).Encode",
		"(*encoding/json.Decoder).Decode":
		return jsonTag, call.Args[0], true
	case "encoding/json.Unmarshal":
		return jsonTag, call.Args[1], true
	default:
		return "", nil, false
	}
}

// structAndPos analyses the given expression and returns the struct to check
// and the position to report if needed.
func structAndPos(pass *analysis.Pass, expr ast.Expr) (*types.Struct, token.Pos, bool) {
	t := pass.TypesInfo.TypeOf(expr)
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}

	switch t := t.(type) {
	case *types.Named: // named type
		s, ok := t.Underlying().(*types.Struct)
		if ok {
			return s, t.Obj().Pos(), true
		}

	case *types.Struct: // anonymous struct
		if unary, ok := expr.(*ast.UnaryExpr); ok {
			expr = unary.X // &x
		}
		//nolint:gocritic // commentedOutCode: these are examples
		switch arg := expr.(type) {
		case *ast.Ident: // var x struct{}; json.Marshal(x)
			return t, arg.Obj.Pos(), true
		case *ast.CompositeLit: // json.Marshal(struct{}{})
			return t, arg.Pos(), true
		}
	}

	return nil, 0, false
}

// checkStruct checks that exported fields of the given struct are annotated
// with the tag and updates the position to report in case a nested struct of a
// named type is found.
func checkStruct(s *types.Struct, tag string, pos *token.Pos) (ok bool) {
	for i := 0; i < s.NumFields(); i++ {
		if !s.Field(i).Exported() {
			continue
		}

		tagged := false
		for _, t := range strings.Split(s.Tag(i), " ") {
			// from the [reflect.StructTag] docs:
			// By convention, tag strings are a concatenation
			// of optionally space-separated key:"value" pairs.
			if strings.HasPrefix(t, tag+":") {
				tagged = true
			}
		}
		if !tagged {
			return false
		}

		// check if the field is a nested struct.
		t := s.Field(i).Type()
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		nested, ok := t.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		if ok := checkStruct(nested, tag, pos); ok {
			continue
		}
		// update the position to point to the named type.
		if named, ok := t.(*types.Named); ok {
			*pos = named.Obj().Pos()
		}
		return false
	}

	return true
}
