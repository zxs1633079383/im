package model

import "time"

type ChannelType int16

const (
	ChannelTypeDM    ChannelType = 1
	ChannelTypeGroup ChannelType = 2
)

type MemberRole int16

const (
	MemberRoleMember MemberRole = 1
	MemberRoleAdmin  MemberRole = 2
	MemberRoleOwner  MemberRole = 3
)

type Channel struct {
	ID        int64       `json:"id"`
	Type      ChannelType `json:"type"`
	Name      string      `json:"name"`
	AvatarURL string      `json:"avatar_url"`
	Seq       int64       `json:"seq"`
	CreatorID *int64      `json:"creator_id"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type ChannelMember struct {
	UserID        int64      `json:"user_id"`
	ChannelID     int64      `json:"channel_id"`
	Role          MemberRole `json:"role"`
	LastReadSeq   int64      `json:"last_read_seq"`
	PhantomCount  int64      `json:"phantom_count"`
	PhantomAtRead int64      `json:"phantom_at_read"`
	JoinedAt      time.Time  `json:"joined_at"`
}

func (m *ChannelMember) UnreadCount(channelSeq int64) int64 {
	unread := (channelSeq - m.LastReadSeq) - (m.PhantomCount - m.PhantomAtRead)
	if unread < 0 {
		return 0
	}
	return unread
}
