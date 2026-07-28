package engine

import (
	"math"
	"testing"

	"github.com/DTrader-store/formula-go/types"
)

func TestExecuteReturnsErrorForNilMarketDataEntry(t *testing.T) {
	formulaEngine := NewFormulaEngine()
	program, err := formulaEngine.Compile("CLOSE")
	if err != nil {
		t.Fatalf("compile formula: %v", err)
	}

	if _, err := formulaEngine.Execute(program, []*types.MarketData{nil}); err == nil {
		t.Fatal("Execute() error = nil, want an error for a nil market data entry")
	}
}

func TestExecuteReturnsErrorForInvalidMarketData(t *testing.T) {
	formulaEngine := NewFormulaEngine()
	program, err := formulaEngine.Compile("CLOSE")
	if err != nil {
		t.Fatalf("compile formula: %v", err)
	}
	invalid := types.NewMarketData(10, 10, 8, 9, 100, 1000)

	if _, err := formulaEngine.Execute(program, []*types.MarketData{invalid}); err == nil {
		t.Fatal("Execute() error = nil, want an error for invalid market data")
	}
}

func TestExecuteReturnsErrorForNonFiniteMarketData(t *testing.T) {
	formulaEngine := NewFormulaEngine()
	program, err := formulaEngine.Compile("CLOSE")
	if err != nil {
		t.Fatalf("compile formula: %v", err)
	}
	invalid := types.NewMarketData(math.NaN(), 10, 11, 9, 100, 1000)

	if _, err := formulaEngine.Execute(program, []*types.MarketData{invalid}); err == nil {
		t.Fatal("Execute() error = nil, want an error for non-finite market data")
	}
}
