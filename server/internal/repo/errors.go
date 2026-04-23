package repo

import "errors"

// ErrNotFound is returned when no row matches the query.
var ErrNotFound = errors.New("not found")
