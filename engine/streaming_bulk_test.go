package engine

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/DTrader-store/formula-go/interpreter"
	"github.com/DTrader-store/formula-go/types"
)

func TestStreamingBulkMatchesEveryPrefix(t *testing.T) {
	formulas := map[string]string{
		"MACD": benchmarkMACDFormula,
		"nested gaps": `N := ABS(-3); X := IF(C > O, C, DRAWNULL());
			A: EMA(MA(X, N), N), COLORRED; B: SMA(A, 2, 1); D: HHVBARS(X, N)`,
		"reassigned periods": `N := 2; A: MA(C, N); N := 5; B: MA(C, N); D: EMA(A, N)`,
		"scalar branches":    `N := IF(1, 3, 2); A: MA(C, N); B: IF(0, C, 7); S := 9`,
	}
	for name, tc := range builtinCoverageCases() {
		if name != "REFX" && name != "REFXV" {
			formulas[name] = tc.formula
		}
	}
	data := createBenchmarkMarketData(48)
	for name, source := range formulas {
		for _, seedLen := range []int{0, 1, 4, 30} {
			t.Run(fmt.Sprintf("%s/seed%d", name, seedLen), func(t *testing.T) {
				engine := NewFormulaEngine()
				bulk, err := engine.NewStreamingSessionFromSource(source, data[:seedLen])
				if err != nil {
					t.Fatal(err)
				}
				sequential, err := engine.NewStreamingSessionFromSource(source, data[:seedLen])
				if err != nil {
					t.Fatal(err)
				}
				sequential.incremental = nil // Keep full replay as the independent oracle.
				results, err := bulk.AppendMany(data[seedLen:])
				if err != nil {
					t.Fatal(err)
				}
				if len(results) != len(data)-seedLen {
					t.Fatal("incorrect prefix count")
				}
				for i, result := range results {
					want, err := sequential.Append(data[seedLen+i])
					if err != nil {
						t.Fatal(err)
					}
					assertStreamingResultEqual(t, result, want)
				}
				// Continue from the committed state and replace the current bar twice.
				for _, bar := range data[:2] {
					got, err := bulk.UpdateCurrent(bar)
					if err != nil {
						t.Fatal(err)
					}
					want, err := sequential.UpdateCurrent(bar)
					if err != nil {
						t.Fatal(err)
					}
					assertStreamingResultEqual(t, got, want)
				}
				got, err := bulk.Append(data[0])
				if err != nil {
					t.Fatal(err)
				}
				want, err := sequential.Append(data[0])
				if err != nil {
					t.Fatal(err)
				}
				assertStreamingResultEqual(t, got, want)
			})
		}
	}
}

func TestStreamingBulkRollsBackAndOwnsResults(t *testing.T) {
	engine := NewFormulaEngine()
	seed := createTestData()[:1]
	session, err := engine.NewStreamingSessionFromSource(`A: EMA(C, 2), COLORRED; B: 1 / (C - 103)`, seed)
	if err != nil {
		t.Fatal(err)
	}
	if session.bulkWarmup == 0 {
		t.Fatal("expected numeric bulk execution")
	}
	if _, err := session.AppendMany(createTestData()[1:4]); err == nil {
		t.Fatal("expected division by zero")
	}
	if !reflect.DeepEqual(session.History(), seed) {
		t.Fatal("failed bulk execution changed history")
	}
	input := createTestData()[2:5]
	results, err := session.AppendMany(input)
	if err != nil {
		t.Fatal(err)
	}
	firstValue := results[0].Outputs[0].Value
	input[0].Close = 999
	results[1].Outputs[0].Style.Color = "BLUE"
	results[1].Outputs[0].Value = 999
	if results[0].Outputs[0].Style.Color != "RED" || results[0].Outputs[0].Value != firstValue || session.History()[1].Close == 999 {
		t.Fatal("bulk results or history share mutable caller state")
	}
}

func TestStreamingBulkPreservesCustomFunctionCalls(t *testing.T) {
	engine := NewFormulaEngine()
	calls := 0
	engine.RegisterFunction("EMA", func(_ []*interpreter.Value, data []*types.MarketData) (*interpreter.Value, error) {
		calls++
		return interpreter.NewSingleValue(float64(len(data))), nil
	})
	session, err := engine.NewStreamingSessionFromSource("X: EMA(C, 3)", nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || session.bulkWarmup != 0 {
		t.Fatal("custom function was executed during analysis or marked bulk-safe")
	}
	results, err := session.AppendMany(createTestData())
	if err != nil {
		t.Fatal(err)
	}
	if calls != len(results) {
		t.Fatalf("expected %d custom calls, got %d", len(results), calls)
	}
	for i, result := range results {
		assertLatestOutput(t, result, "X", float64(i+1))
	}
}

func assertStreamingResultEqual(t *testing.T, got, want *types.StreamingResult) {
	t.Helper()
	if got.BarIndex != want.BarIndex || len(got.Outputs) != len(want.Outputs) || len(got.Variables) != len(want.Variables) || len(got.Drawings) != len(want.Drawings) {
		t.Fatalf("bar %d: mismatched result structure", want.BarIndex)
	}
	for i, expected := range want.Outputs {
		actual := got.Outputs[i]
		if actual.Name != expected.Name || !floatEqual(actual.Value, expected.Value) || !reflect.DeepEqual(actual.Style, expected.Style) {
			t.Fatalf("bar %d, output %s: got %+v, want %+v", want.BarIndex, expected.Name, actual, expected)
		}
	}
	for name, expected := range want.Variables {
		if actual, ok := got.Variables[name]; !ok || !floatEqual(actual, expected) {
			t.Fatalf("bar %d, variable %s: got %g, want %g", want.BarIndex, name, actual, expected)
		}
	}
	for i, expected := range want.Drawings {
		actual := got.Drawings[i]
		if actual.Action != expected.Action || actual.Key != expected.Key || !drawingEventsEqual(actual.Event, expected.Event) {
			t.Fatalf("bar %d: mismatched drawing delta", want.BarIndex)
		}
	}
}
