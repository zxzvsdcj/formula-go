// Package types provides common types for market data and formula results
package types

import (
	"fmt"
	"math"
)

// MarketData represents a single market data record with OHLCV data
type MarketData struct {
	Open   float64 // Opening price
	Close  float64 // Closing price
	High   float64 // Highest price in the period
	Low    float64 // Lowest price in the period
	Volume float64 // Trading volume (number of shares/units traded)
	Amount float64 // Trading amount (volume * price, optional)
}

// NewMarketData creates a new MarketData instance
func NewMarketData(open, close, high, low, volume, amount float64) *MarketData {
	return &MarketData{
		Open:   open,
		Close:  close,
		High:   high,
		Low:    low,
		Volume: volume,
		Amount: amount,
	}
}

// Validate validates a MarketData object to ensure all required fields are present
// and have correct types and logical constraints
//
// Validation rules:
// - open, close, high, low must be numbers
// - volume must be a non-negative number
// - amount must be a non-negative number
// - high must be >= low
func (m *MarketData) Validate() error {
	if m == nil {
		return fmt.Errorf("market data must not be nil")
	}

	values := []struct {
		name  string
		value float64
	}{
		{"open", m.Open},
		{"close", m.Close},
		{"high", m.High},
		{"low", m.Low},
		{"volume", m.Volume},
		{"amount", m.Amount},
	}
	for _, field := range values {
		if math.IsNaN(field.value) || math.IsInf(field.value, 0) {
			return fmt.Errorf("%s must be finite, got %f", field.name, field.value)
		}
	}

	// Validate logical constraints
	if m.High < m.Low {
		return fmt.Errorf("high (%f) must be >= low (%f)", m.High, m.Low)
	}

	// Volume must be non-negative
	if m.Volume < 0 {
		return fmt.Errorf("volume must be non-negative, got %f", m.Volume)
	}

	// Amount must be non-negative
	if m.Amount < 0 {
		return fmt.Errorf("amount must be non-negative, got %f", m.Amount)
	}

	return nil
}

// GetMarketDataLength returns the length of a MarketData slice
func GetMarketDataLength(data []*MarketData) int {
	return len(data)
}
