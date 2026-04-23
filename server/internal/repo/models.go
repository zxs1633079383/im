package repo

import (
	"time"

	"github.com/lib/pq"
)

// User maps the users table.
type User struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"`
	Username     string    `gorm:"size:50;uniqueIndex;not null"`
	Email        string    `gorm:"size:255;uniqueIndex;not null"`
	PasswordHash string    `gorm:"column:password_hash;size:255;not null"`
	DisplayName  string    `gorm:"column:display_name;size:100;not null;default:''"`
	AvatarURL    string    `gorm:"column:avatar_url;not null;default:''"`
	Status       int16     `gorm:"not null;default:1"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null;default:now()"`
}

// TableName pins the GORM-derived table name to the migration.
func (User) TableName() string { return "users" }

// Channel maps the channels table. CreatorID is nullable per the schema.
type Channel struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	Type      int16     `gorm:"not null"`
	Name      string    `gorm:"size:100;not null;default:''"`
	AvatarURL string    `gorm:"column:avatar_url;not null;default:''"`
	Seq       int64     `gorm:"not null;default:0"`
	CreatorID *int64    `gorm:"column:creator_id"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`
}

// TableName pins the GORM-derived table name to the migration.
func (Channel) TableName() string { return "channels" }

// ChannelMember maps the channel_members table — composite PK on (user_id,
// channel_id). PhantomAtRead matches the column added in the actual schema.
type ChannelMember struct {
	UserID        int64     `gorm:"column:user_id;primaryKey"`
	ChannelID     int64     `gorm:"column:channel_id;primaryKey"`
	Role          int16     `gorm:"not null;default:1"`
	LastReadSeq   int64     `gorm:"column:last_read_seq;not null;default:0"`
	PhantomCount  int64     `gorm:"column:phantom_count;not null;default:0"`
	PhantomAtRead int64     `gorm:"column:phantom_at_read;not null;default:0"`
	JoinedAt      time.Time `gorm:"column:joined_at;not null;default:now()"`
}

// TableName pins the GORM-derived table name to the migration.
func (ChannelMember) TableName() string { return "channel_members" }

// Message maps the messages table. ClientMsgID, ReplyTo, ForwardedFrom are
// nullable; VisibleTo is a Postgres BIGINT[] handled by pq.Int64Array.
type Message struct {
	ID            int64         `gorm:"primaryKey;autoIncrement"`
	ChannelID     int64         `gorm:"column:channel_id;not null"`
	Seq           int64         `gorm:"not null"`
	ClientMsgID   string        `gorm:"column:client_msg_id;size:36"`
	SenderID      int64         `gorm:"column:sender_id;not null"`
	MsgType       int16         `gorm:"column:msg_type;not null;default:1"`
	Content       string        `gorm:"not null;default:''"`
	VisibleTo     pq.Int64Array `gorm:"column:visible_to;type:bigint[]"`
	ReplyTo       *int64        `gorm:"column:reply_to"`
	ForwardedFrom *int64        `gorm:"column:forwarded_from"`
	CreatedAt     time.Time     `gorm:"column:created_at;not null;default:now()"`
}

// TableName pins the GORM-derived table name to the migration.
func (Message) TableName() string { return "messages" }

// Friendship maps the friendships table. The PK is the surrogate id; the
// (requester_id, addressee_id) pair carries a unique constraint.
type Friendship struct {
	ID          int64     `gorm:"primaryKey;autoIncrement"`
	RequesterID int64     `gorm:"column:requester_id;not null;uniqueIndex:uq_friendships_pair,priority:1"`
	AddresseeID int64     `gorm:"column:addressee_id;not null;uniqueIndex:uq_friendships_pair,priority:2"`
	Status      int16     `gorm:"not null;default:1"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt   time.Time `gorm:"column:updated_at;not null;default:now()"`
}

// TableName pins the GORM-derived table name to the migration.
func (Friendship) TableName() string { return "friendships" }

// File maps the files table.
type File struct {
	ID            int64     `gorm:"primaryKey;autoIncrement"`
	UploaderID    int64     `gorm:"column:uploader_id;not null"`
	FileName      string    `gorm:"column:file_name;size:255;not null"`
	FileSize      int64     `gorm:"column:file_size;not null"`
	MimeType      string    `gorm:"column:mime_type;size:100;not null;default:''"`
	StoragePath   string    `gorm:"column:storage_path;not null"`
	ThumbnailPath string    `gorm:"column:thumbnail_path;not null;default:''"`
	CreatedAt     time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName pins the GORM-derived table name to the migration.
func (File) TableName() string { return "files" }

// MessageAttachment maps the message_attachments join table (composite PK).
type MessageAttachment struct {
	MessageID int64 `gorm:"column:message_id;primaryKey"`
	FileID    int64 `gorm:"column:file_id;primaryKey"`
}

// TableName pins the GORM-derived table name to the migration.
func (MessageAttachment) TableName() string { return "message_attachments" }

// MessageFavorite maps the message_favorites join table (composite PK).
type MessageFavorite struct {
	UserID    int64     `gorm:"column:user_id;primaryKey"`
	MessageID int64     `gorm:"column:message_id;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName pins the GORM-derived table name to the migration.
func (MessageFavorite) TableName() string { return "message_favorites" }

// UserSettings maps the user_settings table. SettingsJSON is stored opaquely
// as a JSONB string — callers marshal/unmarshal at the boundary.
type UserSettings struct {
	UserID              int64     `gorm:"column:user_id;primaryKey"`
	NotificationEnabled bool      `gorm:"column:notification_enabled;not null;default:true"`
	Theme               string    `gorm:"size:20;not null;default:'light'"`
	Language            string    `gorm:"size:10;not null;default:'zh-CN'"`
	SettingsJSON        string    `gorm:"column:settings_json;type:jsonb;not null;default:'{}'"`
	UpdatedAt           time.Time `gorm:"column:updated_at;not null;default:now()"`
}

// TableName pins the GORM-derived table name to the migration.
func (UserSettings) TableName() string { return "user_settings" }
