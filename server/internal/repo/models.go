package repo

import (
	"time"

	"github.com/lib/pq"
)

// User maps the users table.
type User struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"                                json:"id"`
	Username     string    `gorm:"size:50;uniqueIndex;not null"                            json:"username"`
	Email        string    `gorm:"size:255;uniqueIndex;not null"                           json:"email"`
	PasswordHash string    `gorm:"column:password_hash;size:255;not null"                  json:"-"`
	DisplayName  string    `gorm:"column:display_name;size:100;not null;default:''"        json:"display_name"`
	AvatarURL    string    `gorm:"column:avatar_url;not null;default:''"                   json:"avatar_url"`
	Status       int16     `gorm:"not null;default:1"                                      json:"status"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;default:now()"                json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null;default:now()"                json:"updated_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (User) TableName() string { return "users" }

// Channel maps the channels table. CreatorID is nullable per the schema.
//
// M2 fine-grained fields (Mattermost /channel/change/* alignment):
//   - Notice/Purpose/PictureURL: descriptive text
//   - Props: JSONB blob for arbitrary custom props (stored as a raw string
//     so callers decide whether to json.RawMessage / map / struct-decode it)
//   - Orient: small tag (0=default / 1=left / 2=right ... callers define)
//   - Permission: 0=open 1=approval 2=closed
//   - IsTop: channel pin/priority flag
type Channel struct {
	ID         int64     `gorm:"primaryKey;autoIncrement"                                json:"id"`
	Type       int16     `gorm:"not null"                                                json:"type"`
	Name       string    `gorm:"size:100;not null;default:''"                            json:"name"`
	AvatarURL  string    `gorm:"column:avatar_url;not null;default:''"                   json:"avatar_url"`
	Seq        int64     `gorm:"not null;default:0"                                      json:"seq"`
	CreatorID  *int64    `gorm:"column:creator_id"                                       json:"creator_id"`
	Notice     string    `gorm:"column:notice;not null;default:''"                       json:"notice"`
	Purpose    string    `gorm:"column:purpose;not null;default:''"                      json:"purpose"`
	PictureURL string    `gorm:"column:picture_url;not null;default:''"                  json:"picture_url"`
	Props      string    `gorm:"column:props;type:jsonb;not null;default:'{}'"           json:"props"`
	Orient     int16     `gorm:"column:orient;not null;default:0"                        json:"orient"`
	Permission int16     `gorm:"column:permission;not null;default:0"                    json:"permission"`
	IsTop      bool      `gorm:"column:is_top;not null;default:false"                    json:"is_top"`
	CreatedAt  time.Time `gorm:"column:created_at;not null;default:now()"                json:"created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at;not null;default:now()"                json:"updated_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (Channel) TableName() string { return "channels" }

