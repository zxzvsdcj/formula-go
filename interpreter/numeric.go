package interpreter

import (
	"fmt"
	"math"
	"strconv"

	"github.com/DTrader-store/formula-go/errors"
)

func isNumeric(value *Value) bool {
	return value != nil && !value.IsString && !value.IsDraw
}

func isNumericScalar(value *Value) bool {
	return isNumeric(value) && !value.IsArray
}

func isNumericArray(value *Value) bool {
	return isNumeric(value) && value.IsArray
}

// Check before converting to int: converting NaN, infinity or an out-of-range
// float to int is implementation dependent and can bypass period validation.
func isIntegerScalar(value *Value) bool {
	if !isNumericScalar(value) {
		return false
	}
	limit := math.Ldexp(1, strconv.IntSize-1)
	n := value.Single
	return n >= -limit && n < limit && n == math.Trunc(n)
}

// recursiveAverage initializes on the first valid sample. A missing sample
// emits a gap without changing the previous valid smoothing state.
func recursiveAverage(values []float64, alpha func(int) float64) []float64 {
	result := nanArray(len(values))
	previous := math.NaN()
	for i, value := range values {
		a := alpha(i)
		if math.IsNaN(value) || math.IsNaN(a) {
			continue
		}
		if math.IsNaN(previous) {
			previous = value
		} else {
			previous = a*value + (1-a)*previous
		}
		result[i] = previous
	}
	return result
}

// rollingExtrema uses a monotonic queue. Equal extrema prefer the newest bar;
// any NaN in the window invalidates both the value and its distance.
func rollingExtrema(args []*Value, name string, highest, bars bool) (*Value, error) {
	if len(args) != 2 {
		return nil, errors.NewRuntimeError(fmt.Sprintf("%s requires 2 arguments", name))
	}
	if !isNumericArray(args[0]) {
		return nil, errors.NewRuntimeError(fmt.Sprintf("%s first argument must be an array", name))
	}
	if !isIntegerScalar(args[1]) {
		return nil, errors.NewRuntimeError(fmt.Sprintf("%s second argument must be a number (finite integer)", name))
	}
	values, n := args[0].Array, int(args[1].Single)
	if n <= 0 || n > len(values) {
		return nil, errors.NewRuntimeError(fmt.Sprintf("%s period must be between 1 and %d", name, len(values)))
	}
	result := nanArray(len(values))
	queue := make([]int, n)
	head, count, missing := 0, 0, 0
	for i, value := range values {
		if count > 0 && queue[head] <= i-n {
			head = (head + 1) % n
			count--
		}
		if i >= n && math.IsNaN(values[i-n]) {
			missing--
		}
		if math.IsNaN(value) {
			missing++
		} else {
			for count > 0 {
				last := values[queue[(head+count-1)%n]]
				if (highest && last > value) || (!highest && last < value) {
					break
				}
				count--
			}
			queue[(head+count)%n] = i
			count++
		}
		if i < n-1 || missing > 0 {
			continue
		}
		if bars {
			result[i] = float64(i - queue[head])
		} else {
			result[i] = values[queue[head]]
		}
	}
	return NewArrayValue(result), nil
}
