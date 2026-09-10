package interpreter

import (
	"math"
	"testing"

	"github.com/DTrader-store/formula-go/lexer"
	"github.com/DTrader-store/formula-go/parser"
	"github.com/DTrader-store/formula-go/types"
)

func compileIncrementalForTest(t *testing.T, source string) *IncrementalProgram {
	t.Helper()
	tokens, err := lexer.NewLexer(source).Tokenize()
	if err != nil {
		t.Fatal(err)
	}
	program, err := parser.NewParser(tokens).Parse()
	if err != nil {
		t.Fatal(err)
	}
	plan := NewFunctionRegistry().CompileIncremental(program)
	if plan == nil {
		t.Fatal("formula did not compile incrementally")
	}
	return plan
}

func TestIncrementalRetainsOnlyRequiredLookbacks(t *testing.T) {
	plan := compileIncrementalForTest(t, "A: MA(C, 7); E: EMA(A, 3); R: REF(E, 5); D: STD(C, 7)")
	bar := types.NewMarketData(10, 11, 12, 9, 1, 11)
	seed := make([]*types.MarketData, 100)
	for i := range seed {
		seed[i] = bar
	}
	if err := plan.Seed(seed); err != nil {
		t.Fatal(err)
	}
	retained := func() int {
		total := 0
		for _, node := range plan.nodes {
			if len(node.values) > node.lookback+1 {
				t.Fatal("node retained excess history")
			}
			total += len(node.values)
		}
		return total
	}
	before := retained()
	for i := 0; i < 10000; i++ {
		if _, err := plan.Append(bar); err != nil {
			t.Fatal(err)
		}
	}
	if after := retained(); before != after {
		t.Fatalf("cache grew with history: %d -> %d", before, after)
	}
	if plan.Len() != 10100 {
		t.Fatal("incorrect bar count")
	}
}

func TestIncrementalHugeReferenceAllocatesOnlyAvailableHistory(t *testing.T) {
	plan := compileIncrementalForTest(t, "X: REF(C, 2147483647)")
	bar := types.NewMarketData(10, 11, 12, 9, 1, 11)
	if err := plan.Seed([]*types.MarketData{bar}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		result, err := plan.Append(bar)
		if err != nil {
			t.Fatal(err)
		}
		if !math.IsNaN(result.Outputs[0].Value) {
			t.Fatal("unavailable reference must be NaN")
		}
	}
	for _, node := range plan.nodes {
		if len(node.values) > 32 {
			t.Fatal("allocated the entire future lookback")
		}
	}
}

func TestIncrementalSeedFailureIsAtomic(t *testing.T) {
	plan := compileIncrementalForTest(t, "A: EMA(C, 2); X: 1 / (C - 12)")
	good := types.NewMarketData(10, 11, 13, 9, 1, 11)
	bad := types.NewMarketData(10, 12, 13, 9, 1, 12)
	if err := plan.Seed([]*types.MarketData{good, bad}); err == nil {
		t.Fatal("expected seed failure")
	}
	if plan.Len() != 0 {
		t.Fatal("failed seed committed bars")
	}
	if err := plan.Seed([]*types.MarketData{good, good}); err != nil {
		t.Fatal(err)
	}
	if got := plan.Latest().Outputs[0].Value; got != 11 {
		t.Fatalf("failed seed contaminated EMA: %g", got)
	}
}
