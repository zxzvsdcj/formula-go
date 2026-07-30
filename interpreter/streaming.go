package interpreter

import (
	"fmt"
	"math"
	"strings"

	"github.com/DTrader-store/formula-go/parser/ast"
	"github.com/DTrader-store/formula-go/types"
)

// StreamingExecution is the interpreter-level latest result plus the complete
// drawing state needed by engine.StreamingSession to diff mutation deltas.
type StreamingExecution struct {
	Result   *types.StreamingResult
	Drawings map[string]*types.DrawingEvent
}

// ExecuteStreaming executes a program using streaming warm-up semantics and
// returns latest-bar data without constructing a historical FormulaResult.
func (interp *Interpreter) ExecuteStreaming(program *ast.Program) (*StreamingExecution, error) {
	interp.streaming = true
	defer func() { interp.streaming = false }()

	interp.resetExecutionState()
	if err := interp.validateInputs(program); err != nil {
		return nil, err
	}

	interp.initMarketDataVariables()
	for _, stmt := range program.Body {
		if err := interp.executeStatement(stmt); err != nil {
			return nil, err
		}
	}

	return interp.buildStreamingExecution(), nil
}

func (interp *Interpreter) buildStreamingExecution() *StreamingExecution {
	barIndex := len(interp.marketData) - 1
	result := &types.StreamingResult{
		BarIndex:  barIndex,
		Outputs:   make([]*types.LatestOutput, 0),
		Variables: make(map[string]float64),
		Drawings:  make([]*types.DrawingChange, 0),
	}
	drawings := make(map[string]*types.DrawingEvent)

	addDrawings := func(source string, value *Value) {
		occurrences := make(map[string]int)
		for _, drawing := range value.Drawings {
			if drawing == nil {
				continue
			}
			occurrenceKey := fmt.Sprintf("%s:%d", drawing.Function, drawing.BarIndex)
			occurrence := occurrences[occurrenceKey]
			occurrences[occurrenceKey]++

			key := fmt.Sprintf("%s:%s:%d:%d", source, drawing.Function, drawing.BarIndex, occurrence)
			drawings[key] = cloneDrawingEvent(drawing)
		}
	}

	addValue := func(source, name string, value *Value, style *types.LineStyle, forceOutput bool) {
		if value == nil {
			return
		}
		if value.IsDraw {
			addDrawings(source, value)
			return
		}
		if value.IsArray || forceOutput {
			latest := value.Single
			if value.IsArray {
				latest = latestArrayValue(value.Array, barIndex)
			}
			result.Outputs = append(result.Outputs, &types.LatestOutput{
				Name:  name,
				Value: latest,
				Style: cloneLineStyle(style),
			})
			return
		}
		if !value.IsString {
			result.Variables[name] = value.Single
		}
	}

	if len(interp.outputs) > 0 {
		for i, output := range interp.outputs {
			addValue(fmt.Sprintf("output:%03d:%s", i, output.Name), output.Name, output.Value, output.Style, true)
		}
		for i, name := range interp.userVars {
			value := interp.variables[name]
			if value == nil {
				continue
			}
			if value.IsDraw {
				addValue(fmt.Sprintf("var:%03d:%s", i, name), name, value, nil, false)
			} else if !value.IsArray && !value.IsString {
				result.Variables[name] = value.Single
			}
		}
		return &StreamingExecution{Result: result, Drawings: drawings}
	}

	for i, name := range interp.userVars {
		addValue(fmt.Sprintf("var:%03d:%s", i, name), name, interp.variables[name], nil, false)
	}
	return &StreamingExecution{Result: result, Drawings: drawings}
}

func latestArrayValue(values []float64, index int) float64 {
	if index < 0 || index >= len(values) {
		return math.NaN()
	}
	return values[index]
}

func (r *FunctionRegistry) streamingWarmupValue(name string, args []*Value, historyLength int) (*Value, bool) {
	if r == nil {
		return nil, false
	}
	switch r.streamingFunctions[strings.ToUpper(name)].warmup {
	case streamingWarmupSingleSeries:
		return streamingWarmupSingleSeriesValue(args, historyLength)
	case streamingWarmupPairSeries:
		return streamingWarmupPairSeriesValue(args, historyLength)
	default:
		return nil, false
	}
}

func streamingWarmupSingleSeriesValue(args []*Value, historyLength int) (*Value, bool) {
	if len(args) != 2 {
		return nil, false
	}
	series, period := args[0], args[1]
	if series == nil || period == nil || !series.IsArray || period.IsArray || period.IsString || period.IsDraw {
		return nil, false
	}
	n := int(period.Single)
	if n <= 0 || n <= historyLength {
		return nil, false
	}
	return NewArrayValue(nanArray(historyLength)), true
}

func streamingWarmupPairSeriesValue(args []*Value, historyLength int) (*Value, bool) {
	if len(args) != 3 {
		return nil, false
	}
	left, right, period := args[0], args[1], args[2]
	if left == nil || right == nil || period == nil || !left.IsArray || !right.IsArray || len(left.Array) != len(right.Array) || period.IsArray || period.IsString || period.IsDraw {
		return nil, false
	}
	n := int(period.Single)
	if n <= 0 || n <= historyLength {
		return nil, false
	}
	return NewArrayValue(nanArray(historyLength)), true
}

func nanArray(length int) []float64 {
	result := make([]float64, length)
	for i := range result {
		result[i] = math.NaN()
	}
	return result
}

func cloneLineStyle(style *types.LineStyle) *types.LineStyle {
	if style == nil {
		return nil
	}
	copy := *style
	return &copy
}

func cloneDrawingEvent(event *types.DrawingEvent) *types.DrawingEvent {
	if event == nil {
		return nil
	}
	copy := &types.DrawingEvent{
		Function: event.Function,
		BarIndex: event.BarIndex,
		Text:     event.Text,
	}
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
