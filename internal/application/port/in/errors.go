package in

import "errors"

// ErrPersistenceFailure distinguishes corrupt stored data or driver failures
// from business invariants of a new request, while preserving the cause.
var ErrPersistenceFailure = errors.New("sale persistence failed")
