package controller

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func withEscrowTestPersistenceDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := *dao.Q
	log, tiku, user := dao.Log, dao.Tiku, dao.User
	dao.SetDefault(db)
	t.Cleanup(func() {
		*dao.Q = previous
		dao.Log, dao.Tiku, dao.User = log, tiku, user
	})
}

func loadEscrowWork(t *testing.T, db *gorm.DB, scriptID int32, requestID string) entity.ScriptEscrowWork {
	t.Helper()
	var row entity.ScriptEscrowWork
	if err := db.Where("script_id = ? AND request_id = ?", scriptID, requestID).Take(&row).Error; err != nil {
		t.Fatalf("load work: %v", err)
	}
	return row
}

func TestEscrowExecutionSearchClaimsBeforeCallingProvider(t *testing.T) {
	for _, async := range []bool{false, true} {
		mode := "sync"
		if async {
			mode = "async"
		}
		t.Run(mode, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			if err := db.AutoMigrate(&entity.ScriptEscrow{}, &entity.ScriptEscrowEntry{}, &entity.ScriptEscrowWork{}); err != nil {
				t.Fatal(err)
			}
			user := seedControllerUser(t, db, 0, "escrow-work-owner", "user")
			admin := seedControllerUser(t, db, 0, "escrow-work-admin", "admin")
			if err := db.Model(&user).Updates(map[string]any{"balance": 100, "phone": "13800000000", "wechat_id": "fixture-escrow-work"}).Error; err != nil {
				t.Fatal(err)
			}
			version, err := service.UploadScriptVersionWithDB(db, admin, "fixture-escrow-work", "fixture.exe", []byte("MZfixture"), true)
			if err != nil {
				t.Fatal(err)
			}
			script, err := service.CreateUserScriptWithDB(db, user, service.CreateUserScriptInput{
				Name: "fixture", VersionID: version.ID, BaseDomain: "fixture.test", BillingMode: entity.ScriptBillingModeEscrow,
			})
			if err != nil {
				t.Fatal(err)
			}
			ticket, err := service.IssueScriptExchangeTokenWithDB(db, user, script.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			exchange, err := service.ExchangeScriptTokenWithDB(db, script.ShortCode, ticket.Token)
			if err != nil {
				t.Fatal(err)
			}
			identity := strings.Repeat("ab", 32)
			run, err := service.ActivateLeasedScriptRunSessionWithDB(db, script.ShortCode, exchange.ActivationToken, identity, "127.0.0.1", 0)
			if err != nil {
				t.Fatal(err)
			}
			seedControllerDefaultSearchProviderConfig(t, db, "icodef", 2.5)
			var requestID string
			withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer,
				func(string, string, url.Values) (resolvedSearchRequestContext, error) {
					t.Error("script must use its authenticated resolver")
					return resolvedSearchRequestContext{}, service.ErrInvalidAccessTokenAuthorization
				},
				func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
					return []func(model.SearchRequest) ([][]string, error){func(req model.SearchRequest) ([][]string, error) {
						requestID = req.RequestID
						var work entity.ScriptEscrowWork
						err := db.Where("script_id = ? AND request_id = ?", script.ID, requestID).Take(&work).Error
						if err != nil || work.DoneAt != nil {
							t.Errorf("provider invoked without a pending durable claim: %+v %v", work, err)
						}
						return [][]string{{"A"}}, nil
					}}
				}, nil, func() {
					req := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1",
						strings.NewReader(`{"question":"fixture","options":["A","B"],"skipCache":true}`))
					req.RemoteAddr = "127.0.0.1:1234"
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Authorization", "Bearer "+run.Token)
					req.Header.Set("X-Script-Identity", identity)
					if async {
						resp, err := executeSearchTask(req)
						if err != nil || resp.Error != nil {
							t.Fatalf("async search failed: %+v %v", resp.Error, err)
						}
					} else {
						rec := httptest.NewRecorder()
						r := gin.New()
						r.POST("/adapter-service/search", Search)
						r.ServeHTTP(rec, req)
						if rec.Code != http.StatusOK {
							t.Fatalf("sync search status: %d", rec.Code)
						}
					}
				})
			if requestID == "" || loadEscrowWork(t, db, script.ID, requestID).DoneAt == nil {
				t.Fatal("a successful paid execution did not settle its work")
			}
			var escrow entity.ScriptEscrow
			if err := db.First(&escrow, "script_id = ?", script.ID).Error; err != nil || escrow.AvailableMicros != 77_500_000 {
				t.Fatalf("expected settled escrow 77500000, got %d (%v)", escrow.AvailableMicros, err)
			}
		})
	}
}

