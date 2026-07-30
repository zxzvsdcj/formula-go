package main

import (
	"fmt"
	"log"
	"math"

	"github.com/DTrader-store/formula-go"
)

const streamingFormula = `
MA3: MA(CLOSE, 3), COLORBLUE;
LAST_CLOSE: CLOSE, COLORBLACK;
UP_MARK := DRAWTEXT(CLOSE > OPEN, LOW, 'UP');
`

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	bars := []*formula.MarketData{
		formula.NewMarketData(100, 101, 102, 99, 1000, 101000),
		formula.NewMarketData(101, 100.5, 102, 100, 1100, 110550),
		formula.NewMarketData(100.5, 102, 103, 100, 1200, 122400),
	}

	engine := formula.NewFormulaEngine()
	session, err := engine.NewStreamingSessionFromSource(streamingFormula, bars[:2])
	if err != nil {
		return fmt.Errorf("create streaming session: %w", err)
	}

	fmt.Println("=== Streaming Formula Example ===")
	fmt.Printf("seeded bars: %d\n", session.Len())

	completed, err := session.Append(bars[2])
	if err != nil {
		return fmt.Errorf("append completed bar: %w", err)
	}
	printMutation("append completed bar", completed)

	current := formula.NewMarketData(102, 104, 105, 101.5, 1300, 135200)
	appended, err := session.Append(current)
	if err != nil {
		return fmt.Errorf("append current bar: %w", err)
	}
	printMutation("append current bar", appended)

	corrected := formula.NewMarketData(102, 101.5, 103, 100.5, 1400, 142100)
	updated, err := session.UpdateCurrent(corrected)
	if err != nil {
		return fmt.Errorf("update current bar: %w", err)
	}
	printMutation("update current bar", updated)

	history := session.History()
	fmt.Printf("history bars: %d, current close: %.2f\n", len(history), history[len(history)-1].Close)
	return nil
}

func printMutation(label string, result *formula.StreamingResult) {
	fmt.Printf("\n%s (bar %d)\n", label, result.BarIndex)
	for _, output := range result.Outputs {
		if math.IsNaN(output.Value) {
			fmt.Printf("  output %-10s NaN\n", output.Name)
			continue
		}
		fmt.Printf("  output %-10s %.3f\n", output.Name, output.Value)
	}
	for _, change := range result.Drawings {
		if change.Event == nil {
			fmt.Printf("  drawing %-7s key=%s\n", change.Action, change.Key)
			continue
		}
		fmt.Printf("  drawing %-7s function=%s bar=%d text=%q price=%.2f\n",
			change.Action,
			change.Event.Function,
			change.Event.BarIndex,
			change.Event.Text,
			change.Event.Values["price"],
		)
	}
}
