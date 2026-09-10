package engine

import (
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/DTrader-store/formula-go/interpreter"
	"github.com/DTrader-store/formula-go/types"
)

const incrementalRegressionFormula = `
	N := ABS(-7);
	X := IF(C > O, C, DRAWNULL());
	A: MA(X, N), COLORRED;
	E: EMA(X, N);
	S: SMA(X, 3, 1);
	D: DMA(X, IF(C > O, 0.25, DRAWNULL()));
	TOTAL: SUM(X, N);
	UPPER: HHV(X, N);
	LOWER: LLV(X, N);
	UPPER_BAR: HHVBARS(X, N);
	LOWER_BAR: LLVBARS(X, N);
	SHIFT: REF(A, 5);
	SIGNAL: CROSS(E, S);
	COUNT7: COUNT(X, N);
	EVERY7: EVERY(X, N);
	EXIST7: EXIST(X, N);
	DEV: STD(X, N);
	SAMPLE: STDDEV(X, N);
	WEIGHTED: WMA(X, N);
	COV: COVAR(C, O, N);
	REL: RELATE(C, O, N);
	SLOPE7: SLOPE(X, N);
	SCALAR: IF(0, C, 9);
	CONSTANT := 42;
`

func TestIncrementalMixedMutationsMatchFullReplay(t *testing.T) {
	for _, seedLen := range []int{0, 1, 6, 7, 64} {
		t.Run(fmt.Sprintf("seed%d", seedLen), func(t *testing.T) {
			engine := NewFormulaEngine()
			seed := createBenchmarkMarketData(seedLen)
			fast, err := engine.NewStreamingSessionFromSource(incrementalRegressionFormula, seed)
			if err != nil {
				t.Fatal(err)
			}
			if fast.incremental == nil {
				t.Fatal("expected an incremental plan")
			}
			replay, err := engine.NewStreamingSessionFromSource(incrementalRegressionFormula, seed)
			if err != nil {
				t.Fatal(err)
			}
			replay.incremental, replay.bulkWarmup = nil, 0
			for step := 0; step < 160; step++ {
				bar := createBenchmarkMarketDataBar(100 + step)
				if step%5 == 0 {
					batch := []*types.MarketData{bar, createBenchmarkMarketDataBar(101 + step), createBenchmarkMarketDataBar(102 + step)}
					got, err := fast.AppendMany(batch)
					if err != nil {
						t.Fatal(err)
					}
					want, err := replay.AppendMany(batch)
					if err != nil {
						t.Fatal(err)
					}
					for i := range got {
						assertStreamingResultEqual(t, got[i], want[i])
					}
				} else {
					var got, want *types.StreamingResult
					if step%5 == 4 {
						got, err = fast.Append(bar)
						if err == nil {
							want, err = replay.Append(bar)
						}
					} else {
						got, err = fast.UpdateCurrent(bar)
						if err == nil {
							want, err = replay.UpdateCurrent(bar)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					assertStreamingResultEqual(t, got, want)
				}
			}
			if !fast.incrementalReady() {
				t.Fatal("warm-up did not activate incremental execution")
			}
			if !reflect.DeepEqual(fast.History(), replay.History()) {
				t.Fatal("history differs from replay")
			}
		})
	}
}

func TestIncrementalFailuresPreservePreviousBarState(t *testing.T) {
	const source = `A: MA(C, 3); E: EMA(C, 3); R: REF(E, 2); H: HHV(C, 3); X: 1 / (C - 103)`
	engine := NewFormulaEngine()
	seed := []*types.MarketData{createTestData()[0], createTestData()[2], createTestData()[3]}
	fast, err := engine.NewStreamingSessionFromSource(source, seed)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := engine.NewStreamingSessionFromSource(source, seed)
	if err != nil {
		t.Fatal(err)
	}
	replay.incremental = nil
	before, history := fast.incremental.Latest(), fast.History()
	bad := types.NewMarketData(103, 103, 104, 102, 1, 103)
	if _, err := fast.UpdateCurrent(bad); err == nil {
		t.Fatal("expected update to fail")
	}
	assertStreamingResultEqual(t, fast.incremental.Latest(), before)
	if _, err := fast.Append(bad); err == nil {
		t.Fatal("expected append to fail")
	}
	assertStreamingResultEqual(t, fast.incremental.Latest(), before)
	if _, err := fast.AppendMany([]*types.MarketData{seed[0], seed[1], bad}); err == nil {
		t.Fatal("expected batch to fail")
	}
	assertStreamingResultEqual(t, fast.incremental.Latest(), before)
	if !reflect.DeepEqual(fast.History(), history) {
		t.Fatal("failed mutation changed history")
	}
	for i := 0; i < 8; i++ {
		bar := createBenchmarkMarketDataBar(300 + i)
		got, err := fast.UpdateCurrent(bar)
		if err != nil {
			t.Fatal(err)
		}
		want, err := replay.UpdateCurrent(bar)
		if err != nil {
			t.Fatal(err)
		}
		assertStreamingResultEqual(t, got, want)
	}
	got, err := fast.Append(seed[0])
	if err != nil {
		t.Fatal(err)
	}
	want, err := replay.Append(seed[0])
	if err != nil {
		t.Fatal(err)
	}
	assertStreamingResultEqual(t, got, want)
}

func TestIncrementalResultsAndInputsRemainIsolated(t *testing.T) {
	engine := NewFormulaEngine()
	fast, err := engine.NewStreamingSessionFromSource(`N := 3; A: MA(C, N), COLORRED`, createTestData())
	if err != nil {
		t.Fatal(err)
	}
	input := createBenchmarkMarketDataBar(100)
	first, err := fast.Append(input)
	if err != nil {
		t.Fatal(err)
	}
	expected := first.Outputs[0].Value
	input.Close = 999
	first.Outputs[0].Style.Color = "BLUE"
	first.Outputs[0].Value = 999
	first.Variables["N"] = 999
	latest := fast.incremental.Latest()
	if latest.Outputs[0].Value != expected || latest.Outputs[0].Style.Color != "RED" || latest.Variables["N"] != 3 || fast.History()[10].Close == 999 {
		t.Fatal("incremental state shares mutable caller values")
	}
	bad := createBenchmarkMarketDataBar(100)
	bad.Volume = math.NaN()
	if _, err := fast.UpdateCurrent(bad); err == nil {
		t.Fatal("expected input validation error")
	}
	assertStreamingResultEqual(t, fast.incremental.Latest(), latest)
}

func TestIncrementalFallbackPreservesFunctionSemantics(t *testing.T) {
	engine := NewFormulaEngine()
	calls := 0
	engine.RegisterFunction("CUSTOM", func(_ []*interpreter.Value, data []*types.MarketData) (*interpreter.Value, error) {
		calls++
		return interpreter.NewSingleValue(float64(len(data))), nil
	})
	for _, source := range []string{
		"X: CONST(C)", "X: TOTALBARSCOUNT()", "X: ISLASTBAR()", "X: BARSLAST(C > O)",
		"X: CUSTOM()", "DRAWTEXT(C > O, C, 'UP')",
	} {
		session, err := engine.NewStreamingSessionFromSource(source, createTestData())
		if err != nil {
			t.Fatal(err)
		}
		if session.incremental != nil {
			t.Fatalf("unexpected incremental plan: %s", source)
		}
		if _, err := session.Append(createTestData()[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := session.UpdateCurrent(createTestData()[1]); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("expected custom function to run for seed, append and update; got %d", calls)
	}
}
