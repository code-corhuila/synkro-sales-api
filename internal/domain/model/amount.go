package model

import (
	"errors"
	"math"
)

var ErrAmountOverflow = errors.New("amount exceeds the int64 range")

// mulExact returns a*b, or ErrAmountOverflow if it does not fit in int64.
// Both operands must be positive, which the line validation guarantees.
func mulExact(a, b int64) (int64, error) {
	if a > math.MaxInt64/b {
		return 0, ErrAmountOverflow
	}
	return a * b, nil
}

// addExact returns a+b, or ErrAmountOverflow if it does not fit in int64.
// Both operands must be non-negative.
func addExact(a, b int64) (int64, error) {
	if a > math.MaxInt64-b {
		return 0, ErrAmountOverflow
	}
	return a + b, nil
}
