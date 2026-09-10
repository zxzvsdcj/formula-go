package engine

import (
	"fmt"
	"math"
	"sort"

	formulaerrors "github.com/DTrader-store/formula-go/errors"
	"github.com/DTrader-store/formula-go/interpreter"
	"github.com/DTrader-store/formula-go/parser/ast"
	"github.com/DTrader-store/formula-go/types"
)

// StreamingSession owns the mutable state for caller-driven streaming formula execution.
// It is not safe for concurrent use.
type StreamingSession struct {
	program     *ast.Program
	functions   *interpreter.FunctionRegistry
	history     []*types.MarketData
	drawings    map[string]*types.DrawingEvent
	bulkWarmup  int
	incremental *interpreter.IncrementalProgram
}

// NewStreamingSession creates a streaming session from a compiled program and optional seed history.
func (e *FormulaEngine) NewStreamingSession(program *ast.Program, seed []*types.MarketData) (*StreamingSession, error) {
	if program == nil {
		return nil, formulaerrors.NewRuntimeError("program must not be nil")
	}
	programCopy, err := cloneProgram(program)
	if err != nil {
		return nil, err
	}
	functions := e.functions.Clone()
	if err := rejectFutureReferences(programCopy, functions); err != nil {
		return nil, err
	}

	history, err := cloneAndValidateMarketData(seed)
	if err != nil {
		return nil, err
	}

	session := &StreamingSession{
		program:   programCopy,
		functions: functions,
		history:   history,
		drawings:  make(map[string]*types.DrawingEvent),
	}
	if warmup, ok := functions.StreamingBulkWarmup(programCopy); ok {
		session.bulkWarmup = warmup
	}
	session.incremental = functions.CompileIncremental(programCopy)
	if len(history) > 0 {
		if session.incremental != nil && len(history) >= session.incremental.Warmup() {
			if err := session.incremental.Seed(history); err != nil {
				return nil, err
			}
			return session, nil
		}
		execution, err := session.execute(history)
		if err != nil {
			return nil, err
		}
		session.drawings = cloneDrawingSet(execution.Drawings)
	}
	return session, nil
}

// NewStreamingSessionFromSource compiles formula source once and creates a streaming session.
func (e *FormulaEngine) NewStreamingSessionFromSource(formula string, seed []*types.MarketData) (*StreamingSession, error) {
	program, err := e.Compile(formula)
	if err != nil {
		return nil, err
	}
	return e.NewStreamingSession(program, seed)
}

// Len returns the number of retained bars.
func (s *StreamingSession) Len() int {
	if s == nil {
		return 0
	}
	return len(s.history)
}

// History returns a read-only snapshot copy of the retained market data.
func (s *StreamingSession) History() []*types.MarketData {
	if s == nil {
		return nil
	}
	return cloneMarketDataSlice(s.history)
}

// Append adds one bar and returns the latest-bar result.
func (s *StreamingSession) Append(data *types.MarketData) (*types.StreamingResult, error) {
	results, err := s.AppendMany([]*types.MarketData{data})
	if err != nil {
		return nil, err
	}
	return results[0], nil
}

// AppendMany atomically appends all bars in order and returns one latest-bar result per bar.
func (s *StreamingSession) AppendMany(data []*types.MarketData) ([]*types.StreamingResult, error) {
	if s == nil {
		return nil, formulaerrors.NewRuntimeError("streaming session must not be nil")
	}
	if len(data) == 0 {
		return []*types.StreamingResult{}, nil
	}

	incoming, err := cloneAndValidateMarketData(data)
	if err != nil {
		return nil, err
	}
	if s.incrementalReady() {
		return s.appendIncremental(incoming)
	}

	candidateHistory := cloneMarketDataSlice(s.history)
	candidateDrawings := cloneDrawingSet(s.drawings)
	candidateIncremental := s.incremental
	results := make([]*types.StreamingResult, 0, len(incoming))

	for i, item := range incoming {
		candidateHistory = append(candidateHistory, cloneMarketData(item))
		if candidateIncremental != nil && len(candidateHistory) >= candidateIncremental.Warmup() {
			candidateIncremental = candidateIncremental.Clone()
			if err := candidateIncremental.Seed(candidateHistory); err != nil {
				return nil, err
			}
			results = append(results, candidateIncremental.Latest())
			for _, remaining := range incoming[i+1:] {
				result, err := candidateIncremental.Append(remaining)
				if err != nil {
					return nil, err
				}
				candidateHistory = append(candidateHistory, remaining)
				results = append(results, result)
			}
			break
		}
		if s.bulkWarmup > 0 && len(candidateHistory) >= s.bulkWarmup && i+1 < len(incoming) {
			startBar := len(candidateHistory) - 1
			candidateHistory = append(candidateHistory, incoming[i+1:]...)
			interp := interpreter.NewInterpreterWithRegistry(candidateHistory, s.functions)
			bulkResults, err := interp.ExecuteStreamingRange(s.program, startBar)
			if err != nil {
				return nil, err
			}
			results = append(results, bulkResults...)
			// Only numeric programs qualify, so there is no drawing state to diff.
			break
		}
		execution, err := s.execute(candidateHistory)
		if err != nil {
			return nil, err
		}
		result := cloneStreamingResult(execution.Result)
		result.Drawings = diffDrawingSets(candidateDrawings, execution.Drawings)
		results = append(results, result)
		candidateDrawings = cloneDrawingSet(execution.Drawings)
	}

	s.incremental = candidateIncremental
	s.history = candidateHistory
	s.drawings = candidateDrawings
	return results, nil
}

