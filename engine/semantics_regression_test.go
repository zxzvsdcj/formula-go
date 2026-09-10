package engine

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/DTrader-store/formula-go/interpreter"
	"github.com/DTrader-store/formula-go/types"
)

func TestRecursiveIndicatorsRecoverFromMissingInputs(t *testing.T) {
	data := createTestData()[:6]
	for _, fn := range []string{"EMA(X, 3)", "SMA(X, 2, 1)", "DMA(X, 0.5)"} {
		t.Run(fn, func(t *testing.T) {
			engine := NewFormulaEngine()
			engine.RegisterFunction("SOURCE", func(_ []*interpreter.Value, _ []*types.MarketData) (*interpreter.Value, error) {
				return interpreter.NewArrayValue([]float64{math.NaN(), 2, 4, math.NaN(), 8, 10}), nil
			})
			result, err := engine.Run("X := SOURCE(); Y: "+fn, data)
			if err != nil {
				t.Fatal(err)
			}
			assertSeries(t, outputByName(t, result, "Y"), []float64{math.NaN(), 2, 3, math.NaN(), 5.5, 7.75})
		})
	}
}

func TestNestedMovingAverageHasValidTail(t *testing.T) {
	data := make([]*types.MarketData, 5)
	for i := range data {
		c := float64(i + 1)
		data[i] = types.NewMarketData(c, c, c, c, 1, c)
	}
	result, err := NewFormulaEngine().Run("X: EMA(MA(C, 3), 3)", data)
	if err != nil {
		t.Fatal(err)
	}
	assertSeries(t, outputByName(t, result, "X"), []float64{math.NaN(), math.NaN(), 2, 2.5, 3.25})
}

func TestKDJNumericalRegression(t *testing.T) {
	closes := []float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 16, 19, 15}
	data := make([]*types.MarketData, len(closes))
	for i, c := range closes {
		data[i] = types.NewMarketData(c, c, 20, 0, 1, c)
	}
	const source = `LOW9 := LLV(LOW, 9); HIGH9 := HHV(HIGH, 9);
		RSV := (CLOSE - LOW9) / (HIGH9 - LOW9) * 100;
		K: SMA(RSV, 3, 1); D: SMA(K, 3, 1); J: 3 * K - 2 * D`
	want := map[string][]float64{
		"K": {90, 260.0 / 3, 805.0 / 9, 2285.0 / 27},
		"D": {90, 800.0 / 9, 2405.0 / 27, 7095.0 / 81},
		"J": {90, 740.0 / 9, 2435.0 / 27, 6375.0 / 81},
	}
	engine := NewFormulaEngine()
	result, err := engine.Run(source, data)
	if err != nil {
		t.Fatal(err)
	}
	for name, tail := range want {
		values := outputByName(t, result, name)
		for i, value := range values[:8] {
			if !math.IsNaN(value) {
				t.Fatalf("%s[%d] should be warming up, got %g", name, i, value)
			}
		}
		assertSeries(t, values[8:], tail)
	}
	session, err := engine.NewStreamingSessionFromSource(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, bar := range data {
		latest, err := session.Append(bar)
		if err != nil {
			t.Fatal(err)
		}
		for name, tail := range want {
			if i < 8 {
				assertLatestOutputNaN(t, latest, name)
			} else {
				assertLatestOutput(t, latest, name, tail[i-8])
			}
		}
	}
}

func TestNaNConditionTruthinessIsConsistent(t *testing.T) {
	result, err := NewFormulaEngine().Run(`
		A := DRAWNULL() AND 1; B := DRAWNULL() OR 0;
		D := IF(DRAWNULL(), 1, 0); E := IFF(DRAWNULL(), 1, 0);
		F := IFN(DRAWNULL(), 1, 0); N := NOT(DRAWNULL())`, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]float64{"A": 0, "B": 0, "D": 0, "E": 0, "F": 1, "N": 1} {
		if value, ok := result.GetVariable(name); !ok || value != want {
			t.Errorf("%s: got %g, want %g", name, value, want)
		}
	}
	result, err = NewFormulaEngine().Run(`X := REF(C, 1); A: X AND 1; B: 0 OR X; D: IF(X, 1, 0)`, createTestData()[:3])
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "D"} {
		assertSeries(t, outputByName(t, result, name), []float64{0, 1, 1})
	}
}

