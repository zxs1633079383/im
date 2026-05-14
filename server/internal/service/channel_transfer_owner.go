package service

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"im-server/internal/repo"
)

// C013 — POST /api/channels/:id/transfer-owner. The endpoint moves group
// ownership from the caller (the current owner) to a target member. The two
// role flips, the channels.creator_id update, the (optional) leave, and the
// 1-or-2 system messages all run inside a single transaction so /api/sync
// replays a coherent order even for offline clients.

// TransferOwnerParams carries the inputs of ChannelService.TransferOwner.
//
//   - ChannelID: group channel id (DM type=1 rejected with ErrDMNoOwner).
//   - CallerID:  mm UserID of the current owner (must hold role=Owner).
//   - NewOwnerID: mm UserID of the new owner; must already be a member.
//   - AlsoLeave: when true the old owner is removed from the channel in the
//     same transaction (matches the "owner clicks 退出群组 → pick new owner"
//     flow described in cses-client featdoc 08).
type TransferOwnerParams struct {
	ChannelID  string
	CallerID   string
	NewOwnerID string
	AlsoLeave  bool
}

// TransferOwnerResult is the payload returned to the HTTP layer + the cses-
// client. Members is the post-change roster snapshot so the client can replace
// local membership in one pass (same shape as channel_member_updated WS).
type TransferOwnerResult struct {
	Channel    *repo.Channel               `json:"channel"`
	Members    []ChannelMemberSummary      `json:"members"`
	OldOwnerID string                      `json:"old_owner_id"`
	NewOwnerID string                      `json:"new_owner_id"`
}

// C013 sentinels. The HTTP transport maps them via errors.Is.
var (
	// ErrDMNoOwner is returned when the request targets a DM channel
	// (type=1). DMs have no owner concept; the call must be rejected up
	// front with 400.
	ErrDMNoOwner = errors.New("DM channels have no owner")
	// ErrTransferToSelf is the service-side guard mirroring the handler-side
	// "cannot transfer to self" pre-check. The handler short-circuits earlier;
	// this sentinel exists so non-HTTP callers (CLI / scripts / future tests)
	// get a typed error rather than a bare 422.
	ErrTransferToSelf = errors.New("cannot transfer ownership to self")
)

// TransferOwner swaps channel ownership from the caller to NewOwnerID. The 7-
// step transaction follows docs/harness/C013-owner-transfer-endpoint.md §3.3:
//
//  1. Assert caller is the current Owner.
//  2. Assert NewOwnerID is a member of the channel.
//  3. Reject DM channels (type=1) up front.
//  4. Swap channel_members.role: old owner → Member, new owner → Owner.
//  5. Update channels.creator_id so the column matches the new role layout.
//  6. When AlsoLeave: remove old owner from channel_members + emit a second
//     system message (member_left) inside the SAME transaction.
//  7. Emit a system message (sys_type=owner_transferred) carrying both IDs so
//     /api/sync replays the event to offline clients.
//
// Post-commit, fanMemberUpdate broadcasts a channel_member_updated WS frame
// with change_type=owner_transfer (+ a follow-up leave frame when AlsoLeave).
func (s *ChannelService) TransferOwner(
	ctx context.Context, p TransferOwnerParams,
) (*TransferOwnerResult, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.TransferOwner")
	defer span.End()

	if p.CallerID == p.NewOwnerID {
		return nil, ErrTransferToSelf
	}

	// Validate caller is the current owner up-front (also surfaces ErrNotMember
	// when caller isn't even in the channel) — keeps the tx body tight.
	if err := s.requireOwner(ctx, p.ChannelID, p.CallerID); err != nil {
		return nil, err
	}

	// Channel-level checks: existence + DM rejection.
	ch, err := s.channels.GetByID(ctx, p.ChannelID)
	if err != nil {
		return nil, err
	}
	if ch.Type == repo.ChannelTypeDM {
		return nil, ErrDMNoOwner
	}
	if ch.DeletedAt != nil {
		// Soft-closed channels can't be transferred — symmetric with
		// CloseChannel's ErrGone semantics; surfaced as 410 by the handler.
		return nil, repo.ErrGone
	}

	// Validate new owner is a member. We do this AFTER the channel fetch so the
	// error precedence matches "missing channel → 404, then membership checks".
	if _, err := s.channels.GetMember(ctx, p.ChannelID, p.NewOwnerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrTargetNotMember
		}
		return nil, err
	}

	if err := s.runTransferOwnerTx(ctx, ch, p); err != nil {
		return nil, err
	}

	// Refresh channel snapshot (creator_id changed) + list members for the
	// caller's response body. Outside the tx so reads see the committed state.
	updatedCh, err := s.channels.GetByID(ctx, p.ChannelID)
	if err != nil {
		return nil, err
	}
	members, err := s.channels.ListMembers(ctx, p.ChannelID)
	if err != nil {
		return nil, err
	}
	summaries := summariesFromMembers(members)

	// Fan WS frames AFTER the commit so receivers never see a state the DB
	// hasn't persisted. owner_transfer goes first; the optional leave follows
	// so the cses-client sees the same ordering /api/sync replays.
	s.fanMemberUpdate(ctx, p.ChannelID, MemberChangeOwnerTransfer,
		p.CallerID, p.NewOwnerID, "")
	if p.AlsoLeave {
		s.fanMemberUpdate(ctx, p.ChannelID, MemberChangeLeave,
			p.CallerID, p.CallerID, "")
	}

	return &TransferOwnerResult{
		Channel:    updatedCh,
		Members:    summaries,
		OldOwnerID: p.CallerID,
		NewOwnerID: p.NewOwnerID,
	}, nil
}

