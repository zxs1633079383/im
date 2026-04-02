package model

import "time"

type MsgType int16

const (
	MsgTypeText    MsgType = 1
	MsgTypeImage   MsgType = 2
	MsgTypeFile    MsgType = 3
	MsgTypeSystem  MsgType = 4
	MsgTypePhantom MsgType = 99
)

type Message struct {
	ID            int64   `json:"id"`
	ChannelID     int64   `json:"channel_id"`
	Seq           int64   `json:"seq"`
	ClientMsgID   string  `json:"client_msg_id,omitempty"`
	SenderID      int64   `json:"sender_id"`
	MsgType       MsgType `json:"msg_type"`
	Content       string  `json:"content"`
	VisibleTo     []int64 `json:"visible_to,omitempty"`
	ReplyTo       *int64  `json:"reply_to,omitempty"`
	ForwardedFrom *int64  `json:"forwarded_from,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

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

type Phantom struct {
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	ChannelID int64  `json:"channel_id"`
}

func NewPhantom(channelID, seq int64) Phantom {
	return Phantom{
		Seq:       seq,
		Type:      "phantom",
		ChannelID: channelID,
	}
}

type File struct {
	ID            int64     `json:"id"`
	UploaderID    int64     `json:"uploader_id"`
	FileName      string    `json:"file_name"`
	FileSize      int64     `json:"file_size"`
	MimeType      string    `json:"mime_type"`
	StoragePath   string    `json:"-"`
	ThumbnailPath string    `json:"thumbnail_path,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type MessageFavorite struct {
	UserID    int64     `json:"user_id"`
	MessageID int64     `json:"message_id"`
	CreatedAt time.Time `json:"created_at"`
}

// MessageSearchResult extends Message with the channel name for display.
type MessageSearchResult struct {
	Message
	ChannelName string `json:"channel_name"`
}
