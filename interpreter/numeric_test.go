package interpreter

import (
	"math"
	"math/rand"
	"testing"
)

func TestRollingExtremaMatchesWindowDefinition(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	values := make([]float64, 97)
	for i := range values {
		values[i] = float64(rng.Intn(9) - 4)
		if i%11 == 0 {
			values[i] = math.NaN()
		}
	}
	for _, period := range []int{1, 2, 7, 13, len(values)} {
		for _, highest := range []bool{false, true} {
			for _, bars := range []bool{false, true} {
				result, err := rollingExtrema([]*Value{NewArrayValue(values), NewSingleValue(float64(period))}, "EXTREMA", highest, bars)
				if err != nil {
					t.Fatal(err)
				}
				for i, actual := range result.Array {
					expected := math.NaN()
					if i >= period-1 {
						best, valid := i, true
						for j := i; j > i-period; j-- {
							valid = valid && !math.IsNaN(values[j])
							if (highest && values[j] > values[best]) || (!highest && values[j] < values[best]) {
								best = j
							}
						}
						if valid {
							expected = values[best]
							if bars {
								expected = float64(i - best)
							}
						}
					}
					if actual != expected && !(math.IsNaN(actual) && math.IsNaN(expected)) {
						t.Fatalf("period=%d highest=%v bars=%v index=%d: got %g, want %g", period, highest, bars, i, actual, expected)
					}
				}
			}
		}
	}
}

func TestRecursiveAverageWaitsForValidAlpha(t *testing.T) {
	alpha := []float64{math.NaN(), 0.5, math.NaN(), 0.5}
	result := recursiveAverage([]float64{2, 4, 8, 10}, func(i int) float64 { return alpha[i] })
	want := []float64{math.NaN(), 4, math.NaN(), 7}
	for i := range result {
		if result[i] != want[i] && !(math.IsNaN(result[i]) && math.IsNaN(want[i])) {
			t.Fatalf("index %d: got %g, want %g", i, result[i], want[i])
		}
	}
}

func TestFilterLargePeriodDoesNotOverflowBeforeFirstSignal(t *testing.T) {
	// A representable positive int close to the upper limit must still allow
	// the first true condition, even after a long run of false conditions.
	period := math.Nextafter(math.Ldexp(1, 63), 0)
	if !isIntegerScalar(NewSingleValue(period)) {
		t.Skip("requires 64-bit int")
	}
	values := make([]float64, 2049)
	values[2048] = 1
	result, err := fnFILTER([]*Value{NewArrayValue(values), NewSingleValue(period)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Array[2048] != 1 {
		t.Fatal("large period suppressed the first signal")
	}
}

func TestOneBarSlopePropagatesMissingInput(t *testing.T) {
	result, err := fnSLOPE([]*Value{NewArrayValue([]float64{math.NaN(), 4}), NewSingleValue(1)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(result.Array[0]) || result.Array[1] != 0 {
		t.Fatalf("expected [NaN, 0], got %v", result.Array)
	}
}
