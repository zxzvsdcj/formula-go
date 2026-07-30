package types

// DrawingChangeAction identifies how a streaming mutation changed drawing state.
type DrawingChangeAction string

const (
	// DrawingAdd means a drawing event is newly present after the mutation.
	DrawingAdd DrawingChangeAction = "add"
	// DrawingReplace means a previously present drawing event has new payload values.
	DrawingReplace DrawingChangeAction = "replace"
	// DrawingRemove means a previously present drawing event is no longer valid.
	DrawingRemove DrawingChangeAction = "remove"
)

// LatestOutput is the latest value for one numeric output line in a streaming result.
type LatestOutput struct {
	Name  string
	Value float64
	Style *LineStyle
}

// DrawingChange describes one rendering delta caused by a streaming mutation.
type DrawingChange struct {
	Action DrawingChangeAction
	Key    string
	Event  *DrawingEvent
}

// StreamingResult is the latest-bar response returned by streaming mutations.
type StreamingResult struct {
	BarIndex  int
	Outputs   []*LatestOutput
	Variables map[string]float64
	Drawings  []*DrawingChange
}
