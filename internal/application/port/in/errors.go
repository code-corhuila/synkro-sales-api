package in

import "errors"

// ErrPersistenceFailure distinguishes corrupt stored data or driver failures
// from business invariants of a new request, while preserving the cause.
var ErrPersistenceFailure = errors.New("sale persistence failed")

// ErrIdempotencyConflict reports a reused Idempotency-Key with different business input.
// It carries no stored data, so no caller can learn the original request from it.
var ErrIdempotencyConflict = errors.New("idempotency key already used with a different request")