// UpdateCurrent replaces the current unfinished bar without changing history length.
func (s *StreamingSession) UpdateCurrent(data *types.MarketData) (*types.StreamingResult, error) {
	if s == nil {
		return nil, formulaerrors.NewRuntimeError("streaming session must not be nil")
	}
	if len(s.history) == 0 {
		return nil, formulaerrors.NewRuntimeError("update-current requires at least one bar")
	}
	if data == nil {
		return nil, formulaerrors.NewRuntimeError("market data must not be nil")
	}
	item := cloneMarketData(data)
	if err := item.Validate(); err != nil {
		return nil, formulaerrors.NewRuntimeError(fmt.Sprintf("invalid market data: %v", err))
	}
	if s.incrementalReady() {
		result, err := s.incremental.UpdateCurrent(item)
		if err != nil {
			return nil, err
		}
		s.history[len(s.history)-1] = item
		return result, nil
	}

	candidateHistory := cloneMarketDataSlice(s.history)
	candidateHistory[len(candidateHistory)-1] = item
	execution, err := s.execute(candidateHistory)
	if err != nil {
		return nil, err
	}

	result := cloneStreamingResult(execution.Result)
	result.Drawings = diffDrawingSets(s.drawings, execution.Drawings)
	s.history = candidateHistory
	s.drawings = cloneDrawingSet(execution.Drawings)
	return result, nil
}

func (s *StreamingSession) execute(history []*types.MarketData) (*interpreter.StreamingExecution, error) {
	interp := interpreter.NewInterpreterWithRegistry(history, s.functions)
	return interp.ExecuteStreaming(s.program)
}

func (s *StreamingSession) incrementalReady() bool {
	return s.incremental != nil && s.incremental.Len() == len(s.history) && len(s.history) >= s.incremental.Warmup()
}

func (s *StreamingSession) appendIncremental(incoming []*types.MarketData) ([]*types.StreamingResult, error) {
	candidate := s.incremental
	if len(incoming) > 1 {
		// Copy only bounded indicator state, so a later bar failing rolls back
		// the entire batch without copying the retained market history.
		candidate = candidate.Clone()
	}
	results := make([]*types.StreamingResult, 0, len(incoming))
	for _, data := range incoming {
		result, err := candidate.Append(data)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	s.incremental = candidate
	s.history = append(s.history, incoming...)
	return results, nil
}

func cloneAndValidateMarketData(data []*types.MarketData) ([]*types.MarketData, error) {
	result := make([]*types.MarketData, len(data))
	for i, item := range data {
		if item == nil {
			return nil, formulaerrors.NewRuntimeError(fmt.Sprintf("market data at index %d is nil", i))
		}
		clone := cloneMarketData(item)
		if err := clone.Validate(); err != nil {
			return nil, formulaerrors.NewRuntimeError(fmt.Sprintf("invalid market data at index %d: %v", i, err))
		}
		result[i] = clone
	}
	return result, nil
}

func cloneMarketDataSlice(data []*types.MarketData) []*types.MarketData {
	if data == nil {
		return nil
	}
	result := make([]*types.MarketData, len(data))
	for i, item := range data {
		result[i] = cloneMarketData(item)
	}
	return result
}

func cloneMarketData(data *types.MarketData) *types.MarketData {
	if data == nil {
		return nil
	}
	copy := *data
	return &copy
}

func cloneProgram(program *ast.Program) (*ast.Program, error) {
	if program == nil {
		return nil, formulaerrors.NewRuntimeError("program must not be nil")
	}
	body := make([]ast.Statement, len(program.Body))
	for i, stmt := range program.Body {
		clone, err := cloneStatement(stmt)
		if err != nil {
			return nil, err
		}
		body[i] = clone
	}
	return &ast.Program{Body: body}, nil
}

func cloneStatement(stmt ast.Statement) (ast.Statement, error) {
	switch node := stmt.(type) {
	case *ast.VariableDeclaration:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil variable declaration")
		}
		value, err := cloneExpression(node.Value)
		if err != nil {
			return nil, err
		}
		return &ast.VariableDeclaration{Name: node.Name, Value: value}, nil
	case *ast.OutputDeclaration:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil output declaration")
		}
		value, err := cloneExpression(node.Value)
		if err != nil {
			return nil, err
		}
		return &ast.OutputDeclaration{Name: node.Name, Value: value, Style: cloneDrawingStyle(node.Style)}, nil
	case *ast.ExpressionStatement:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil expression statement")
		}
		expr, err := cloneExpression(node.Expr)
		if err != nil {
			return nil, err
		}
		return &ast.ExpressionStatement{Expr: expr}, nil
	default:
		return nil, formulaerrors.NewRuntimeError(fmt.Sprintf("unsupported statement type in streaming program: %T", stmt))
	}
}

