package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
)

// P2.3 单测 — ChannelService.CreateTopic 在 channelEvent 挂载时必须把
// channel INSERT + CreateChannelSequences 跑在同一笔 WithinTx 内；否则
// topic 创建后首条消息会撞 `relation "channel_msg_seq_<uuid>" does not
// exist`（C018 §3.2 / P3 NEED_FOLLOWUP / P2.3 NEED_FURTHER_FOLLOWUP）。
//
// 老路径（channelEvent == nil）由现有 m4_topic_test.go 集成测试覆盖，
// 这里不再重复——核心 contract 是：channelEvent 接上后走新事务路径，
// CreateChannelSequences 必然以 tx + topicID 调用一次。
//
// 复用 channel_create_sequences_test.go 里的 withinTxRunAndReturn 帮手
// 来同步驱动 WithinTx 闭包；用 RunAndReturn 而非 Run+Return 是为了避免
// 把闭包跑两遍（Run 跑闭包 + Return 再 stub 返回值会双触发内部 mock）。

const (
	ttParentID = "01J0TOPIC_PARENT0000000000"
	ttCaller   = "01J0TOPIC_CALLER0000000000"
	ttMember1  = "01J0TOPIC_MEMBER1000000000"
	ttTopicID  = "01J0TOPIC_CHILD00000000000"
)

func TestCreateTopic_HappyPath_CallsCreateChannelSequences(t *testing.T) {
	chanMock := mocks.NewChannelRepoMock(t)
	evtMock := mocks.NewChannelEventRepoMock(t)

	// 1. 鉴权：caller 是 parent 的成员
	chanMock.On("GetMember", mock.Anything, ttParentID, ttCaller).
		Return(&repo.ChannelMember{ChannelID: ttParentID, UserID: ttCaller, Role: repo.MemberRoleOwner}, nil).Once()
	// 2. 子集校验：member1 也是 parent 的成员
	chanMock.On("ListMembers", mock.Anything, ttParentID).
		Return([]repo.ChannelMember{
			{ChannelID: ttParentID, UserID: ttCaller, Role: repo.MemberRoleOwner},
			{ChannelID: ttParentID, UserID: ttMember1, Role: repo.MemberRoleMember},
		}, nil).Once()
	// 3. WithinTx 同步驱动闭包一次
	chanMock.On("WithinTx", mock.Anything, mock.AnythingOfType("func(*gorm.DB) error")).
		Return(withinTxRunAndReturn).Once()
	// 4. CreateTopicTx 在 tx 内 INSERT topic + 成员，回填 ID
	chanMock.On("CreateTopicTx", mock.Anything, mock.Anything, mock.AnythingOfType("repo.CreateTopicParams")).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(repo.CreateTopicParams)
			require.Equal(t, ttParentID, params.ParentID)
			require.Equal(t, ttCaller, params.CreatorID)
			require.Equal(t, []string{ttMember1}, params.MemberIDs)
		}).
		Return(&repo.Channel{ID: ttTopicID, Type: repo.ChannelTypeGroup, CreatorID: ttCaller}, nil).Once()
	// 5. CreateChannelSequences 必须在同一 tx 内、且用 post-INSERT 的 topicID
	//    —— 这正是 P2.3 NEED_FURTHER_FOLLOWUP 标记的 gap
	evtMock.On("CreateChannelSequences", mock.Anything, mock.Anything, ttTopicID).
		Return(nil).Once()

	svc := NewChannelService(chanMock, nil) // messages == nil → postSys 短路
	svc.AttachChannelEventRepo(evtMock)

	topic, err := svc.CreateTopic(context.Background(), CreateTopicRequest{
		CallerID:      ttCaller,
		ParentID:      ttParentID,
		RootMessageID: "01J0TOPIC_ROOT_MSG00000000",
		Name:          "t1",
		MemberIDs:     []string{ttMember1},
	})
	require.NoError(t, err)
	require.NotNil(t, topic)
	require.Equal(t, ttTopicID, topic.ID)
}

func TestCreateTopic_SequenceFailRollsBack(t *testing.T) {
	chanMock := mocks.NewChannelRepoMock(t)
	evtMock := mocks.NewChannelEventRepoMock(t)

	chanMock.On("GetMember", mock.Anything, ttParentID, ttCaller).
		Return(&repo.ChannelMember{ChannelID: ttParentID, UserID: ttCaller, Role: repo.MemberRoleOwner}, nil).Once()
	// MemberIDs 非空 → ensureMembersSubset 会调一次 ListMembers
	chanMock.On("ListMembers", mock.Anything, ttParentID).
		Return([]repo.ChannelMember{
			{ChannelID: ttParentID, UserID: ttCaller, Role: repo.MemberRoleOwner},
			{ChannelID: ttParentID, UserID: ttMember1, Role: repo.MemberRoleMember},
		}, nil).Once()
	// WithinTx 把闭包错误透传给 caller —— 真实 PG 事务在此处 ROLLBACK
	// （channel 行 + 成员 INSERT 一起回滚）。用 RunAndReturn 单次驱动。
	chanMock.On("WithinTx", mock.Anything, mock.AnythingOfType("func(*gorm.DB) error")).
		Return(withinTxRunAndReturn).Once()
	chanMock.On("CreateTopicTx", mock.Anything, mock.Anything, mock.AnythingOfType("repo.CreateTopicParams")).
		Return(&repo.Channel{ID: ttTopicID}, nil).Once()
	// CreateChannelSequences 爆，闭包应立即返回 wrapped error
	evtMock.On("CreateChannelSequences", mock.Anything, mock.Anything, ttTopicID).
		Return(errors.New("pg sequence blew up")).Once()

	svc := NewChannelService(chanMock, nil)
	svc.AttachChannelEventRepo(evtMock)

	_, err := svc.CreateTopic(context.Background(), CreateTopicRequest{
		CallerID:      ttCaller,
		ParentID:      ttParentID,
		RootMessageID: "01J0TOPIC_ROOT_MSG00000000",
		Name:          "t1",
		MemberIDs:     []string{ttMember1},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "create topic sequences")
	require.Contains(t, err.Error(), "pg sequence blew up")
}

// channelEvent 未挂载时退化到原 CreateTopic 路径，保持向后兼容；不应
// 触发 CreateTopicTx / CreateChannelSequences / WithinTx。MemberIDs 为空
// 时 ensureMembersSubset 直接 return，因此不需要 mock ListMembers。
func TestCreateTopic_NoChannelEvent_FallsBackToLegacyPath(t *testing.T) {
	chanMock := mocks.NewChannelRepoMock(t)

	chanMock.On("GetMember", mock.Anything, ttParentID, ttCaller).
		Return(&repo.ChannelMember{ChannelID: ttParentID, UserID: ttCaller, Role: repo.MemberRoleOwner}, nil).Once()
	// 老路径：直接走 CreateTopic（不挂 WithinTx / CreateTopicTx）
	chanMock.On("CreateTopic", mock.Anything, mock.AnythingOfType("repo.CreateTopicParams")).
		Return(&repo.Channel{ID: ttTopicID, Type: repo.ChannelTypeGroup}, nil).Once()

	svc := NewChannelService(chanMock, nil)
	// 不调 AttachChannelEventRepo

	topic, err := svc.CreateTopic(context.Background(), CreateTopicRequest{
		CallerID:      ttCaller,
		ParentID:      ttParentID,
		RootMessageID: "01J0TOPIC_ROOT_MSG00000000",
		Name:          "t1",
	})
	require.NoError(t, err)
	require.Equal(t, ttTopicID, topic.ID)
}
