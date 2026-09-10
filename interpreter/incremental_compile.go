package interpreter

import (
	"fmt"
	"math"
	"strings"

	"github.com/DTrader-store/formula-go/parser/ast"
	"github.com/DTrader-store/formula-go/types"
)

type incrementalKind uint8

const (
	incrementalConstant incrementalKind = iota
	incrementalField
	incrementalBinary
	incrementalUnary
	incrementalPointwise
	incrementalAverage
	incrementalSum
	incrementalReference
	incrementalCross
	incrementalExtrema
	incrementalWindow
	incrementalCount
)

// IncrementalProgram keeps bounded numeric state for supported causal formulas.
// It is owned by one streaming session and is not safe for concurrent use.
type IncrementalProgram struct {
	nodes   []incrementalNode
	outputs []incrementalOutput
	scalars []incrementalOutput
	warmup  int
	count   int
}

type incrementalOutput struct {
	name  string
	node  int
	style *types.LineStyle
}

type incrementalState struct {
	sum     float64
	missing int
	smooth  float64
}

type incrementalNode struct {
	kind     incrementalKind
	name     string
	array    bool
	inputs   []int
	operator ast.BinaryOperator
	period   int
	alpha    float64
	fn       Function
	args     []*Value // Reusable pointwise or window arguments; never passed to custom functions.
	lookback int
	values   []float64 // Ring indexed by absolute bar index.
	state    incrementalState
	before   incrementalState // State immediately before the current bar.
	next     incrementalState // Scratch state, committed only after all nodes succeed.
	value    float64          // Scratch value, or the immutable value of a constant.
}

// CompileIncremental returns nil when a program needs the general interpreter.
// The registry's builtin metadata excludes custom overrides from optimization.
// A compiled plan must be seeded once its Warmup length is available.
func (r *FunctionRegistry) CompileIncremental(program *ast.Program) *IncrementalProgram {
	warmup, ok := r.StreamingBulkWarmup(program)
	if !ok {
		return nil
	}
	p := &IncrementalProgram{warmup: warmup}
	variables := make(map[string]int)
	for _, aliases := range [][]string{
		{"OPEN", "O"}, {"CLOSE", "C"}, {"HIGH", "H"}, {"LOW", "L"},
		{"VOLUME", "V", "VOL"}, {"AMOUNT", "AMO"},
	} {
		id := p.addNode(incrementalNode{kind: incrementalField, name: aliases[0], array: true})
		for _, name := range aliases {
			variables[name] = id
		}
	}
	var visit func(ast.Expression) (int, bool)
	visit = func(expr ast.Expression) (int, bool) {
		switch node := expr.(type) {
		case *ast.NumberLiteral:
			return p.addNode(incrementalNode{value: node.Value}), true
		case *ast.Identifier:
			id, exists := variables[node.Name]
			return id, exists
		case *ast.UnaryExpression:
			id, valid := visit(node.Operand)
			if !valid || node.Operator != ast.OpUnaryMinus {
				return 0, false
			}
			if !p.nodes[id].array {
				return p.addNode(incrementalNode{value: -p.nodes[id].value}), true
			}
			return p.addNode(incrementalNode{kind: incrementalUnary, array: true, inputs: []int{id}}), true
		case *ast.BinaryExpression:
			left, leftOK := visit(node.Left)
			right, rightOK := visit(node.Right)
			if !leftOK || !rightOK {
				return 0, false
			}
			if !p.nodes[left].array && !p.nodes[right].array {
				value, err := (&Interpreter{}).binaryOpScalarScalar(node.Operator, p.nodes[left].value, p.nodes[right].value)
				return p.addNode(incrementalNode{value: value}), err == nil
			}
			if _, err := (&Interpreter{}).binaryOpScalarScalar(node.Operator, 1, 1); err != nil {
				return 0, false
			}
			return p.addNode(incrementalNode{kind: incrementalBinary, array: true, inputs: []int{left, right}, operator: node.Operator}), true
		case *ast.FunctionCall:
			inputs := make([]int, len(node.Arguments))
			for i, arg := range node.Arguments {
				id, valid := visit(arg)
				if !valid {
					return 0, false
				}
				inputs[i] = id
			}
			return p.compileFunction(strings.ToUpper(node.Name), inputs, r)
		default:
			return 0, false
		}
	}
	var userVars []string
	exprCount := 0
	for _, stmt := range program.Body {
		var name string
		var expr ast.Expression
		var style *types.LineStyle
		output := false
		switch node := stmt.(type) {
		case *ast.VariableDeclaration:
			name, expr = node.Name, node.Value
		case *ast.OutputDeclaration:
			name, expr, style, output = node.Name, node.Value, lineStyleFromAST(node.Style), true
		case *ast.ExpressionStatement:
			name, expr = fmt.Sprintf("__expr__%d", exprCount), node.Expr
			exprCount++
			if ident, ok := node.Expr.(*ast.Identifier); ok {
				name = ident.Name
			}
		default:
			return nil
		}
		id, valid := visit(expr)
		if !valid {
			return nil
		}
		variables[name] = id
		if output {
			p.outputs = append(p.outputs, incrementalOutput{name, id, style})
		} else {
			userVars = append(userVars, name)
		}
	}
	explicitOutputs := len(p.outputs) > 0
	for _, name := range userVars {
		id := variables[name]
		if !p.nodes[id].array {
			p.scalars = append(p.scalars, incrementalOutput{name: name, node: id})
		} else if !explicitOutputs {
			p.outputs = append(p.outputs, incrementalOutput{name: name, node: id})
		}
	}
	return p
}

