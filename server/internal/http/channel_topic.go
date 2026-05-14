package http

import (
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// createTopicReq mirrors the JSON body for POST /api/channels/:id/topics.
// Field names are kept snake_case to match the rest of this handler family.
type createTopicReq struct {
	RootMessageID string   `json:"root_message_id"`
	Name          string   `json:"name"`
	MemberUserIDs []string `json:"member_user_ids"`
}

// registerTopicRoutes wires POST/GET /api/channels/:id/topics.
// Called from RegisterChannelRoutes so the topic endpoints live in the same
// auth scope as the rest of the channel API.
func registerTopicRoutes(authed *gin.RouterGroup, svc *service.ChannelService) {
	authed.POST("/channels/:id/topics", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		parentID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var in createTopicReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.Name == "" {
			c.JSON(422, gin.H{"error": "name is required"})
			return
		}
		teamID := teamIDFromCtx(c)
		var teamPtr *string
		if teamID != "" {
			teamPtr = &teamID
		}
		topic, err := svc.CreateTopic(c.Request.Context(), service.CreateTopicRequest{
			CallerID:      uid,
			TeamID:        teamPtr,
			ParentID:      parentID,
			RootMessageID: in.RootMessageID,
			Name:          in.Name,
			MemberIDs:     in.MemberUserIDs,
		})
		writeTopicCreateResponse(c, topic, err)
	})

	authed.GET("/channels/:id/topics", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		parentID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		topics, err := svc.ListTopics(c.Request.Context(), uid, parentID)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if topics == nil {
				topics = []repo.Channel{}
			}
			c.JSON(200, topics)
		}
	})
}

// writeTopicCreateResponse keeps the create-endpoint error mapping short so
// the closure above reads in one screen.
func writeTopicCreateResponse(c *gin.Context, topic interface{}, err error) {
	switch {
	case errors.Is(err, service.ErrNotMember):
		c.JSON(403, gin.H{"error": "not a member of parent channel"})
	case err != nil:
		c.JSON(422, gin.H{"error": err.Error()})
	default:
		c.JSON(201, topic)
	}
}
