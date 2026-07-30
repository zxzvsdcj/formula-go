package engine

import (
	"fmt"
	"math"
	"testing"

	"github.com/DTrader-store/formula-go/interpreter"
	"github.com/DTrader-store/formula-go/parser/ast"
	"github.com/DTrader-store/formula-go/types"
)

func TestStreamingAppendMatchesFreshBatchLatestBar(t *testing.T) {
	engine := NewFormulaEngine()
	session, err := engine.NewStreamingSessionFromSource(`
		DIFF: HIGH - LOW
		CLOSE_COPY: CLOSE
		SCALAR := 7
	`, nil)
	if err != nil {
		t.Fatalf("create streaming session: %v", err)
	}

	data := createTestData()[:4]
	for i := range data {
		result, err := session.Append(data[i])
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if result.BarIndex != i {
			t.Fatalf("append %d: expected bar index %d, got %d", i, i, result.BarIndex)
		}

		batch, err := engine.Run(`
			DIFF: HIGH - LOW
			CLOSE_COPY: CLOSE
			SCALAR := 7
		`, data[:i+1])
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		batchOutputs := outputsByName(batch)
		assertLatestOutput(t, result, "DIFF", batchOutputs["DIFF"][i])
		assertLatestOutput(t, result, "CLOSE_COPY", batchOutputs["CLOSE_COPY"][i])
		assertStreamingVariable(t, result, "SCALAR", 7)
	}
}

func TestStreamingUpdateCurrentMatchesFreshBatchLatestBar(t *testing.T) {
	engine := NewFormulaEngine()
	seed := cloneMarketDataForTest(createTestData()[:3])
	session, err := engine.NewStreamingSessionFromSource(`
		MA3: MA(CLOSE, 3)
		DIFF: HIGH - LOW
	`, seed)
	if err != nil {
		t.Fatalf("create streaming session: %v", err)
	}

	updated := types.NewMarketData(120, 126, 130, 119, 2000, 252000)
	result, err := session.UpdateCurrent(updated)
	if err != nil {
		t.Fatalf("update current: %v", err)
	}
	if result.BarIndex != 2 {
		t.Fatalf("expected bar index 2, got %d", result.BarIndex)
	}
	if session.Len() != 3 {
		t.Fatalf("expected session length 3, got %d", session.Len())
	}

	fresh := cloneMarketDataForTest(seed)
	fresh[2] = updated
	batch, err := engine.Run(`
		MA3: MA(CLOSE, 3)
		DIFF: HIGH - LOW
	`, fresh)
	if err != nil {
		t.Fatalf("batch after replace: %v", err)
	}
	batchOutputs := outputsByName(batch)
	assertLatestOutput(t, result, "MA3", batchOutputs["MA3"][2])
	assertLatestOutput(t, result, "DIFF", batchOutputs["DIFF"][2])
}

func TestStreamingMACrossStrategyMatchesBatchLatestBar(t *testing.T) {
	engine := NewFormulaEngine()
	formula := `
		FAST_MA := MA(CLOSE, 5)
		SLOW_MA := MA(CLOSE, 10)
		GOLDEN_CROSS := CROSS(FAST_MA, SLOW_MA)
		DEATH_CROSS := CROSS(SLOW_MA, FAST_MA)
	`
	data := createTestData()
	session, err := engine.NewStreamingSessionFromSource(formula, data)
	if err != nil {
		t.Fatalf("create MA cross session: %v", err)
	}

	newBar := types.NewMarketData(113, 116, 118, 112, 2000, 232000)
	streaming, err := session.Append(newBar)
	if err != nil {
		t.Fatalf("append MA cross bar: %v", err)
	}

	batchData := append(cloneMarketDataForTest(data), newBar)
	batch, err := engine.Run(formula, batchData)
	if err != nil {
		t.Fatalf("run batch MA cross strategy: %v", err)
	}
	batchOutputs := outputsByName(batch)
	last := len(batchData) - 1
	for _, name := range []string{"FAST_MA", "SLOW_MA", "GOLDEN_CROSS", "DEATH_CROSS"} {
		assertLatestOutput(t, streaming, name, batchOutputs[name][last])
	}
}

