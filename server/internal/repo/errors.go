package repo

import "errors"

// ErrNotFound is returned when no row matches the query.
var ErrNotFound = errors.New("not found")

// ErrForbidden is returned when the caller is not authorised to perform the
// action on the targeted row (e.g. editing someone else's message).
var ErrForbidden = errors.New("forbidden")

// ErrGone is returned when the targeted row is already in a terminal state
// (e.g. an already soft-deleted message). Idempotent callers can treat this
// as success.
var ErrGone = errors.New("already gone")
