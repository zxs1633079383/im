package http

import (
	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
)

// RegisterModuleRoutes wires GET /api/modules — replaces mattermost
// csesapi /modules/getAll. Read-only; the table is seeded by migration 016
// and not mutated at runtime.
func RegisterModuleRoutes(authed *gin.RouterGroup, modules repo.ModuleRepo) {
	authed.GET("/modules", func(c *gin.Context) {
		list, err := modules.List(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if list == nil {
			list = []repo.Module{}
		}
		c.JSON(200, list)
	})
}
