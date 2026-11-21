package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

// A ban permits authentication for an appeal, never ordinary service access.
// Keep this separate from the spending/enabled predicates.
func isUserAppealSessionAllowed(user *entity.User) bool {
	return isUserSessionAllowed(user) || (user != nil && strings.EqualFold(strings.TrimSpace(user.Status), "banned"))
}

func refreshSessionBan(user *entity.User) error {
	if user == nil {
		return nil
	}
	status := strings.ToLower(strings.TrimSpace(user.Status))
	// Ban records restrict enabled accounts; they never promote an independently
	// disabled/unknown account into an authenticated session. Preserve the
	// closure_pending contract so its existing session can cancel account closure.
	if status != "" && status != "enabled" && status != "banned" {
		return nil
	}
	banned, err := service.IsBanned(dao.User.UnderlyingDB(), user.ID)
	if err != nil {
		return err
	}
	if banned {
		user.Status = "banned"
	} else if strings.EqualFold(strings.TrimSpace(user.Status), "banned") {
		user.Status = "enabled"
	}
	return nil
}

func bannedSessionRouteAllowed(c *gin.Context) bool {
	path := c.Request.URL.Path
	switch path {
	case "/api/user/info", "/api/user/session":
		return c.Request.Method == http.MethodGet
	case "/api/user/logout":
		return c.Request.Method == http.MethodPost
	case "/api/tickets":
		return c.Query("scope") != "all" && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodPost)
	case "/api/ticket-attachments":
		return c.Request.Method == http.MethodPost
	}
	if strings.HasPrefix(path, "/api/ticket-attachments/") {
		id, err := strconv.ParseInt(strings.TrimPrefix(path, "/api/ticket-attachments/"), 10, 64)
		return err == nil && id > 0 && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodDelete)
	}
	if !strings.HasPrefix(path, "/api/tickets/") || c.Query("scope") == "all" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/tickets/"), "/")
	if len(parts) == 1 {
		_, err := strconv.ParseInt(parts[0], 10, 32)
		return err == nil && c.Request.Method == http.MethodGet
	}
	if len(parts) == 2 && (parts[1] == "messages" || parts[1] == "attachments") {
		_, err := strconv.ParseInt(parts[0], 10, 32)
		return err == nil && ((parts[1] == "messages" && c.Request.Method == http.MethodPost) || (parts[1] == "attachments" && c.Request.Method == http.MethodGet))
	}
	return false
}