func cloneExpression(expr ast.Expression) (ast.Expression, error) {
	switch node := expr.(type) {
	case *ast.Identifier:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil identifier")
		}
		return &ast.Identifier{Name: node.Name}, nil
	case *ast.NumberLiteral:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil number literal")
		}
		return &ast.NumberLiteral{Value: node.Value}, nil
	case *ast.StringLiteral:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil string literal")
		}
		return &ast.StringLiteral{Value: node.Value, External: node.External}, nil
	case *ast.FunctionCall:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil function call")
		}
		arguments := make([]ast.Expression, len(node.Arguments))
		for i, argument := range node.Arguments {
			clone, err := cloneExpression(argument)
			if err != nil {
				return nil, err
			}
			arguments[i] = clone
		}
		return &ast.FunctionCall{Name: node.Name, Arguments: arguments}, nil
	case *ast.BinaryExpression:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil binary expression")
		}
		left, err := cloneExpression(node.Left)
		if err != nil {
			return nil, err
		}
		right, err := cloneExpression(node.Right)
		if err != nil {
			return nil, err
		}
		return &ast.BinaryExpression{Left: left, Operator: node.Operator, Right: right}, nil
	case *ast.UnaryExpression:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil unary expression")
		}
		operand, err := cloneExpression(node.Operand)
		if err != nil {
			return nil, err
		}
		return &ast.UnaryExpression{Operator: node.Operator, Operand: operand}, nil
	case *ast.ConditionalExpression:
		if node == nil {
			return nil, formulaerrors.NewRuntimeError("program contains a nil conditional expression")
		}
		test, err := cloneExpression(node.Test)
		if err != nil {
			return nil, err
		}
		consequent, err := cloneExpression(node.Consequent)
		if err != nil {
			return nil, err
		}
		alternate, err := cloneExpression(node.Alternate)
		if err != nil {
			return nil, err
		}
		return &ast.ConditionalExpression{Test: test, Consequent: consequent, Alternate: alternate}, nil
	default:
		return nil, formulaerrors.NewRuntimeError(fmt.Sprintf("unsupported expression type in streaming program: %T", expr))
	}
}

func cloneDrawingStyle(style *ast.DrawingStyle) *ast.DrawingStyle {
	if style == nil {
		return nil
	}
	clone := &ast.DrawingStyle{Hidden: style.Hidden}
	if style.Color != nil {
		value := *style.Color
		clone.Color = &value
	}
	if style.LineWidth != nil {
		value := *style.LineWidth
		clone.LineWidth = &value
	}
	if style.LineStyle != nil {
		value := *style.LineStyle
		clone.LineStyle = &value
	}
	if style.DrawMethod != nil {
		value := *style.DrawMethod
		clone.DrawMethod = &value
	}
	return clone
}

func rejectFutureReferences(program *ast.Program, functions *interpreter.FunctionRegistry) error {
	for _, stmt := range program.Body {
		if err := rejectFutureReferencesInStatement(stmt, functions); err != nil {
			return err
		}
	}
	return nil
}

func rejectFutureReferencesInStatement(stmt ast.Statement, functions *interpreter.FunctionRegistry) error {
	switch node := stmt.(type) {
	case *ast.VariableDeclaration:
		return rejectFutureReferencesInExpression(node.Value, functions)
	case *ast.OutputDeclaration:
		return rejectFutureReferencesInExpression(node.Value, functions)
	case *ast.ExpressionStatement:
		return rejectFutureReferencesInExpression(node.Expr, functions)
	default:
		return nil
	}
}

