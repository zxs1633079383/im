package model

import "time"

type UserStatus int16

const (
	UserStatusActive   UserStatus = 1
	UserStatusDisabled UserStatus = 2
)

type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	PasswordHash string     `json:"-"`
	DisplayName  string     `json:"display_name"`
	AvatarURL    string     `json:"avatar_url"`
	Status       UserStatus `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type UserSettings struct {
	UserID              int64  `json:"user_id"`
	NotificationEnabled bool   `json:"notification_enabled"`
	Theme               string `json:"theme"`
	Language            string `json:"language"`
	SettingsJSON        []byte `json:"settings_json"`
}
