package handler_test

import (
	"log/slog"
	"os"
)

// testLogger returns a slog.Logger writing to stderr (used by profile and settings tests).
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}