func TestEscrowExecutionAIClaimsAndDistinguishesUncertainResults(t *testing.T) {
	for _, async := range []bool{false, true} {
		mode := "sync"
		if async {
			mode = "async"
		}
		t.Run(mode, func(t *testing.T) {
			db, user, token, session := newScriptRuntimeBillingFixture(t, entity.ScriptBillingModeEscrow, 1)
			if err := db.AutoMigrate(&entity.ScriptEscrowWork{}, &entity.Log{}); err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&entity.UserScript{}).Where("id = ?", session.ScriptID).Update("escrow_work_tracked", true).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&entity.ScriptEscrow{ScriptID: session.ScriptID, UserID: user.ID, AvailableMicros: 80_000_000, Revision: 1}).Error; err != nil {
				t.Fatal(err)
			}
			withEscrowTestPersistenceDB(t, db)
			originalAI, originalStore := runAIFallbackService, defaultAIFallbackTaskStore
			defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
			t.Cleanup(func() { runAIFallbackService, defaultAIFallbackTaskStore = originalAI, originalStore })
			called := make(chan string, 2)
			runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
				var work entity.ScriptEscrowWork
				err := db.Where("script_id = ? AND request_id = ?", session.ScriptID, req.RequestID).Take(&work).Error
				if err != nil || work.DoneAt != nil {
					t.Errorf("AI provider invoked without a pending claim: %+v %v", work, err)
				}
				called <- req.RequestID
				if req.Question == "uncertain" {
					return service.AIFallbackResult{Error: &service.AIFallbackError{Code: service.AIFallbackErrorCodeUpstreamError}}
				}
				return service.BuildAIFallbackUnavailableResult()
			}
			for _, question := range []string{"known", "uncertain"} {
				rec := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(rec)
				ctx.Set("user", &user)
				ctx.Set("access_token", &token)
				ctx.Set("script_session", session)
				ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback",
					strings.NewReader(`{"question":"`+question+`","type":0,"skipCache":true}`))
				ctx.Request.Header.Set("Content-Type", "application/json")
				if async {
					CreateAIFallbackTask(ctx)
					var created aiFallbackTaskResponse
					if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.TaskID == "" {
						t.Fatalf("async AI admission: %d %v", rec.Code, err)
					}
					eventuallyAIFallbackTaskStatus(t, defaultAIFallbackTaskStore, created.TaskID, user.ID, aiFallbackTaskStatusFailed)
				} else {
					AIFallback(ctx)
					if rec.Code != http.StatusOK {
						t.Fatalf("sync AI status: %d", rec.Code)
					}
				}
				select {
				case id := <-called:
					work := loadEscrowWork(t, db, session.ScriptID, id)
					if (work.DoneAt == nil) != (question == "uncertain") {
						t.Fatalf("outcome %s did not correctly gate release", question)
					}
				default:
					t.Fatal("provider not reached")
				}
			}
		})
	}
}

func TestEscrowExecutionBillingRetryCompletesWorkAtomically(t *testing.T) {
	db, user, token, session := newScriptRuntimeBillingFixture(t, entity.ScriptBillingModeEscrow, 1)
	if err := db.AutoMigrate(&entity.ScriptEscrowWork{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.UserScript{}).Where("id = ?", session.ScriptID).Update("escrow_work_tracked", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.ScriptEscrow{ScriptID: session.ScriptID, UserID: user.ID, AvailableMicros: 80_000_000, Revision: 1}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, err := loadAIFallbackBillingContextWithDB(db, user.ID, token.ID, session)
	if err != nil {
		t.Fatal(err)
	}
	ctx.escrowWorkTracked, ctx.RequestID = true, "fixture-retry-work"
	if err := service.BeginScriptEscrowWorkWithDB(db, session.ScriptID, ctx.RequestID, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&session).Update("revoked_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&token).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.BeginScriptEscrowWorkWithDB(db, session.ScriptID, "must-not-admit", session.ID); !errors.Is(err, service.ErrScriptEscrowWorkDenied) {
		t.Fatalf("revoked run admitted a replacement work: %v", err)
	}
	failOnce := true
	if err := db.Callback().Update().Before("gorm:update").Register("fixture:fail-first-work-completion", func(tx *gorm.DB) {
		if tx.Statement.Table == (&entity.ScriptEscrowWork{}).TableName() && failOnce {
			failOnce = false
			tx.AddError(driver.ErrBadConn)
		}
	}); err != nil {
		t.Fatal(err)
	}
	queue := newSearchBillingRetryQueue()
	charges := []searchBillingCharge{{Provider: "fixture", Amount: 0.5}}
	if queued, err := persistSearchBillingRecordsWithRetry(db, ctx, charges, queue); !queued || !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("transient work write must retain pending retry: %t %v", queued, err)
	}
	var bills int64
	if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil || bills != 0 || loadEscrowWork(t, db, session.ScriptID, ctx.RequestID).DoneAt != nil {
		t.Fatalf("work write failure must roll back billing: bills=%d %v", bills, err)
	}
	if !queue.processDue(context.Background(), time.Now().Add(time.Minute)) || queue.pendingCount() != 0 {
		t.Fatal("retry did not finish")
	}
	if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil || bills != 1 ||
		loadEscrowWork(t, db, session.ScriptID, ctx.RequestID).DoneAt == nil {
		t.Fatalf("retry must persist exactly one bill and complete work: bills=%d %v", bills, err)
	}
}