func rejectFutureReferencesInExpression(expr ast.Expression, functions *interpreter.FunctionRegistry) error {
	switch node := expr.(type) {
	case *ast.FunctionCall:
		if functions.IsStreamingFutureReference(node.Name) {
			return formulaerrors.NewRuntimeError(fmt.Sprintf("unsupported future-reference function in streaming session: %s", node.Name))
		}
		for _, arg := range node.Arguments {
			if err := rejectFutureReferencesInExpression(arg, functions); err != nil {
				return err
			}
		}
	case *ast.BinaryExpression:
		if err := rejectFutureReferencesInExpression(node.Left, functions); err != nil {
			return err
		}
		return rejectFutureReferencesInExpression(node.Right, functions)
	case *ast.UnaryExpression:
		return rejectFutureReferencesInExpression(node.Operand, functions)
	case *ast.ConditionalExpression:
		if err := rejectFutureReferencesInExpression(node.Test, functions); err != nil {
			return err
		}
		if err := rejectFutureReferencesInExpression(node.Consequent, functions); err != nil {
			return err
		}
		return rejectFutureReferencesInExpression(node.Alternate, functions)
	}
	return nil
}

func diffDrawingSets(previous, next map[string]*types.DrawingEvent) []*types.DrawingChange {
	keys := make([]string, 0, len(previous)+len(next))
	seen := make(map[string]struct{}, len(previous)+len(next))
	for key := range previous {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range next {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	changes := make([]*types.DrawingChange, 0)
	for _, key := range keys {
		previousEvent, hadPrevious := previous[key]
		nextEvent, hasNext := next[key]
		switch {
		case !hadPrevious && hasNext:
			changes = append(changes, &types.DrawingChange{Action: types.DrawingAdd, Key: key, Event: clonePublicDrawingEvent(nextEvent)})
		case hadPrevious && !hasNext:
			changes = append(changes, &types.DrawingChange{Action: types.DrawingRemove, Key: key, Event: clonePublicDrawingEvent(previousEvent)})
		case hadPrevious && hasNext && !drawingEventsEqual(previousEvent, nextEvent):
			changes = append(changes, &types.DrawingChange{Action: types.DrawingReplace, Key: key, Event: clonePublicDrawingEvent(nextEvent)})
		}
	}
	return changes
}

func drawingEventsEqual(left, right *types.DrawingEvent) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.Function != right.Function || left.BarIndex != right.BarIndex || left.Text != right.Text {
		return false
	}
	if len(left.Values) != len(right.Values) || len(left.Meta) != len(right.Meta) {
		return false
	}
	for key, leftValue := range left.Values {
		rightValue, exists := right.Values[key]
		if !exists || !floatEqual(leftValue, rightValue) {
			return false
		}
	}
	for key, leftValue := range left.Meta {
		if right.Meta[key] != leftValue {
			return false
		}
	}
	return true
}

func floatEqual(left, right float64) bool {
	return left == right || (math.IsNaN(left) && math.IsNaN(right))
}

func cloneDrawingSet(drawings map[string]*types.DrawingEvent) map[string]*types.DrawingEvent {
	result := make(map[string]*types.DrawingEvent, len(drawings))
	for key, event := range drawings {
		result[key] = clonePublicDrawingEvent(event)
	}
	return result
}

func cloneStreamingResult(result *types.StreamingResult) *types.StreamingResult {
	if result == nil {
		return nil
	}
	copy := &types.StreamingResult{
		BarIndex:  result.BarIndex,
		Outputs:   make([]*types.LatestOutput, len(result.Outputs)),
		Variables: make(map[string]float64, len(result.Variables)),
		Drawings:  make([]*types.DrawingChange, len(result.Drawings)),
	}
	for i, output := range result.Outputs {
		if output != nil {
			copy.Outputs[i] = &types.LatestOutput{Name: output.Name, Value: output.Value, Style: clonePublicLineStyle(output.Style)}
		}
	}
	for key, value := range result.Variables {
		copy.Variables[key] = value
	}
	for i, change := range result.Drawings {
		if change != nil {
			copy.Drawings[i] = &types.DrawingChange{Action: change.Action, Key: change.Key, Event: clonePublicDrawingEvent(change.Event)}
		}
	}
	return copy
}

func clonePublicLineStyle(style *types.LineStyle) *types.LineStyle {
	if style == nil {
		return nil
	}
	copy := *style
	return &copy
}

func clonePublicDrawingEvent(event *types.DrawingEvent) *types.DrawingEvent {
	if event == nil {
		return nil
	}
	copy := &types.DrawingEvent{Function: event.Function, BarIndex: event.BarIndex, Text: event.Text}
	if event.Values != nil {
		copy.Values = make(map[string]float64, len(event.Values))
		for key, value := range event.Values {
			copy.Values[key] = value
		}
	}
	if event.Meta != nil {
		copy.Meta = make(map[string]string, len(event.Meta))
		for key, value := range event.Meta {
			copy.Meta[key] = value
		}
	}
	return copy
}