func TestNumericOperationsRejectTextAndDrawings(t *testing.T) {
	for _, invalid := range []string{"'UP'", "DRAWTEXT(C > O, C, 'UP')"} {
		for _, expression := range []string{
			"%s + 1", "C + %s", "-%s", "%s AND 1", "%s = 0",
			"REF(C, %s)", "NOT(%s)", "IF(%s, 1, 0)", "IF(1, %s, 0)",
			"IF(0, 1, %s)", "BETWEEN(C, %s, 200)", "DMA(C, %s)",
			"CONST(%s)", "VALUEWHEN(C > O, %s)", "SUMBARS(C, %s)",
			"ROUND2(C, %s)", "LAST(C > O, %s, 0)", "EXISTR(C > O, 2, %s)",
		} {
			source := "X := " + fmt.Sprintf(expression, invalid)
			t.Run(source, func(t *testing.T) {
				if _, err := NewFormulaEngine().Run(source, createTestData()); err == nil {
					t.Fatal("expected a numeric type error")
				}
			})
		}
	}
}

func TestExtremaWindowsPropagateNaNAndRecover(t *testing.T) {
	engine := NewFormulaEngine()
	engine.RegisterFunction("SOURCE", func(_ []*interpreter.Value, _ []*types.MarketData) (*interpreter.Value, error) {
		return interpreter.NewArrayValue([]float64{math.NaN(), 3, 3, 1, math.NaN(), 2, 4}), nil
	})
	for fn, want := range map[string][]float64{
		"HHV":     {math.NaN(), math.NaN(), 3, 3, math.NaN(), math.NaN(), 4},
		"LLV":     {math.NaN(), math.NaN(), 3, 1, math.NaN(), math.NaN(), 2},
		"HHVBARS": {math.NaN(), math.NaN(), 0, 1, math.NaN(), math.NaN(), 0},
		"LLVBARS": {math.NaN(), math.NaN(), 0, 0, math.NaN(), math.NaN(), 1},
	} {
		t.Run(fn, func(t *testing.T) {
			result, err := engine.Run("X: "+fn+"(SOURCE(), 2)", createTestData()[:7])
			if err != nil {
				t.Fatal(err)
			}
			assertSeries(t, outputByName(t, result, "X"), want)
		})
	}
}

func TestPeriodArgumentsRejectInvalidNumbersInBatchAndStreaming(t *testing.T) {
	for _, fn := range []string{"MA", "EMA", "SUM", "HHV", "LLV", "REF", "STD", "VAR", "WMA", "COUNT", "EVERY", "EXIST", "HHVBARS", "LLVBARS", "AVEDEV", "FILTER", "UPNDAY", "DOWNNDAY", "DEVSQ", "FORCAST", "SLOPE", "STDDEV", "SMA"} {
		for _, period := range []string{"'UP'", "DRAWNULL()", "EXP(1000)", "1e30", "2.5"} {
			source := fmt.Sprintf("X: %s(C, %s)", fn, period)
			t.Run(source, func(t *testing.T) {
				engine := NewFormulaEngine()
				if _, err := engine.Run(source, createTestData()); err == nil {
					t.Fatal("batch accepted invalid period")
				}
				session, err := engine.NewStreamingSessionFromSource(source, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = session.Append(createTestData()[0]); err == nil {
					t.Fatal("streaming accepted invalid period")
				}
				if session.Len() != 0 {
					t.Fatal("failed mutation committed history")
				}
			})
		}
	}
}

func TestEveryBuiltinNumericArgumentRejectsText(t *testing.T) {
	engine := NewFormulaEngine()
	for name, arity := range builtinArityCases() {
		for i := range arity.validArgs {
			if name == "DRAWTEXT" && i == 2 {
				continue // Text is an intentional part of this signature.
			}
			args := append([]string(nil), arity.validArgs...)
			args[i] = "'INVALID'"
			source := name + "(" + strings.Join(args, ",") + ")"
			t.Run(source, func(t *testing.T) {
				if _, err := engine.Run(source, createTestData()); err == nil {
					t.Fatal("numeric argument accepted text")
				}
			})
		}
	}
}
