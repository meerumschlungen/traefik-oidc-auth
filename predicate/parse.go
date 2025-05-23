/*
This file has been modified to remove the dependency on github.com/gravitational/trace, which doesn't work with yaegi.
*/

package predicate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
)

func NewParser(d Def) (Parser, error) {
	return &predicateParser{d: d}, nil
}

type predicateParser struct {
	d Def
}

func (p *predicateParser) Parse(in string) (interface{}, error) {
	expr, err := parser.ParseExpr(in)
	if err != nil {
		return nil, err
	}

	return p.parse(expr)
}

func (p *predicateParser) parse(expr ast.Expr) (interface{}, error) {
	switch n := expr.(type) {
	case *ast.BinaryExpr:
		x, err := p.parse(n.X)
		if err != nil {
			return nil, err
		}

		y, err := p.parse(n.Y)
		if err != nil {
			return nil, err
		}

		return p.joinPredicates(n.Op, x, y)

	case *ast.ParenExpr:
		return p.parse(n.X)

	case *ast.UnaryExpr:
		joinFn, err := p.getJoinFunction(n.Op)
		if err != nil {
			return nil, err
		}

		node, err := p.parse(n.X)
		if err != nil {
			return nil, err
		}

		return callFunction(joinFn, []interface{}{node})

	default:
		return p.evaluateExpr(n)
	}
}

func (p *predicateParser) evaluateArguments(nodes []ast.Expr) ([]interface{}, error) {
	out := make([]interface{}, len(nodes))
	for i, n := range nodes {
		val, err := p.evaluateExpr(n)
		if err != nil {
			return nil, err
		}
		out[i] = val
	}
	return out, nil
}

func (p *predicateParser) evaluateExpr(n ast.Expr) (interface{}, error) {
	switch l := n.(type) {
	case *ast.BasicLit:
		val, err := literalToValue(l)
		if err != nil {
			return nil, err
		}

		return val, nil

	case *ast.IndexExpr:
		if p.d.GetProperty == nil {
			return nil, fmt.Errorf("properties are not supported")
		}

		mapVal, err := p.evaluateExpr(l.X)
		if err != nil {
			return nil, err
		}

		keyVal, err := p.evaluateExpr(l.Index)
		if err != nil {
			return nil, err
		}

		val, err := p.d.GetProperty(mapVal, keyVal)
		if err != nil {
			return nil, err
		}

		return val, nil

	case *ast.SelectorExpr:
		fields, err := evaluateSelector(l, []string{})
		if err != nil {
			return nil, err
		}

		if p.d.GetIdentifier == nil {
			return nil, fmt.Errorf("%v is not defined", strings.Join(fields, "."))
		}

		val, err := p.d.GetIdentifier(fields)
		if err != nil {
			return nil, err
		}
		return val, nil

	case *ast.Ident:
		if p.d.GetIdentifier == nil {
			return nil, fmt.Errorf("%v is not defined", l.Name)
		}

		val, err := p.d.GetIdentifier([]string{l.Name})
		if err != nil {
			return nil, err
		}
		return val, nil

	case *ast.CallExpr:
		name, err := getIdentifier(l.Fun)
		if err != nil {
			return nil, err
		}

		fn, err := p.getFunction(name)
		if err != nil {
			return nil, err
		}

		arguments, err := p.evaluateArguments(l.Args)
		if err != nil {
			return nil, err
		}

		return callFunction(fn, arguments)

	default:
		return nil, fmt.Errorf("%T is not supported", n)
	}
}

// evaluateSelector recursively evaluates the selector field and returns a list
// of properties at the end.
func evaluateSelector(sel *ast.SelectorExpr, fields []string) ([]string, error) {
	fields = append([]string{sel.Sel.Name}, fields...)
	switch l := sel.X.(type) {
	case *ast.SelectorExpr:
		return evaluateSelector(l, fields)

	case *ast.Ident:
		fields = append([]string{l.Name}, fields...)
		return fields, nil

	default:
		return nil, fmt.Errorf("unsupported selector type: %T", l)
	}
}

func (p *predicateParser) getFunction(name string) (interface{}, error) {
	v, ok := p.d.Functions[name]
	if !ok {
		return nil, fmt.Errorf("unsupported function: %s", name)
	}
	return v, nil
}

func (p *predicateParser) joinPredicates(op token.Token, a, b interface{}) (interface{}, error) {
	joinFn, err := p.getJoinFunction(op)
	if err != nil {
		return nil, err
	}

	return callFunction(joinFn, []interface{}{a, b})
}

func (p *predicateParser) getJoinFunction(op token.Token) (interface{}, error) {
	var fn interface{}
	switch op {
	case token.NOT:
		fn = p.d.Operators.NOT
	case token.LAND:
		fn = p.d.Operators.AND
	case token.LOR:
		fn = p.d.Operators.OR
	case token.GTR:
		fn = p.d.Operators.GT
	case token.GEQ:
		fn = p.d.Operators.GE
	case token.LSS:
		fn = p.d.Operators.LT
	case token.LEQ:
		fn = p.d.Operators.LE
	case token.EQL:
		fn = p.d.Operators.EQ
	case token.NEQ:
		fn = p.d.Operators.NEQ
	}
	if fn == nil {
		return nil, fmt.Errorf("%v is not supported", op)
	}
	return fn, nil
}

func getIdentifier(node ast.Node) (string, error) {
	sexpr, ok := node.(*ast.SelectorExpr)
	if ok {
		id, okIdent := sexpr.X.(*ast.Ident)
		if !okIdent {
			return "", fmt.Errorf("expected selector identifier, got: %T", sexpr.X)
		}
		return fmt.Sprintf("%s.%s", id.Name, sexpr.Sel.Name), nil
	}

	id, ok := node.(*ast.Ident)
	if !ok {
		return "", fmt.Errorf("expected identifier, got: %T", node)
	}
	return id.Name, nil
}

func literalToValue(a *ast.BasicLit) (interface{}, error) {
	switch a.Kind {
	case token.FLOAT:
		value, err := strconv.ParseFloat(a.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse argument: %s, error: %s", a.Value, err)
		}
		return value, nil

	case token.INT:
		value, err := strconv.Atoi(a.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse argument: %s, error: %s", a.Value, err)
		}
		return value, nil

	case token.STRING:
		value, err := strconv.Unquote(a.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse argument: %s, error: %s", a.Value, err)
		}
		return value, nil
	}

	return nil, fmt.Errorf("unsupported function argument type: '%v'", a.Kind)
}

func callFunction(f interface{}, args []interface{}) (v interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s", r)
		}
	}()

	arguments := make([]reflect.Value, len(args))
	for i, a := range args {
		arguments[i] = reflect.ValueOf(a)
	}

	fn := reflect.ValueOf(f)

	ret := fn.Call(arguments)
	switch len(ret) {
	case 1:
		return ret[0].Interface(), nil

	case 2:
		v, e := ret[0].Interface(), ret[1].Interface()
		if e == nil {
			return v, nil
		}
		err, ok := e.(error)
		if !ok {
			return nil, fmt.Errorf("expected error as a second return value, got %T", e)
		}
		return v, err

	default:
		return nil, fmt.Errorf("expected at least one return argument for '%v'", fn)
	}
}
