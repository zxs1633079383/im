//go:build integration

package repo

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"im-server/internal/testutil/containers"
)

// TestModels_NoSchemaDrift runs a no-rows query against each model. An empty
// table returns ErrRecordNotFound (success). A column mismatch surfaces as a
// different error (e.g. "column does not exist").
func TestModels_NoSchemaDrift(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)

	type modelCase struct {
		name string
		dest any
	}
	cases := []modelCase{
		{"User", &User{}},
		{"Channel", &Channel{}},
		{"ChannelMember", &ChannelMember{}},
		{"Message", &Message{}},
		{"Friendship", &Friendship{}},
		{"File", &File{}},
		{"MessageAttachment", &MessageAttachment{}},
		{"MessageFavorite", &MessageFavorite{}},
		{"UserSettings", &UserSettings{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := db.First(tc.dest).Error
			require.True(t, err == nil || errors.Is(err, gorm.ErrRecordNotFound),
				"model %s schema mismatch: %v", tc.name, err)
		})
	}
}
