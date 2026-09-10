package interpreter

import (
	"fmt"
	"math"

	"github.com/DTrader-store/formula-go/errors"
	"github.com/DTrader-store/formula-go/types"
)

// Warmup is the minimum history length at which this plan can take over from replay.
func (p *IncrementalProgram) Warmup() int { return p.warmup }

// Len is the number of bars incorporated in the plan's state.
func (p *IncrementalProgram) Len() int { return p.count }

// Clone copies mutable node state and retained windows for an atomic batch.
// Its cost depends on the formula's lookbacks, not the total retained history.
func (p *IncrementalProgram) Clone() *IncrementalProgram {
	clone := *p
	clone.nodes = append([]incrementalNode(nil), p.nodes...)
	for i := range clone.nodes {
		node := &clone.nodes[i]
		node.values = append([]float64(nil), node.values...)
		node.args = make([]*Value, len(node.args))
		for j, arg := range p.nodes[i].args {
			value := *arg
			value.Array = append([]float64(nil), arg.Array...)
			node.args[j] = &value
		}
	}
	return &clone
}

// Seed builds state from complete history using batch semantics. Streaming
// warm-up responses before Warmup must be produced by the general interpreter.
// A failed seed leaves the plan unchanged.
func (p *IncrementalProgram) Seed(history []*types.MarketData) error {
	if p.count != 0 || len(history) < p.warmup {
		return errors.NewRuntimeError("incremental seed requires an empty plan and completed warm-up")
	}
	candidate := p.Clone()
	for _, data := range history {
		if err := candidate.step(data, false); err != nil {
			return err
		}
	}
	*p = *candidate
	return nil
}

// Append evaluates and commits one bar. Errors do not change committed state.
func (p *IncrementalProgram) Append(data *types.MarketData) (*types.StreamingResult, error) {
	if p.count < p.warmup {
		return nil, errors.NewRuntimeError("incremental program requires seeded history")
	}
	if err := p.step(data, false); err != nil {
		return nil, err
	}
	return p.Latest(), nil
}

// UpdateCurrent evaluates against the state BEFORE the current bar, so repeated
// updates replace its contribution instead of accumulating it. Commit is atomic.
func (p *IncrementalProgram) UpdateCurrent(data *types.MarketData) (*types.StreamingResult, error) {
	if p.count < p.warmup {
		return nil, errors.NewRuntimeError("incremental program requires seeded history")
	}
	if err := p.step(data, true); err != nil {
		return nil, err
	}
	return p.Latest(), nil
}

// Latest returns an independent snapshot, including after a failed mutation.
func (p *IncrementalProgram) Latest() *types.StreamingResult {
	result := &types.StreamingResult{
		BarIndex:  p.count - 1,
		Outputs:   make([]*types.LatestOutput, 0, len(p.outputs)),
		Variables: make(map[string]float64, len(p.scalars)),
		Drawings:  make([]*types.DrawingChange, 0),
	}
	for _, output := range p.outputs {
		result.Outputs = append(result.Outputs, &types.LatestOutput{
			Name: output.name, Value: p.at(output.node, p.count-1, -1), Style: cloneLineStyle(output.style),
		})
	}
	for _, scalar := range p.scalars {
		result.Variables[scalar.name] = p.nodes[scalar.node].value
	}
	return result
}

func (p *IncrementalProgram) step(data *types.MarketData, update bool) error {
	if err := data.Validate(); err != nil {
		return errors.NewRuntimeError(fmt.Sprintf("invalid market data: %v", err))
	}
	index := p.count
	if update {
		index--
	}
	// Stage all nodes first. Rings and saved states are untouched until success.
	for i := range p.nodes {
		node := &p.nodes[i]
		node.next = node.state
		if update {
			node.next = node.before
		}
		value, err := p.evaluateNode(node, index, data)
		if err != nil {
			return err
		}
		node.value = value
	}
	for i := range p.nodes {
		node := &p.nodes[i]
		if !update {
			node.before = node.state
		}
		node.state = node.next
		if node.array {
			node.growWindow(index, p.count)
			node.values[index%len(node.values)] = node.value
		}
	}
	if !update {
		p.count++
	}
	return nil
}

func (node *incrementalNode) growWindow(index, count int) {
	needed := min(node.lookback, index) + 1
	if len(node.values) >= needed {
		return
	}
	size := max(needed, len(node.values)*2)
	// Avoid lookback+1 overflow for very large valid REF offsets.
	if size > 0 && size-1 > node.lookback {
		size = node.lookback + 1
	}
	values := make([]float64, size)
	for bar := count - len(node.values); bar < count; bar++ {
		values[bar%size] = node.values[bar%len(node.values)]
	}
	node.values = values
}