func (p *IncrementalProgram) addNode(node incrementalNode) int {
	node.state.smooth = math.NaN()
	node.before = node.state
	p.nodes = append(p.nodes, node)
	return len(p.nodes) - 1
}

func (p *IncrementalProgram) compileFunction(name string, inputs []int, r *FunctionRegistry) (int, bool) {
	if !r.streamingFunctions[name].stablePrefixes {
		return 0, false
	}
	node := incrementalNode{name: name, inputs: inputs, array: true, fn: r.functions[name]}
	integer := func(i int) (int, bool) {
		if i >= len(inputs) || p.nodes[inputs[i]].array {
			return 0, false
		}
		value := p.nodes[inputs[i]].value
		if !isIntegerScalar(NewSingleValue(value)) {
			return 0, false
		}
		return int(value), true
	}
	series := func(i int) bool { return i < len(inputs) && p.nodes[inputs[i]].array }
	retain := func(i, lookback int) {
		input := &p.nodes[inputs[i]]
		input.lookback = max(input.lookback, lookback)
	}
	switch name {
	case "EMA", "SMA", "DMA":
		if name == "SMA" && len(inputs) == 2 {
			node.kind = incrementalSum
			break
		}
		if !series(0) || (name != "SMA" && len(inputs) != 2) || (name == "SMA" && len(inputs) != 3) {
			return 0, false
		}
		node.kind = incrementalAverage
		if name != "DMA" {
			n, valid := integer(1)
			if !valid || n <= 0 {
				return 0, false
			}
			node.alpha = 2 / (float64(n) + 1)
			if name == "SMA" {
				m, valid := integer(2)
				if !valid || m <= 0 || m > n {
					return 0, false
				}
				node.alpha = float64(m) / float64(n)
			}
		}
	case "MA", "SUM":
		node.kind = incrementalSum
	case "COUNT", "EVERY", "EXIST":
		node.kind = incrementalCount
	case "REF", "REFV":
		node.kind = incrementalReference
	case "HHV", "LLV", "HHVBARS", "LLVBARS":
		node.kind = incrementalExtrema
	case "STD", "STDP", "STDDEV", "VAR", "VARP", "DEVSQ", "FORCAST", "SLOPE", "AVEDEV", "WMA", "COVAR", "RELATE", "BETA":
		node.kind = incrementalWindow
	case "CROSS":
		if len(inputs) != 2 || !series(0) || !series(1) {
			return 0, false
		}
		node.kind = incrementalCross
		retain(0, 1)
		retain(1, 1)
	case "MAX", "MIN", "ABS", "SQRT", "POW", "EXP", "LN", "LOG", "MOD", "CEILING", "FLOOR",
		"INTPART", "FRACPART", "ROUND", "ROUND2", "SIGN", "SIN", "COS", "TAN", "ASIN", "ACOS", "ATAN",
		"IF", "IFF", "IFN", "NOT", "BETWEEN", "RANGE", "DRAWNULL":
		// Validate broadcasting and arity with one-element series. Scalar-only
		// results (including a constant IF branch) are folded into constants.
		args := make([]*Value, len(inputs))
		for i, id := range inputs {
			args[i] = NewSingleValue(p.nodes[id].value)
			if series(i) {
				args[i] = NewArrayValue([]float64{1})
			}
		}
		value, err := node.fn(args, nil)
		if err != nil || !isNumeric(value) {
			return 0, false
		}
		if !value.IsArray {
			return p.addNode(incrementalNode{value: value.Single}), true
		}
		node.kind = incrementalPointwise
	default:
		return 0, false
	}
	if node.kind == incrementalSum || node.kind == incrementalCount || node.kind == incrementalReference || node.kind == incrementalExtrema || node.kind == incrementalWindow {
		periodIndex := 1
		if name == "COVAR" || name == "RELATE" || name == "BETA" {
			periodIndex = 2
		}
		n, valid := integer(periodIndex)
		if len(inputs) != periodIndex+1 || !series(0) || (periodIndex == 2 && !series(1)) || !valid || n < 0 || (n == 0 && node.kind != incrementalReference) {
			return 0, false
		}
		node.period = n
		lookback := n
		if node.kind == incrementalWindow || node.kind == incrementalExtrema {
			lookback = n - 1
		}
		for i := 0; i < periodIndex; i++ {
			retain(i, lookback)
		}
	}
	node.args = make([]*Value, len(inputs))
	for i := range node.args {
		node.args[i] = NewSingleValue(0)
	}
	return p.addNode(node), true
}
