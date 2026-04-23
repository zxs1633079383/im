package http

import (
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// searchResponse mirrors the legacy handler.searchResponse exactly. Pointer
// slices + `omitempty` preserve the wire shape: a category that wasn't
// requested is *absent* from the JSON envelope, while a requested category
// with no matches is emitted as an empty array. Existing clients already
// rely on this distinction.
type searchResponse struct {
	Messages *[]repo.MessageSearchResult `json:"messages,omitempty"`
	Users    *[]repo.User                `json:"users,omitempty"`
	Channels *[]repo.Channel             `json:"channels,omitempty"`
}

// RegisterSearchRoutes wires GET /api/search onto authed. authed must already
// have JWT middleware applied (see RegisterProfileRoutes for the contract).
//
// log is optional — pass nil to fall back to slog.Default(). Used for
// non-fatal 500 detail.
func RegisterSearchRoutes(authed *gin.RouterGroup, svc *service.SearchService, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}

	authed.GET("/search", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}

		q := c.Query("q")
		if q == "" {
			c.JSON(400, gin.H{"error": "q is required"})
			return
		}

		// Optional integer params — match the legacy parseIntParam behaviour:
		// missing/blank/garbage values fall back to defaults rather than 400.
		channelID := parseInt64Default(c.Query("channel_id"), 0)
		limit := parseIntDefault(c.Query("limit"), service.SearchDefaultLimit)

		result, err := svc.Search(c.Request.Context(), uid, service.SearchParams{
			Query:     q,
			Type:      c.Query("type"),
			ChannelID: channelID,
			Limit:     limit,
		})
		if err != nil {
			log.Error("search", "user_id", uid, "error", err)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}

		// Transcribe to the wire shape. nil slice → key omitted (caller did
		// not request that category); non-nil slice → key emitted as an
		// (possibly empty) array. Same envelope as the legacy handler.
		var resp searchResponse
		if result.Messages != nil {
			msgs := result.Messages
			resp.Messages = &msgs
		}
		if result.Users != nil {
			users := result.Users
			resp.Users = &users
		}
		if result.Channels != nil {
			channels := result.Channels
			resp.Channels = &channels
		}
		c.JSON(200, resp)
	})
}

// parseInt64Default parses s as int64; returns def on parse failure or empty
// input. Mirrors the legacy handler.parseIntParam helper so query-string
// behaviour is preserved verbatim across the cut-over.
func parseInt64Default(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// parseIntDefault is parseInt64Default for plain ints.
func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}