func TestStreamingWarmupReturnsNaNWithoutChangingBatchBehavior(t *testing.T) {
	engine := NewFormulaEngine()
	formula := `
		MA5: MA(CLOSE, 5)
		UP5: UPNDAY(CLOSE, 5)
	`

	if _, err := engine.Run(formula, createTestData()[:1]); err == nil {
		t.Fatal("expected batch short-history MA to fail")
	}

	session, err := engine.NewStreamingSessionFromSource(formula, nil)
	if err != nil {
		t.Fatalf("create streaming session: %v", err)
	}
	result, err := session.Append(createTestData()[0])
	if err != nil {
		t.Fatalf("append warm-up bar: %v", err)
	}
	assertLatestOutputNaN(t, result, "MA5")
	assertLatestOutputNaN(t, result, "UP5")

	more, err := session.AppendMany(createTestData()[1:5])
	if err != nil {
		t.Fatalf("append to full period: %v", err)
	}
	batch, err := engine.Run(formula, createTestData()[:5])
	if err != nil {
		t.Fatalf("batch full period: %v", err)
	}
	batchOutputs := outputsByName(batch)
	last := more[len(more)-1]
	assertLatestOutput(t, last, "MA5", batchOutputs["MA5"][4])
	assertLatestOutput(t, last, "UP5", batchOutputs["UP5"][4])
}

func TestStreamingInvalidPeriodDoesNotCommit(t *testing.T) {
	engine := NewFormulaEngine()
	session, err := engine.NewStreamingSessionFromSource(`BAD: MA(CLOSE, 0)`, nil)
	if err != nil {
		t.Fatalf("create streaming session: %v", err)
	}
	if _, err := session.Append(createTestData()[0]); err == nil {
		t.Fatal("expected invalid period append to fail")
	}
	if session.Len() != 0 {
		t.Fatalf("expected failed append to leave length 0, got %d", session.Len())
	}
}

