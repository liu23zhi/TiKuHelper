package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Account actions target the ticket owner, never a client-supplied user ID.
func TicketAccountAction(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	admin, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseTicketID(c)
	if !ok {
		return
	}
	var input struct {
		Action         string `json:"action"`
		Value          string `json:"value"`
		Reason         string `json:"reason"`
		DurationAmount int    `json:"durationAmount"`
		DurationUnit   string `json:"durationUnit"`
	}
	if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.Reason) == "" {
		c.JSON(400, gin.H{"message": "请填写处理原因"})
		return
	}
	db := ticketControllerDB()
	err := db.Transaction(func(tx *gorm.DB) error {
		var ticket entity.Ticket
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ticket, id).Error; err != nil {
			return err
		}
		if ticket.Category != "account" && ticket.Category != "account_rebind" && ticket.Category != "blacklist_appeal" {
			return service.ErrTicketForbidden
		}
		switch input.Action {
		case "unban":
			return service.Unban(tx, service.UserUnbanRequest{UserID: ticket.UserID, OperatorUserID: admin.ID, Reason: input.Reason})
		case "ban":
			expires, err := service.ResolveUserBanExpiresAt(input.DurationAmount, input.DurationUnit, time.Now())
			if input.DurationAmount == 0 {
				expires = nil
				err = nil
			}
			if err != nil {
				return err
			}
			_, err = service.Ban(tx, service.UserBanRequest{UserID: ticket.UserID, OperatorUserID: admin.ID, Reason: input.Reason, ExpiresAt: expires})
			return err
		case "phone", "wechat":
			if input.Action == "phone" {
				_, err := service.UpdateUserPhoneWithDB(tx, ticket.UserID, strings.TrimSpace(input.Value), false)
				return err
			}
			_, err := service.UpdateUserWechatBindingWithDB(tx, ticket.UserID, strings.TrimSpace(input.Value), false)
			return err
		default:
			return errors.New("不支持的账号处理动作")
		}
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	recordAccountAudit(c, &admin, "ticket", 20, "管理员通过工单处理账号", map[string]any{"ticketId": id, "action": input.Action, "reason": input.Reason})
	c.JSON(200, gin.H{"message": "账号处理完成"})
}
