package middleware

import (
	"strings"

	"datacenter-thermal-capacity-planner/backend/internal/auth"
	"datacenter-thermal-capacity-planner/backend/internal/web"
	"github.com/gin-gonic/gin"
)

func Auth(service *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := strings.TrimSpace(c.GetHeader("Authorization"))
		parts := strings.SplitN(header, " ", 2)
		claims, err := service.Parse(strings.TrimSpace(parts[1]))
		if err != nil {
			c.Abort()
			web.Fail(c, err)
			return
		}
		user, err := service.CurrentPrincipal(c.Request.Context(), claims.UserID)
		if err != nil {
			c.Abort()
			web.Fail(c, err)
			return
		}
		c.Set(web.ContextUserID, user.ID)
		c.Set(web.ContextUsername, user.Username)
		c.Set(web.ContextRole, user.Role)
		c.Next()
	}
}
