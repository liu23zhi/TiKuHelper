package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
)

const registrationBrowserCookieName = "tiku_registration_browser"

// Return evidence only when the client actually presented a signed cookie.
// Issuing a new value on a first/legacy POST never counts that registration.
func ensureRegistrationBrowser(c *gin.Context, now time.Time) string {
	if raw, err := c.Cookie(registrationBrowserCookieName); err == nil {
		if digest := util.RegistrationBrowserHash(raw, now); digest != "" {
			return digest
		}
	}
	raw, err := util.NewRegistrationBrowserCookieValue(now)
	if err != nil {
		logger.SysWarn("未能签发注册浏览器标识，注册不受影响")
		return ""
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     registrationBrowserCookieName,
		Value:    raw,
		Path:     "/api/user/register",
		HttpOnly: true,
		Secure:   requestIsHTTPS(c),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(util.RegistrationBrowserTTL / time.Second),
		Expires:  now.Add(util.RegistrationBrowserTTL),
	})
	return ""
}

func recordCommittedRegistrationBrowser(c *gin.Context, userID int32, clientIP string) {
	now := time.Now()
	digest := ensureRegistrationBrowser(c, now)
	if digest == "" {
		return
	}
	// Evidence is best effort; a busy secondary write must not hold the response.
	ctx, cancel := context.WithTimeout(databaseRequestContext(c), time.Second)
	defer cancel()
	db := withDatabasePriority(dao.User.UnderlyingDB(), ctx, dbqueue.PriorityLow)
	if err := service.RecordRegistrationBrowserEventWithDB(db, userID, digest, clientIP, now); err != nil {
		// Avoid echoing tokens, IPs or database parameters in the warning. The
		// user account was already committed; do not turn this into a retry.
		logger.SysWarn("已提交注册，但浏览器风控事件未写入；该账号不计入本次检测")
	}
}