// runTransferOwnerTx encapsulates the atomic mutations so TransferOwner stays
// under the 50-line per-function cap.
func (s *ChannelService) runTransferOwnerTx(
	ctx context.Context, ch *repo.Channel, p TransferOwnerParams,
) error {
	return s.channels.WithinTx(ctx, func(tx *gorm.DB) error {
		// Step 4: swap roles. Old owner first (so an interim "two owners"
		// state never exists between the two writes).
		if err := s.channels.SetMemberRoleTx(ctx, tx,
			p.ChannelID, p.CallerID, repo.MemberRoleMember); err != nil {
			return fmt.Errorf("demote old owner: %w", err)
		}
		if err := s.channels.SetMemberRoleTx(ctx, tx,
			p.ChannelID, p.NewOwnerID, repo.MemberRoleOwner); err != nil {
			return fmt.Errorf("promote new owner: %w", err)
		}
		// Step 5: keep channels.creator_id in lock-step.
		if err := s.channels.SetCreatorTx(ctx, tx,
			p.ChannelID, p.NewOwnerID); err != nil {
			return fmt.Errorf("set creator: %w", err)
		}
		// Step 7a: owner_transferred system message (always).
		if err := s.postSys(ctx, tx, p.ChannelID, p.CallerID, ch.TeamID,
			ownerTransferredProps(p.CallerID, p.NewOwnerID)); err != nil {
			return err
		}
		// Step 6+7b: optional "owner also leaves" path.
		if p.AlsoLeave {
			if err := s.postSys(ctx, tx, p.ChannelID, p.CallerID, ch.TeamID,
				memberLeftProps(p.CallerID)); err != nil {
				return err
			}
			if err := s.channels.RemoveMemberTx(ctx, tx,
				p.ChannelID, p.CallerID); err != nil {
				return fmt.Errorf("remove old owner: %w", err)
			}
		}
		return nil
	})
}

// summariesFromMembers projects []repo.ChannelMember to []ChannelMemberSummary
// — same shape fanMemberUpdate uses, lifted here so the HTTP response body
// matches the WS payload (cses-client can switch on either source).
func summariesFromMembers(members []repo.ChannelMember) []ChannelMemberSummary {
	out := make([]ChannelMemberSummary, 0, len(members))
	for _, m := range members {
		out = append(out, ChannelMemberSummary{
			UserID:     m.UserID,
			Role:       m.Role,
			NickName:   m.NickName,
			IsTop:      m.IsTop,
			NotifyPref: m.NotifyPref,
		})
	}
	return out
}