// ChannelMember maps the channel_members table — composite PK on (user_id,
// channel_id). PhantomAtRead matches the column added in the actual schema.
//
// M2: NotifyPref is 0=all 1=mentions 2=none; used by the broadcaster to
// decide whether to deliver a given message/event to this member.
type ChannelMember struct {
	UserID        int64     `gorm:"column:user_id;primaryKey"                               json:"user_id"`
	ChannelID     int64     `gorm:"column:channel_id;primaryKey"                            json:"channel_id"`
	Role          int16     `gorm:"not null;default:1"                                      json:"role"`
	LastReadSeq   int64     `gorm:"column:last_read_seq;not null;default:0"                 json:"last_read_seq"`
	PhantomCount  int64     `gorm:"column:phantom_count;not null;default:0"                 json:"phantom_count"`
	PhantomAtRead int64     `gorm:"column:phantom_at_read;not null;default:0"               json:"phantom_at_read"`
	NotifyPref    int16     `gorm:"column:notify_pref;not null;default:0"                   json:"notify_pref"`
	JoinedAt      time.Time `gorm:"column:joined_at;not null;default:now()"                 json:"joined_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (ChannelMember) TableName() string { return "channel_members" }

// Message maps the messages table. ClientMsgID, ReplyTo, ForwardedFrom are
// nullable; VisibleTo is a Postgres BIGINT[] handled by pq.Int64Array.
//
// Deleted/DeletedAt track soft-delete (M1 revoke); UpdatedAt tracks edit (M1 edit).
type Message struct {
	ID            int64         `gorm:"primaryKey;autoIncrement"                                json:"id"`
	ChannelID     int64         `gorm:"column:channel_id;not null"                              json:"channel_id"`
	Seq           int64         `gorm:"not null"                                                json:"seq"`
	ClientMsgID   string        `gorm:"column:client_msg_id;size:36"                            json:"client_msg_id,omitempty"`
	SenderID      int64         `gorm:"column:sender_id;not null"                               json:"sender_id"`
	MsgType       int16         `gorm:"column:msg_type;not null;default:1"                      json:"msg_type"`
	Content       string        `gorm:"not null;default:''"                                     json:"content"`
	VisibleTo     pq.Int64Array `gorm:"column:visible_to;type:bigint[]"                         json:"visible_to,omitempty"`
	ReplyTo       *int64        `gorm:"column:reply_to"                                         json:"reply_to,omitempty"`
	ForwardedFrom *int64        `gorm:"column:forwarded_from"                                   json:"forwarded_from,omitempty"`
	CreatedAt     time.Time     `gorm:"column:created_at;not null;default:now()"                json:"created_at"`
	UpdatedAt     *time.Time    `gorm:"column:updated_at"                                       json:"updated_at,omitempty"`
	Deleted       bool          `gorm:"column:deleted;not null;default:false"                   json:"deleted,omitempty"`
	DeletedAt     *time.Time    `gorm:"column:deleted_at"                                       json:"deleted_at,omitempty"`
	IsUrgent      bool          `gorm:"column:is_urgent;not null;default:false"                 json:"is_urgent,omitempty"`
}

// TableName pins the GORM-derived table name to the migration.
func (Message) TableName() string { return "messages" }

// IsVisibleTo reports whether userID can see this message. Broadcast messages
// (VisibleTo == nil) are visible to all members; directed messages are visible
// only to the listed user IDs.
func (m *Message) IsVisibleTo(userID int64) bool {
	if m.VisibleTo == nil {
		return true
	}
	for _, id := range m.VisibleTo {
		if id == userID {
			return true
		}
	}
	return false
}

// MsgType constants (mirror internal/model.MsgType).
const (
	MsgTypeText    int16 = 1
	MsgTypeImage   int16 = 2
	MsgTypeFile    int16 = 3
	MsgTypeSystem  int16 = 4
	MsgTypePhantom int16 = 99
)

// User status constants (mirror internal/model.UserStatus).
const (
	UserStatusActive   int16 = 1
	UserStatusDisabled int16 = 2
)

// Friendship maps the friendships table. The PK is the surrogate id; the
// (requester_id, addressee_id) pair carries a unique constraint.
type Friendship struct {
	ID          int64     `gorm:"primaryKey;autoIncrement"                                                          json:"id"`
	RequesterID int64     `gorm:"column:requester_id;not null;uniqueIndex:uq_friendships_pair,priority:1"           json:"requester_id"`
	AddresseeID int64     `gorm:"column:addressee_id;not null;uniqueIndex:uq_friendships_pair,priority:2"           json:"addressee_id"`
	Status      int16     `gorm:"not null;default:1"                                                                json:"status"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;default:now()"                                          json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at;not null;default:now()"                                          json:"updated_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (Friendship) TableName() string { return "friendships" }

// File maps the files table.
type File struct {
	ID            int64     `gorm:"primaryKey;autoIncrement"                                json:"id"`
	UploaderID    int64     `gorm:"column:uploader_id;not null"                             json:"uploader_id"`
	FileName      string    `gorm:"column:file_name;size:255;not null"                      json:"file_name"`
	FileSize      int64     `gorm:"column:file_size;not null"                               json:"file_size"`
	MimeType      string    `gorm:"column:mime_type;size:100;not null;default:''"           json:"mime_type"`
	StoragePath   string    `gorm:"column:storage_path;not null"                            json:"-"`
	ThumbnailPath string    `gorm:"column:thumbnail_path;not null;default:''"               json:"thumbnail_path,omitempty"`
	CreatedAt     time.Time `gorm:"column:created_at;not null;default:now()"                json:"created_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (File) TableName() string { return "files" }

// MessageAttachment maps the message_attachments join table (composite PK).
type MessageAttachment struct {
	MessageID int64 `gorm:"column:message_id;primaryKey"                                json:"message_id"`
	FileID    int64 `gorm:"column:file_id;primaryKey"                                   json:"file_id"`
}

// TableName pins the GORM-derived table name to the migration.
func (MessageAttachment) TableName() string { return "message_attachments" }

// MessageFavorite maps the message_favorites join table (composite PK).
type MessageFavorite struct {
	UserID    int64     `gorm:"column:user_id;primaryKey"                                 json:"user_id"`
	MessageID int64     `gorm:"column:message_id;primaryKey"                              json:"message_id"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"                  json:"created_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (MessageFavorite) TableName() string { return "message_favorites" }

// ChannelManager maps the channel_managers table — fine-grained manager
// rights on a channel. A manager has admin rights beyond "member" but less
// than "owner" (only owners can add/remove managers).
type ChannelManager struct {
	ChannelID int64     `gorm:"column:channel_id;primaryKey"                              json:"channel_id"`
	UserID    int64     `gorm:"column:user_id;primaryKey"                                 json:"user_id"`
	AddedBy   int64     `gorm:"column:added_by;not null"                                  json:"added_by"`
	AddedAt   time.Time `gorm:"column:added_at;not null;default:now()"                    json:"added_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (ChannelManager) TableName() string { return "channel_managers" }

// ChannelPinnedMessage maps channel_pinned_messages — a channel-scoped pin
// table. Composite PK on (channel_id, message_id).
type ChannelPinnedMessage struct {
	ChannelID int64     `gorm:"column:channel_id;primaryKey"                              json:"channel_id"`
	MessageID int64     `gorm:"column:message_id;primaryKey"                              json:"message_id"`
	PinnedBy  int64     `gorm:"column:pinned_by;not null"                                 json:"pinned_by"`
	PinnedAt  time.Time `gorm:"column:pinned_at;not null;default:now()"                   json:"pinned_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (ChannelPinnedMessage) TableName() string { return "channel_pinned_messages" }

// Channel member notify_pref constants.
const (
	NotifyPrefAll      int16 = 0
	NotifyPrefMentions int16 = 1
	NotifyPrefNone     int16 = 2
)

// Channel permission constants.
const (
	ChannelPermissionOpen     int16 = 0
	ChannelPermissionApproval int16 = 1
	ChannelPermissionClosed   int16 = 2
)

// UserSettings maps the user_settings table. SettingsJSON is stored opaquely
// as a JSONB string — callers marshal/unmarshal at the boundary.
type UserSettings struct {
	UserID              int64     `gorm:"column:user_id;primaryKey"                            json:"user_id"`
	NotificationEnabled bool      `gorm:"column:notification_enabled;not null;default:true"    json:"notification_enabled"`
	Theme               string    `gorm:"size:20;not null;default:'light'"                     json:"theme"`
	Language            string    `gorm:"size:10;not null;default:'zh-CN'"                     json:"language"`
	SettingsJSON        string    `gorm:"column:settings_json;type:jsonb;not null;default:'{}'" json:"settings_json"`
	UpdatedAt           time.Time `gorm:"column:updated_at;not null;default:now()"             json:"updated_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (UserSettings) TableName() string { return "user_settings" }
