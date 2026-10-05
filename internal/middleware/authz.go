package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
)

// RequireAdmin requires the authenticated user to have the admin role.
func RequireAdmin(c *gin.Context) {
	value, exists := c.Get("user")
	user, ok := value.(*entity.User)
	if !exists || !ok || user == nil || !strings.EqualFold(strings.TrimSpace(user.Role), "admin") {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "无权限执行该操作"})
		return
	}
	c.Next()
}
