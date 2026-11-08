package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

const tenantPaginationChildEnv = "TIKU_TEST_BILLING_TENANT_PAGINATION_CHILD"
const tenantPaginationWorkdirEnv = "TIKU_TEST_BILLING_TENANT_PAGINATION_WORKDIR"
const tenantPaginationComplete = "D03_TENANT_PAGINATION_COMPLETE"
const tenantPaginationPageSize = 2

func TestBillingRecordTenantPaginationPreservesScopeAndTotals(t *testing.T) {
	if os.Getenv(tenantPaginationChildEnv) != "" {
		t.Fatal("parent received child mode")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestBillingRecordTenantPaginationPreservesScopeAndTotalsChild$", "-test.v", "-test.timeout=16s")
	cmd.Env = append(os.Environ(), tenantPaginationChildEnv+"=1", tenantPaginationWorkdirEnv+"="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	t.Logf("child stdout:\n%s", stdout.Bytes())
	if stderr.Len() != 0 {
		t.Logf("child stderr:\n%s", stderr.Bytes())
	}
	if err != nil || ctx.Err() != nil || stderr.Len() != 0 {
		t.Fatalf("tenant-pagination child failed: run=%v context=%v", err, ctx.Err())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(tenantPaginationComplete)) {
		t.Fatal("child did not reach complete tenant-pagination conservation assertion")
	}
}

type tenantPaginationHiddenToken struct {
	TokenHash     string `json:"token_hash"`
	TokenValueSHA string `json:"token_value_sha256"`
}
type tenantPaginationState struct {
	Users           []entity.User                         `json:"users"`
	UserPasswordSHA map[int32]string                      `json:"user_password_sha256"`
	Tokens          []entity.AccessToken                  `json:"tokens"`
	TokenHidden     map[int32]tenantPaginationHiddenToken `json:"token_hidden"`
	Rows            []entity.BillingRecord                `json:"rows"`
}

func tenantPaginationHash(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}
func tenantPaginationLog(t *testing.T, label string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %s", label, b)
}
func tenantPaginationRead(t *testing.T, db *gorm.DB) tenantPaginationState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := tenantPaginationState{Users: []entity.User{}, Tokens: []entity.AccessToken{}, Rows: []entity.BillingRecord{}, UserPasswordSHA: map[int32]string{}, TokenHidden: map[int32]tenantPaginationHiddenToken{}}
	if err := db.WithContext(ctx).Order("id").Find(&out.Users).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Order("id").Find(&out.Tokens).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Order("id").Find(&out.Rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, u := range out.Users {
		out.UserPasswordSHA[u.ID] = tenantPaginationHash(u.Password)
	}
	for _, token := range out.Tokens {
		out.TokenHidden[token.ID] = tenantPaginationHiddenToken{TokenHash: token.TokenHash, TokenValueSHA: tenantPaginationHash(token.TokenValue)}
	}
	return out
}

type tenantPaginationWantRow struct {
	ID, UserID      int32
	Type, RequestID string
	Amount          float64
	Fees            map[string]float64
}
type tenantPaginationCase struct {
	Name                      string
	Operator                  entity.User
	Query                     service.BillingRecordListQuery
	Rows                      []tenantPaginationWantRow
	PageExpenses, PageIncomes []float64
	TotalExpense, TotalIncome float64
}
type tenantPaginationVisibleIdentity struct {
	ID, UserID int32
	RequestID  string
}

func tenantPaginationCheck(t *testing.T, db *gorm.DB, c tenantPaginationCase) {
	t.Helper()
	if len(c.PageExpenses) != len(c.PageIncomes) || len(c.PageExpenses) == 0 || len(c.PageExpenses) != (len(c.Rows)+tenantPaginationPageSize-1)/tenantPaginationPageSize {
		t.Fatal("invalid fixed expected page matrix")
	}
	seen := map[int32]bool{}
	visible := []tenantPaginationVisibleIdentity{}
	expenseAcrossPages, incomeAcrossPages := 0.0, 0.0
	for page := 1; page <= len(c.PageExpenses); page++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		got, err := service.ListBillingRecordsWithDB(db.WithContext(ctx), c.Operator, c.Query, page, tenantPaginationPageSize)
		cancel()
		tenantPaginationLog(t, "listing "+c.Name, struct {
			Page   int                             `json:"page"`
			Result service.BillingRecordListResult `json:"result"`
			Error  string                          `json:"error"`
		}{page, got, fmt.Sprint(err)})
		if err != nil {
			t.Fatalf("%s page%d actual List API: %v", c.Name, page, err)
		}
		lo := (page - 1) * tenantPaginationPageSize
		hi := lo + tenantPaginationPageSize
		if hi > len(c.Rows) {
			hi = len(c.Rows)
		}
		wantRows := c.Rows[lo:hi]
		if got.Total != int64(len(c.Rows)) || got.Page != page || got.PageSize != tenantPaginationPageSize || len(got.Data) != len(wantRows) {
			t.Fatalf("%s page%d count/pagination differs: %+v", c.Name, page, got)
		}
		if got.Summary.TotalAmount != c.TotalExpense || got.Summary.CurrentPageAmount != c.PageExpenses[page-1] {
			t.Fatalf("%s page%d expense summary differs: %+v", c.Name, page, got.Summary)
		}
		pageExpense, pageIncome := 0.0, 0.0
		for i, row := range got.Data {
			want := wantRows[i]
			if row.ID != want.ID || row.UserID != want.UserID || row.Type != want.Type || row.RequestID != want.RequestID || row.Amount != want.Amount {
				t.Fatalf("%s page%d complete grouping identity/amount differs: got=%+v want=%+v", c.Name, page, row, want)
			}
			if seen[row.ID] {
				t.Fatalf("%s repeats displayed id%d across pages", c.Name, row.ID)
			}
			seen[row.ID] = true
			visible = append(visible, tenantPaginationVisibleIdentity{row.ID, row.UserID, row.RequestID})
			if row.Type == "recharge" || row.Type == "registration_bonus" {
				if row.Direction != "income" {
					t.Fatalf("%s income direction differs: %+v", c.Name, row)
				}
				pageIncome += row.Amount
			} else {
				if row.Direction != "expense" {
					t.Fatalf("%s expense direction differs: %+v", c.Name, row)
				}
				pageExpense += row.Amount
				feeTotals := map[string]float64{}
				for _, fee := range row.FeeDetails {
					feeTotals[fee.Provider] += fee.Amount
				}
				if !reflect.DeepEqual(feeTotals, want.Fees) {
					t.Fatalf("%s page%d fee details crossed tenant/request or lost channel: got=%v want=%v", c.Name, page, feeTotals, want.Fees)
				}
				sumFees := 0.0
				for _, amount := range feeTotals {
					sumFees += amount
				}
				if sumFees != row.Amount {
					t.Fatalf("%s row%d expense differs from own raw channel sum", c.Name, row.ID)
				}
			}
		}
		if pageExpense != c.PageExpenses[page-1] || pageIncome != c.PageIncomes[page-1] || pageExpense != got.Summary.CurrentPageAmount {
			t.Fatalf("%s page%d row-to-page expense/income domains differ: expense=%v income=%v", c.Name, page, pageExpense, pageIncome)
		}
		expenseAcrossPages += pageExpense
		incomeAcrossPages += pageIncome
	}
	tenantPaginationLog(t, "scope totals "+c.Name, struct {
		Expense, Income float64
		Visible         []tenantPaginationVisibleIdentity
	}{expenseAcrossPages, incomeAcrossPages, visible})
	if len(seen) != len(c.Rows) || expenseAcrossPages != c.TotalExpense || incomeAcrossPages != c.TotalIncome {
		t.Fatalf("%s page-to-all-page distinct expense/income totals differ: expense=%v income=%v", c.Name, expenseAcrossPages, incomeAcrossPages)
	}
}

func TestBillingRecordTenantPaginationPreservesScopeAndTotalsChild(t *testing.T) {
	if os.Getenv(tenantPaginationChildEnv) != "1" {
		t.Skip("private bounded tenant-pagination child")
	}
	workdir := os.Getenv(tenantPaginationWorkdirEnv)
	if !filepath.IsAbs(workdir) {
		t.Fatal("parent-owned absolute temporary directory required")
	}
	path := filepath.Join(workdir, "tenant-pagination.db")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("synthetic database must not already exist")
	}
	watchdog := time.AfterFunc(12*time.Second, func() { fmt.Fprintln(os.Stderr, "tenant-pagination child watchdog"); os.Exit(24) })
	defer watchdog.Stop()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err = db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	users := []entity.User{
		{ID: 1, Username: "tenant-pagination-admin", Password: "synthetic-admin-password", Role: "admin", Status: "enabled", Balance: 321.123456, RegisterSource: "admin", CreatedAt: fixed},
		{ID: 2, Username: "tenant-pagination-owner-A", Password: "synthetic-owner-A-password", Role: "user", Status: "enabled", Balance: 106, RegisterSource: "admin", CreatedAt: fixed},
		{ID: 3, Username: "tenant-pagination-owner-B", Password: "synthetic-owner-B-password", Role: "user", Status: "enabled", Balance: 43, RegisterSource: "admin", CreatedAt: fixed},
	}
	for _, u := range users {
		if err = db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	tokens := []entity.AccessToken{}
	for _, v := range []struct {
		ID, UserID int32
		Name       string
		Used       float64
	}{{1, 2, "tenant-A-token", 9}, {2, 3, "tenant-B-token", 57}, {3, 1, "tenant-admin-token", 0}} {
		value := fmt.Sprintf("tk_synthetic_tenant_pagination_%d", v.ID)
		tokens = append(tokens, entity.AccessToken{ID: v.ID, UserID: v.UserID, Name: v.Name, TokenHash: tenantPaginationHash(value), TokenPrefix: "tk_synthetic", TokenValue: value, Status: "enabled", TotalQuotaLimit: 0, TotalAmountUsed: v.Used, CreatedAt: fixed, UpdatedAt: fixed})
	}
	for _, token := range tokens {
		if err = db.Create(&token).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []entity.BillingRecord{
		{ID: 1, UserID: 2, Type: "recharge", Provider: "epay", Amount: 100, BalanceBefore: 10, BalanceAfter: 110, RequestID: "scope-income", UnitPriceSnapshot: "provider=epay flatPrice=100.00", RelatedOrderID: 101, Remark: "synthetic recharge"},
		{ID: 2, UserID: 2, Type: "registration_bonus", Provider: "system", Amount: 5, BalanceBefore: 110, BalanceAfter: 115, RequestID: "scope-bonus", UnitPriceSnapshot: "provider=system bonus=5.00", RelatedOrderID: 102, Remark: "synthetic bonus"},
		{ID: 3, UserID: 2, AccessTokenID: 1, Type: "normal_search", UsedDefaultAccount: true, Provider: "icodef", Amount: 1.25, BalanceBefore: 115, BalanceAfter: 113.75, RequestID: "hist-scope-merged", UnitPriceSnapshot: "provider=icodef question=synthetic-dual", QuestionText: "双渠道合成题", ClientIP: "192.0.2.33", Remark: "synthetic channel one"},
		{ID: 4, UserID: 2, AccessTokenID: 1, Type: "normal_search", UsedDefaultAccount: true, Provider: "ai", ModelName: "d03-tenant-model", InputTokens: 100, CachedInputTokens: 20, OutputTokens: 30, TotalTokens: 130, Amount: 0.75, BalanceBefore: 113.75, BalanceAfter: 113, RequestID: "hist-scope-merged", UnitPriceSnapshot: "provider=ai model=d03-tenant-model question=synthetic-dual", QuestionText: "双渠道合成题", ClientIP: "192.0.2.34", Remark: "synthetic channel two"},
		{ID: 5, UserID: 2, AccessTokenID: 1, Type: "normal_search", UsedDefaultAccount: true, Provider: "icodef", Amount: 3, BalanceBefore: 113, BalanceAfter: 110, RequestID: "scope-A-2", UnitPriceSnapshot: "provider=icodef question=synthetic-A2", QuestionText: "合成A2", ClientIP: "192.0.2.35", Remark: "synthetic A2"},
		{ID: 6, UserID: 2, AccessTokenID: 1, Type: "normal_search", UsedDefaultAccount: true, Provider: "icodef", Amount: 4, BalanceBefore: 110, BalanceAfter: 106, RequestID: "scope-A-3", UnitPriceSnapshot: "provider=icodef question=synthetic-A3", QuestionText: "合成A3", ClientIP: "192.0.2.36", Remark: "synthetic A3"},
		{ID: 7, UserID: 3, AccessTokenID: 2, Type: "normal_search", UsedDefaultAccount: true, Provider: "icodef", Amount: 50, BalanceBefore: 100, BalanceAfter: 50, RequestID: "hist-scope-merged", UnitPriceSnapshot: "provider=icodef question=synthetic-Bsame", QuestionText: "另租户同请求题", ClientIP: "192.0.2.37", Remark: "synthetic B same request"},
		{ID: 8, UserID: 3, AccessTokenID: 2, Type: "normal_search", UsedDefaultAccount: true, Provider: "icodef", Amount: 7, BalanceBefore: 50, BalanceAfter: 43, RequestID: "scope-B-2", UnitPriceSnapshot: "provider=icodef question=synthetic-B2", QuestionText: "合成B2", ClientIP: "192.0.2.38", Remark: "synthetic B2"},
	}
	for i := range rows {
		rows[i].CreatedAt = fixed.Add(time.Duration(i+1) * time.Minute)
		if err = db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	before := tenantPaginationRead(t, db)
	tenantPaginationLog(t, "before readonly listing", before)
	if len(before.Users) != 3 || len(before.Tokens) != 3 || len(before.Rows) != 8 {
		t.Fatal("fixed synthetic row/identity counts differ")
	}
	want := map[int32]tenantPaginationWantRow{
		1: {ID: 1, UserID: 2, Type: "recharge", RequestID: "scope-income", Amount: 100},
		2: {ID: 2, UserID: 2, Type: "registration_bonus", RequestID: "scope-bonus", Amount: 5},
		4: {ID: 4, UserID: 2, Type: "normal_search", RequestID: "hist-scope-merged", Amount: 2, Fees: map[string]float64{"icodef": 1.25, "ai": 0.75}},
		5: {ID: 5, UserID: 2, Type: "normal_search", RequestID: "scope-A-2", Amount: 3, Fees: map[string]float64{"icodef": 3}},
		6: {ID: 6, UserID: 2, Type: "normal_search", RequestID: "scope-A-3", Amount: 4, Fees: map[string]float64{"icodef": 4}},
		7: {ID: 7, UserID: 3, Type: "normal_search", RequestID: "hist-scope-merged", Amount: 50, Fees: map[string]float64{"icodef": 50}},
		8: {ID: 8, UserID: 3, Type: "normal_search", RequestID: "scope-B-2", Amount: 7, Fees: map[string]float64{"icodef": 7}},
	}
	selectRows := func(ids ...int32) []tenantPaginationWantRow {
		out := []tenantPaginationWantRow{}
		for _, id := range ids {
			out = append(out, want[id])
		}
		return out
	}
	cases := []tenantPaginationCase{
		{Name: "owner_A_all", Operator: users[1], Rows: selectRows(6, 5, 4, 2, 1), PageExpenses: []float64{7, 2, 0}, PageIncomes: []float64{0, 5, 100}, TotalExpense: 9, TotalIncome: 105},
		{Name: "owner_B_all", Operator: users[2], Rows: selectRows(8, 7), PageExpenses: []float64{57}, PageIncomes: []float64{0}, TotalExpense: 57},
		{Name: "admin_all", Operator: users[0], Rows: selectRows(8, 7, 6, 5, 4, 2, 1), PageExpenses: []float64{57, 7, 2, 0}, PageIncomes: []float64{0, 0, 5, 100}, TotalExpense: 66, TotalIncome: 105},
		{Name: "owner_A_keyword", Operator: users[1], Query: service.BillingRecordListQuery{Keyword: "hist-scope-merged"}, Rows: selectRows(4), PageExpenses: []float64{2}, PageIncomes: []float64{0}, TotalExpense: 2},
		{Name: "owner_B_keyword", Operator: users[2], Query: service.BillingRecordListQuery{Keyword: "hist-scope-merged"}, Rows: selectRows(7), PageExpenses: []float64{50}, PageIncomes: []float64{0}, TotalExpense: 50},
		{Name: "admin_keyword", Operator: users[0], Query: service.BillingRecordListQuery{Keyword: "hist-scope-merged"}, Rows: selectRows(7, 4), PageExpenses: []float64{52}, PageIncomes: []float64{0}, TotalExpense: 52},
		{Name: "owner_A_recharge_type", Operator: users[1], Query: service.BillingRecordListQuery{Type: "recharge"}, Rows: selectRows(1), PageExpenses: []float64{0}, PageIncomes: []float64{100}, TotalIncome: 100},
		{Name: "owner_A_bonus_type", Operator: users[1], Query: service.BillingRecordListQuery{Type: "registration_bonus"}, Rows: selectRows(2), PageExpenses: []float64{0}, PageIncomes: []float64{5}, TotalIncome: 5},
		{Name: "owner_A_expense_type", Operator: users[1], Query: service.BillingRecordListQuery{Type: "normal_search"}, Rows: selectRows(6, 5, 4), PageExpenses: []float64{7, 2}, PageIncomes: []float64{0, 0}, TotalExpense: 9},
	}
	for _, c := range cases {
		tenantPaginationCheck(t, db, c)
	}
	after := tenantPaginationRead(t, db)
	tenantPaginationLog(t, "after all readonly listings", after)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("readonly List API changed complete eight raw rows, user wallets/identity, tokens/usage, or hidden fields")
	}
	tenantPaginationLog(t, "readonly conservation", struct {
		Queries             int
		Rows, Users, Tokens int
		AllFieldsEqual      bool
	}{15, len(after.Rows), len(after.Users), len(after.Tokens), true})
	t.Log(tenantPaginationComplete)
}
