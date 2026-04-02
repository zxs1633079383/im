package model

import "testing"

func TestChannelMember_UnreadCount(t *testing.T) {
	tests := []struct {
		name       string
		member     ChannelMember
		channelSeq int64
		want       int64
	}{
		{
			name:       "no unread",
			member:     ChannelMember{LastReadSeq: 100, PhantomCount: 5, PhantomAtRead: 5},
			channelSeq: 100,
			want:       0,
		},
		{
			name:       "3 normal unread",
			member:     ChannelMember{LastReadSeq: 100, PhantomCount: 5, PhantomAtRead: 5},
			channelSeq: 103,
			want:       3,
		},
		{
			name:       "unread with phantom excluded",
			member:     ChannelMember{LastReadSeq: 100, PhantomCount: 6, PhantomAtRead: 5},
			channelSeq: 104,
			want:       3,
		},
		{
			name:       "after read",
			member:     ChannelMember{LastReadSeq: 106, PhantomCount: 6, PhantomAtRead: 6},
			channelSeq: 106,
			want:       0,
		},
		{
			name:       "visible directed message",
			member:     ChannelMember{LastReadSeq: 106, PhantomCount: 6, PhantomAtRead: 6},
			channelSeq: 107,
			want:       1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.member.UnreadCount(tt.channelSeq)
			if got != tt.want {
				t.Errorf("UnreadCount(%d) = %d, want %d", tt.channelSeq, got, tt.want)
			}
		})
	}
}

func TestMessage_IsVisibleTo(t *testing.T) {
	tests := []struct {
		name      string
		visibleTo []int64
		userID    int64
		want      bool
	}{
		{"nil means visible to all", nil, 999, true},
		{"in visible list", []int64{1, 2, 3}, 2, true},
		{"not in visible list", []int64{1, 2, 3}, 4, false},
		{"empty list means no one", []int64{}, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Message{VisibleTo: tt.visibleTo}
			if got := m.IsVisibleTo(tt.userID); got != tt.want {
				t.Errorf("IsVisibleTo(%d) = %v, want %v", tt.userID, got, tt.want)
			}
		})
	}
}
