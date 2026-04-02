package model

import "time"

type FriendshipStatus int16

const (
	FriendshipPending  FriendshipStatus = 1
	FriendshipAccepted FriendshipStatus = 2
	FriendshipRejected FriendshipStatus = 3
	FriendshipBlocked  FriendshipStatus = 4
)

type Friendship struct {
	ID          int64            `json:"id"`
	RequesterID int64            `json:"requester_id"`
	AddresseeID int64            `json:"addressee_id"`
	Status      FriendshipStatus `json:"status"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}