func (p *IncrementalProgram) at(id, index, current int) float64 {
	node := &p.nodes[id]
	if !node.array {
		return node.value
	}
	if index < 0 {
		return math.NaN()
	}
	if index == current {
		return node.value
	}
	if index < p.count-len(node.values) || index >= p.count {
		return math.NaN()
	}
	return node.values[index%len(node.values)]
}

func (p *IncrementalProgram) evaluateNode(node *incrementalNode, index int, data *types.MarketData) (float64, error) {
	input := func(i int) float64 { return p.nodes[node.inputs[i]].value }
	switch node.kind {
	case incrementalConstant:
		return node.value, nil
	case incrementalField:
		switch node.name {
		case "OPEN":
			return data.Open, nil
		case "CLOSE":
			return data.Close, nil
		case "HIGH":
			return data.High, nil
		case "LOW":
			return data.Low, nil
		case "VOLUME":
			return data.Volume, nil
		default:
			return data.Amount, nil
		}
	case incrementalBinary:
		return (&Interpreter{}).binaryOpScalarScalar(node.operator, input(0), input(1))
	case incrementalUnary:
		return -input(0), nil
	case incrementalPointwise:
		for i, arg := range node.args {
			arg.Single = input(i)
		}
		value, err := node.fn(node.args, nil)
		if err != nil {
			return 0, err
		}
		return value.Single, nil
	case incrementalAverage:
		value, alpha := input(0), node.alpha
		if node.name == "DMA" {
			alpha = input(1)
		}
		if math.IsNaN(value) || math.IsNaN(alpha) {
			return math.NaN(), nil
		}
		if math.IsNaN(node.next.smooth) {
			node.next.smooth = value
		} else {
			node.next.smooth = alpha*value + (1-alpha)*node.next.smooth
		}
		return node.next.smooth, nil
	case incrementalSum:
		value := input(0)
		if math.IsNaN(value) {
			node.next.missing++
		} else {
			node.next.sum += value
		}
		if index >= node.period {
			old := p.at(node.inputs[0], index-node.period, index)
			if math.IsNaN(old) {
				node.next.missing--
			} else {
				node.next.sum -= old
			}
		}
		if index < node.period-1 || node.next.missing > 0 {
			return math.NaN(), nil
		}
		if node.name == "SUM" {
			return node.next.sum, nil
		}
		return node.next.sum / float64(node.period), nil
	case incrementalCount:
		if isTruthy(input(0)) {
			node.next.sum++
		}
		if index >= node.period && isTruthy(p.at(node.inputs[0], index-node.period, index)) {
			node.next.sum--
		}
		if index < node.period-1 {
			if node.name == "COUNT" {
				return math.NaN(), nil
			}
			return 0, nil
		}
		if node.name == "COUNT" {
			return node.next.sum, nil
		}
		if (node.name == "EVERY" && node.next.sum == float64(node.period)) || (node.name == "EXIST" && node.next.sum > 0) {
			return 1, nil
		}
		return 0, nil
	case incrementalReference:
		return p.at(node.inputs[0], index-node.period, index), nil
	case incrementalCross:
		if index > 0 && p.at(node.inputs[0], index-1, index) <= p.at(node.inputs[1], index-1, index) && input(0) > input(1) {
			return 1, nil
		}
		return 0, nil
	case incrementalExtrema:
		if index < node.period-1 {
			return math.NaN(), nil
		}
		best, bars := input(0), 0
		highest := node.name == "HHV" || node.name == "HHVBARS"
		for distance := 0; distance < node.period; distance++ {
			value := p.at(node.inputs[0], index-distance, index)
			if math.IsNaN(value) {
				return math.NaN(), nil
			}
			if (highest && value > best) || (!highest && value < best) {
				best, bars = value, distance
			}
		}
		if node.name == "HHVBARS" || node.name == "LLVBARS" {
			return float64(bars), nil
		}
		return best, nil
	case incrementalWindow:
		if index < node.period-1 {
			return math.NaN(), nil
		}
		for i, arg := range node.args {
			if i == len(node.args)-1 {
				arg.Single = float64(node.period)
				continue
			}
			if !arg.IsArray {
				arg.IsArray, arg.Array = true, make([]float64, node.period)
			}
			for j := range arg.Array {
				arg.Array[j] = p.at(node.inputs[i], index-node.period+1+j, index)
			}
		}
		value, err := node.fn(node.args, nil)
		if err != nil {
			return 0, err
		}
		return value.Array[node.period-1], nil
	default:
		return 0, errors.NewRuntimeError("unknown incremental node")
	}
}
