package auth

import (
	"errors"
	"fmt"
)

// UserIDLen is the canonical length of a Mattermost user id, also used as
// team id, channel id (mm-side), and any other 24-char hex MongoDB
// ObjectId pulled from the cses ecosystem.
const UserIDLen = 24

// ErrInvalidUserID is returned by ValidateUserID. It carries no diagnostic
// detail to keep handler error responses consistent (handlers typically map
// it to 400 + a generic "invalid user id" body).
var ErrInvalidUserID = errors.New("invalid user id")

// ValidateUserID returns nil iff s is exactly 24 lowercase-hex characters
// (the MongoDB ObjectId shape Mattermost uses for user / team / org ids).
//
// Validation policy:
//   - length must be exactly UserIDLen
//   - characters must be 0-9 or a-f (lowercase only — uppercase IDs from
//     external systems must be lower-cased before hitting im handlers)
//
// The function is allocation-free and takes O(len(s)) time. It is safe for
// hot-path use (called once per HTTP handler entry).
func ValidateUserID(s string) error {
	if len(s) != UserIDLen {
		return fmt.Errorf("%w: length %d != %d", ErrInvalidUserID, len(s), UserIDLen)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return fmt.Errorf("%w: non-hex byte at offset %d", ErrInvalidUserID, i)
		}
	}
	return nil
}

// MustUserID panics if s is not a valid user id. Tests-only helper — the
// panic is intentional so a fixture typo fails the test loudly rather than
// quietly producing a broken row. Production code MUST use ValidateUserID.
func MustUserID(s string) string {
	if err := ValidateUserID(s); err != nil {
		panic(fmt.Sprintf("auth.MustUserID: %v (got %q)", err, s))
	}
	return s
}
