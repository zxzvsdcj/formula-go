package interpreter

import (
	"strings"

	"github.com/DTrader-store/formula-go/errors"
	"github.com/DTrader-store/formula-go/parser/ast"
	"github.com/DTrader-store/formula-go/types"
)

// StreamingBulkWarmup identifies numeric programs whose historical outputs are
// stable after a known warm-up length. It returns false for drawings, custom
// functions, noncausal functions and periods that cannot be resolved statically.
func (r *FunctionRegistry) StreamingBulkWarmup(program *ast.Program) (int, bool) {
	if r == nil || program == nil {
		return 0, false
	}
	variables := make(map[string]*Value)
	for _, name := range []string{"OPEN", "CLOSE", "HIGH", "LOW", "VOLUME", "AMOUNT", "O", "C", "H", "L", "V", "VOL", "AMO"} {
		variables[name] = nil // nil marks a series; scalars carry their constant value.
	}
	warmup := 1
	var visit func(ast.Expression) (*Value, bool)
	visit = func(expr ast.Expression) (*Value, bool) {
		switch node := expr.(type) {
		case *ast.NumberLiteral:
			return NewSingleValue(node.Value), true
		case *ast.Identifier:
			value, ok := variables[node.Name]
			return value, ok
		case *ast.UnaryExpression:
			value, ok := visit(node.Operand)
			if !ok || node.Operator != ast.OpUnaryMinus {
				return nil, false
			}
			if value == nil {
				return nil, true
			}
			return NewSingleValue(-value.Single), true
		case *ast.BinaryExpression:
			left, leftOK := visit(node.Left)
			right, rightOK := visit(node.Right)
			if !leftOK || !rightOK {
				return nil, false
			}
			if left == nil || right == nil {
				return nil, true
			}
			value, err := (&Interpreter{}).binaryOpScalarScalar(node.Operator, left.Single, right.Single)
			return NewSingleValue(value), err == nil
		case *ast.FunctionCall:
			metadata := r.streamingFunctions[strings.ToUpper(node.Name)]
			if !metadata.stablePrefixes {
				return nil, false
			}
			args := make([]*Value, len(node.Arguments))
			series := false
			for i, arg := range node.Arguments {
				value, ok := visit(arg)
				if !ok {
					return nil, false
				}
				args[i] = value
				series = series || value == nil
			}
			periodIndex := -1
			if metadata.warmup == streamingWarmupSingleSeries && len(args) == 2 {
				periodIndex = 1
			} else if metadata.warmup == streamingWarmupPairSeries && len(args) == 3 {
				periodIndex = 2
			}
			if periodIndex >= 0 {
				period := args[periodIndex]
				if !isIntegerScalar(period) || period.Single <= 0 {
					return nil, false
				}
				warmup = max(warmup, int(period.Single))
			}
			if series {
				return nil, true
			}
			value, err := r.Call(node.Name, args, nil)
			return value, err == nil && isNumericScalar(value)
		default:
			return nil, false
		}
	}
	for _, stmt := range program.Body {
		var name string
		var expr ast.Expression
		switch node := stmt.(type) {
		case *ast.VariableDeclaration:
			name, expr = node.Name, node.Value
		case *ast.OutputDeclaration:
			name, expr = node.Name, node.Value
		case *ast.ExpressionStatement:
			expr = node.Expr
		default:
			return 0, false
		}
		value, ok := visit(expr)
		if !ok {
			return 0, false
		}
		if name != "" {
			variables[name] = value
		}
	}
	return warmup, true
}

// ExecuteStreamingRange computes stable latest-bar responses from one full
// execution. Earlier warm-up prefixes must be replayed separately by the caller.
func (interp *Interpreter) ExecuteStreamingRange(program *ast.Program, startBar int) ([]*types.StreamingResult, error) {
	warmup, ok := interp.functions.StreamingBulkWarmup(program)
	if !ok || startBar < warmup-1 || startBar >= len(interp.marketData) {
		return nil, errors.NewRuntimeError("program or range does not support stable streaming prefixes")
	}
	if _, err := interp.ExecuteStreaming(program); err != nil {
		return nil, err
	}
	results := make([]*types.StreamingResult, 0, len(interp.marketData)-startBar)
	for bar := startBar; bar < len(interp.marketData); bar++ {
		results = append(results, interp.buildStreamingExecutionAt(bar).Result)
	}
	return results, nil
}