func TestStreamingAppendManyReturnsOrderedPrefixResultsAndContinues(t *testing.T) {
	engine := NewFormulaEngine()
	formula := `
		TOTAL: TOTALBARSCOUNT()
		LAST: ISLASTBAR()
		STATUS: BARSTATUS()
	`
	session, err := engine.NewStreamingSessionFromSource(formula, nil)
	if err != nil {
		t.Fatalf("create streaming session: %v", err)
	}

	results, err := session.AppendMany(createTestData()[:3])
	if err != nil {
		t.Fatalf("append many: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, result := range results {
		if result.BarIndex != i {
			t.Fatalf("result %d: expected bar index %d, got %d", i, i, result.BarIndex)
		}
		assertLatestOutput(t, result, "TOTAL", float64(i+1))
		assertLatestOutput(t, result, "LAST", 1)
	}
	assertLatestOutput(t, results[0], "STATUS", 1)
	assertLatestOutput(t, results[1], "STATUS", 3)
	assertLatestOutput(t, results[2], "STATUS", 3)

	continued, err := session.Append(createTestData()[3])
	if err != nil {
		t.Fatalf("append after bulk: %v", err)
	}
	if continued.BarIndex != 3 || session.Len() != 4 {
		t.Fatalf("expected append after bulk at index 3 and len 4, got index %d len %d", continued.BarIndex, session.Len())
	}
}

func TestStreamingDrawingInvalidationsOnUpdateCurrent(t *testing.T) {
	engine := NewFormulaEngine()
	formula := `MARK := DRAWTEXT(CLOSE > OPEN, LOW, 'UP')`
	seed := []*types.MarketData{types.NewMarketData(10, 12, 13, 9, 100, 1200)}
	session, err := engine.NewStreamingSessionFromSource(formula, seed)
	if err != nil {
		t.Fatalf("create streaming session: %v", err)
	}

	removed, err := session.UpdateCurrent(types.NewMarketData(12, 10, 13, 9, 100, 1000))
	if err != nil {
		t.Fatalf("update false: %v", err)
	}
	assertSingleDrawingChange(t, removed, types.DrawingRemove, "DRAWTEXT", 0)

	added, err := session.UpdateCurrent(types.NewMarketData(10, 12, 13, 9, 100, 1200))
	if err != nil {
		t.Fatalf("update true: %v", err)
	}
	assertSingleDrawingChange(t, added, types.DrawingAdd, "DRAWTEXT", 0)

	replaced, err := session.UpdateCurrent(types.NewMarketData(10, 12, 13, 8, 100, 1200))
	if err != nil {
		t.Fatalf("update changed payload: %v", err)
	}
	assertSingleDrawingChange(t, replaced, types.DrawingReplace, "DRAWTEXT", 0)
	if got := replaced.Drawings[0].Event.Values["price"]; got != 8 {
		t.Fatalf("expected replaced drawing price 8, got %.2f", got)
	}
}

func TestStreamingAtomicExecutionFailureDoesNotCommit(t *testing.T) {
	engine := NewFormulaEngine()
	engine.RegisterFunction("FAILON2", func(_ []*interpreter.Value, marketData []*types.MarketData) (*interpreter.Value, error) {
		if len(marketData) == 2 {
			return nil, fmt.Errorf("boom")
		}
		values := make([]float64, len(marketData))
		for i := range values {
			values[i] = float64(len(marketData))
		}
		return interpreter.NewArrayValue(values), nil
	})
	session, err := engine.NewStreamingSessionFromSource(`X: FAILON2()`, nil)
	if err != nil {
		t.Fatalf("create streaming session: %v", err)
	}

	if _, err := session.AppendMany(createTestData()[:2]); err == nil {
		t.Fatal("expected second prefix execution to fail")
	}
	if session.Len() != 0 {
		t.Fatalf("expected failed bulk append to leave length 0, got %d", session.Len())
	}
	result, err := session.Append(createTestData()[0])
	if err != nil {
		t.Fatalf("append after rollback: %v", err)
	}
	assertLatestOutput(t, result, "X", 1)
}

func TestStreamingRejectsEmptyUpdateFutureReferencesAndCallerMutation(t *testing.T) {
	engine := NewFormulaEngine()
	session, err := engine.NewStreamingSessionFromSource(`CLOSE_OUT: CLOSE`, nil)
	if err != nil {
		t.Fatalf("create streaming session: %v", err)
	}
	if _, err := session.UpdateCurrent(createTestData()[0]); err == nil {
		t.Fatal("expected update-current on empty session to fail")
	}
	if _, err := session.Append(nil); err == nil {
		t.Fatal("expected nil append to fail")
	}
	if _, err := engine.NewStreamingSessionFromSource(`CLOSE_OUT: CLOSE`, []*types.MarketData{nil}); err == nil {
		t.Fatal("expected nil seed to fail")
	}

	seeded, err := engine.NewStreamingSessionFromSource(`CLOSE_OUT: CLOSE`, createTestData()[:1])
	if err != nil {
		t.Fatalf("create seeded session: %v", err)
	}
	if _, err := seeded.UpdateCurrent(nil); err == nil {
		t.Fatal("expected nil update-current to fail")
	}

	if _, err := engine.NewStreamingSessionFromSource(`F: refx(CLOSE, 1)`, nil); err == nil {
		t.Fatal("expected lower-case future reference to be rejected")
	}

	bar := types.NewMarketData(10, 11, 12, 9, 100, 1100)
	if _, err := session.Append(bar); err != nil {
		t.Fatalf("append for ownership test: %v", err)
	}
	bar.Close = 999
	history := session.History()
	if history[0].Close != 11 {
		t.Fatalf("expected appended data to be copied, got close %.2f", history[0].Close)
	}
	history[0].Close = 888
	if session.History()[0].Close != 11 {
		t.Fatal("expected History to return a deep-copy snapshot")
	}
}

func TestStreamingUsesRegisteredFunctionSemantics(t *testing.T) {
	engine := NewFormulaEngine()
	engine.RegisterFunction("MA", func(_ []*interpreter.Value, marketData []*types.MarketData) (*interpreter.Value, error) {
		values := make([]float64, len(marketData))
		for i := range values {
			values[i] = 42
		}
		return interpreter.NewArrayValue(values), nil
	})
	engine.RegisterFunction("REFX", func(_ []*interpreter.Value, marketData []*types.MarketData) (*interpreter.Value, error) {
		values := make([]float64, len(marketData))
		for i := range values {
			values[i] = 24
		}
		return interpreter.NewArrayValue(values), nil
	})

	maSession, err := engine.NewStreamingSessionFromSource(`OUT: MA(CLOSE, 5)`, nil)
	if err != nil {
		t.Fatalf("create session with overridden MA: %v", err)
	}
	maResult, err := maSession.Append(createTestData()[0])
	if err != nil {
		t.Fatalf("append with overridden MA: %v", err)
	}
	assertLatestOutput(t, maResult, "OUT", 42)

	refSession, err := engine.NewStreamingSessionFromSource(`OUT: REFX(CLOSE, 1)`, nil)
	if err != nil {
		t.Fatalf("create session with overridden REFX: %v", err)
	}
	refResult, err := refSession.Append(createTestData()[0])
	if err != nil {
		t.Fatalf("append with overridden REFX: %v", err)
	}
	assertLatestOutput(t, refResult, "OUT", 24)
}

func TestStreamingOwnsProgramSnapshot(t *testing.T) {
	engine := NewFormulaEngine()
	program, err := engine.Compile(`OUT: MA(CLOSE, 1)`)
	if err != nil {
		t.Fatalf("compile program: %v", err)
	}
	session, err := engine.NewStreamingSession(program, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	call := program.Body[0].(*ast.OutputDeclaration).Value.(*ast.FunctionCall)
	call.Name = "REFX"

	data := createTestData()[0]
	result, err := session.Append(data)
	if err != nil {
		t.Fatalf("append after caller AST mutation: %v", err)
	}
	assertLatestOutput(t, result, "OUT", data.Close)
}

func TestStreamingShortSeriesStillFailsAfterWarmup(t *testing.T) {
	engine := NewFormulaEngine()
	engine.RegisterFunction("SHORT", func(_ []*interpreter.Value, _ []*types.MarketData) (*interpreter.Value, error) {
		return interpreter.NewArrayValue([]float64{1}), nil
	})
	session, err := engine.NewStreamingSessionFromSource(`OUT: MA(SHORT(), 3)`, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := session.AppendMany(createTestData()[:3]); err == nil {
		t.Fatal("expected short series error once history reaches the requested period")
	}
	if session.Len() != 0 {
		t.Fatalf("expected failed append-many to leave length 0, got %d", session.Len())
	}
}

func assertLatestOutput(t *testing.T, result *types.StreamingResult, name string, want float64) {
	t.Helper()
	got, ok := latestOutputByName(result, name)
	if !ok {
		t.Fatalf("expected latest output %s", name)
	}
	if math.IsNaN(want) {
		if !math.IsNaN(got) {
			t.Fatalf("output %s: expected NaN, got %f", name, got)
		}
		return
	}
	if math.Abs(got-want) > 0.0001 {
		t.Fatalf("output %s: expected %.6f, got %.6f", name, want, got)
	}
}

func assertLatestOutputNaN(t *testing.T, result *types.StreamingResult, name string) {
	t.Helper()
	assertLatestOutput(t, result, name, math.NaN())
}

func assertStreamingVariable(t *testing.T, result *types.StreamingResult, name string, want float64) {
	t.Helper()
	got, ok := result.Variables[name]
	if !ok {
		t.Fatalf("expected streaming variable %s", name)
	}
	if math.Abs(got-want) > 0.0001 {
		t.Fatalf("variable %s: expected %.6f, got %.6f", name, want, got)
	}
}

func latestOutputByName(result *types.StreamingResult, name string) (float64, bool) {
	for _, output := range result.Outputs {
		if output.Name == name {
			return output.Value, true
		}
	}
	return 0, false
}

func assertSingleDrawingChange(t *testing.T, result *types.StreamingResult, action types.DrawingChangeAction, function string, barIndex int) {
	t.Helper()
	if len(result.Drawings) != 1 {
		t.Fatalf("expected 1 drawing change, got %d", len(result.Drawings))
	}
	change := result.Drawings[0]
	if change.Action != action {
		t.Fatalf("expected drawing action %s, got %s", action, change.Action)
	}
	if change.Event == nil {
		t.Fatal("expected drawing event payload")
	}
	if change.Event.Function != function || change.Event.BarIndex != barIndex {
		t.Fatalf("expected %s at bar %d, got %s at bar %d", function, barIndex, change.Event.Function, change.Event.BarIndex)
	}
	if change.Key == "" {
		t.Fatal("expected stable drawing key")
	}
}

func cloneMarketDataForTest(data []*types.MarketData) []*types.MarketData {
	result := make([]*types.MarketData, len(data))
	for i, item := range data {
		copy := *item
		result[i] = &copy
	}
	return result
}
