package registry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const legacyHistoryChildEnv = "TIKU_BILLING_LEGACY_HISTORY_CHILD"
const legacyHistoryWorkdirEnv = "TIKU_BILLING_LEGACY_HISTORY_WORKDIR"
const legacyHistoryComplete = "D03_LEGACY_HISTORY_CONSERVATION_COMPLETE"

func TestBillingRecordLegacyHistoryPreservesOriginalColumns(t *testing.T) {
	if os.Getenv(legacyHistoryChildEnv) != "" {
		t.Fatal("parent received child mode")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestBillingRecordLegacyHistoryPreservesOriginalColumnsChild$", "-test.v", "-test.timeout=16s")
	cmd.Env = append(os.Environ(), legacyHistoryChildEnv+"=1", legacyHistoryWorkdirEnv+"="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	t.Logf("child stdout:\n%s", stdout.Bytes())
	if stderr.Len() != 0 {
		t.Logf("child stderr:\n%s", stderr.Bytes())
	}
	if err != nil || ctx.Err() != nil || stderr.Len() != 0 {
		t.Fatalf("legacy-history child failed: run=%v context=%v", err, ctx.Err())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(legacyHistoryComplete)) {
		t.Fatal("child did not reach complete legacy-history conservation assertion")
	}
}

type legacyHistorySQLValue struct {
	GoType string `json:"go_type"`
	Value  string `json:"value"`
	Null   bool   `json:"null"`
}
type legacyHistoryColumn struct {
	CID        int            `json:"cid"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	NotNull    int            `json:"not_null"`
	Default    sql.NullString `json:"default"`
	PrimaryKey int            `json:"primary_key"`
}
type legacyHistoryState struct {
	DDL             string                    `json:"ddl"`
	Columns         []legacyHistoryColumn     `json:"columns"`
	RowColumns      []string                  `json:"row_columns"`
	Rows            [][]legacyHistorySQLValue `json:"rows"`
	OriginalColumns []legacyHistoryColumn     `json:"original_columns"`
	OriginalRows    [][]legacyHistorySQLValue `json:"original_rows"`
	Settings        []entity.SystemSetting    `json:"settings"`
}

func legacyHistoryLog(t *testing.T, label string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %s", label, b)
}
func legacyHistoryValue(t *testing.T, value any) legacyHistorySQLValue {
	t.Helper()
	out := legacyHistorySQLValue{GoType: fmt.Sprintf("%T", value)}
	switch v := value.(type) {
	case nil:
		out.Null = true
	case int64:
		out.Value = strconv.FormatInt(v, 10)
	case float64:
		out.Value = strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		out.Value = strconv.FormatBool(v)
	case string:
		out.Value = v
	case []byte:
		out.Value = hex.EncodeToString(v)
	case time.Time:
		out.Value = v.Format(time.RFC3339Nano)
	default:
		t.Fatalf("unhandled observed SQLite value type %T", value)
	}
	return out
}
func legacyHistoryRead(t *testing.T, db *gorm.DB, originalNames []string) legacyHistoryState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	out := legacyHistoryState{Columns: []legacyHistoryColumn{}, RowColumns: []string{}, Rows: [][]legacyHistorySQLValue{}, OriginalColumns: []legacyHistoryColumn{}, OriginalRows: [][]legacyHistorySQLValue{}, Settings: []entity.SystemSetting{}}
	if err = sqlDB.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", entity.TableNameBillingRecord).Scan(&out.DDL); err != nil {
		t.Fatal(err)
	}
	info, err := sqlDB.QueryContext(ctx, "PRAGMA table_info(billing_records)")
	if err != nil {
		t.Fatal(err)
	}
	for info.Next() {
		var c legacyHistoryColumn
		if err = info.Scan(&c.CID, &c.Name, &c.Type, &c.NotNull, &c.Default, &c.PrimaryKey); err != nil {
			info.Close()
			t.Fatal(err)
		}
		out.Columns = append(out.Columns, c)
	}
	err = info.Err()
	closeErr := info.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read legacy column metadata: rows=%v close=%v", err, closeErr)
	}
	data, err := sqlDB.QueryContext(ctx, "SELECT * FROM billing_records ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	out.RowColumns, err = data.Columns()
	if err != nil {
		data.Close()
		t.Fatal(err)
	}
	for data.Next() {
		values := make([]any, len(out.RowColumns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err = data.Scan(pointers...); err != nil {
			data.Close()
			t.Fatal(err)
		}
		row := make([]legacyHistorySQLValue, len(values))
		for i, value := range values {
			row[i] = legacyHistoryValue(t, value)
		}
		out.Rows = append(out.Rows, row)
	}
	err = data.Err()
	closeErr = data.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read complete legacy rows: rows=%v close=%v", err, closeErr)
	}
	if err = db.WithContext(ctx).Order("id").Find(&out.Settings).Error; err != nil {
		t.Fatal(err)
	}
	if originalNames == nil {
		originalNames = append([]string(nil), out.RowColumns...)
	}
	positions := map[string]int{}
	columnMetadata := map[string]legacyHistoryColumn{}
	for i, name := range out.RowColumns {
		positions[name] = i
	}
	for _, column := range out.Columns {
		columnMetadata[column.Name] = column
	}
	for _, name := range originalNames {
		column, ok := columnMetadata[name]
		if !ok {
			t.Fatalf("original legacy column %q disappeared", name)
		}
		out.OriginalColumns = append(out.OriginalColumns, column)
	}
	for _, row := range out.Rows {
		projected := []legacyHistorySQLValue{}
		for _, name := range originalNames {
			index, ok := positions[name]
			if !ok {
				t.Fatalf("original legacy column %q absent from row", name)
			}
			projected = append(projected, row[index])
		}
		out.OriginalRows = append(out.OriginalRows, projected)
	}
	return out
}
func legacyHistoryOriginalUnchanged(t *testing.T, before, after legacyHistoryState) {
	t.Helper()
	if len(before.Rows) != 2 || len(after.Rows) != 2 || !reflect.DeepEqual(after.OriginalColumns, before.OriginalColumns) || !reflect.DeepEqual(after.OriginalRows, before.OriginalRows) {
		t.Fatal("legacy backfill changed original two-row count, original column metadata, or any original stored value")
	}
	if len(after.Columns) != len(before.Columns)+1 || len(after.RowColumns) != len(before.RowColumns)+1 {
		t.Fatal("legacy migration added or removed columns other than question_text")
	}
	added := []string{}
	original := map[string]bool{}
	for _, c := range before.Columns {
		original[c.Name] = true
	}
	for _, c := range after.Columns {
		if !original[c.Name] {
			added = append(added, c.Name)
		}
	}
	if !reflect.DeepEqual(added, []string{"question_text"}) {
		t.Fatalf("unexpected legacy schema change: %v", added)
	}
}
func legacyHistoryQuestion(t *testing.T, db *gorm.DB, id int32) sql.NullString {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var q sql.NullString
	if err := db.WithContext(ctx).Raw("SELECT question_text FROM billing_records WHERE id = ?", id).Scan(&q).Error; err != nil {
		t.Fatal(err)
	}
	return q
}

func TestBillingRecordLegacyHistoryPreservesOriginalColumnsChild(t *testing.T) {
	if os.Getenv(legacyHistoryChildEnv) != "1" {
		t.Skip("private bounded legacy-history child")
	}
	workdir := os.Getenv(legacyHistoryWorkdirEnv)
	if !filepath.IsAbs(workdir) {
		t.Fatal("parent-owned absolute temporary directory required")
	}
	path := filepath.Join(workdir, "legacy-history.db")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("synthetic database must not already exist")
	}
	watchdog := time.AfterFunc(12*time.Second, func() { fmt.Fprintln(os.Stderr, "legacy-history child watchdog"); os.Exit(24) })
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
	// Existing db_test.go fixture type has precisely the original eight columns and real TableName.
	// No RegisterDB/global singleton, startup orchestration or user/token API is fabricated here.
	if err = db.AutoMigrate(&legacyBillingRecordWithoutQuestionText{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn(entity.TableNameBillingRecord, "question_text") {
		t.Fatal("legacy baseline must start without question_text")
	}
	fixed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	legacy := legacyBillingRecordWithoutQuestionText{UserID: 1, Type: "normal_search", Provider: "icodef", Amount: 1.25, UnitPriceSnapshot: "provider=icodef question=" + url.QueryEscape("历史账单的题目"), RequestID: "req-legacy", CreatedAt: fixed}
	recharge := legacyBillingRecordWithoutQuestionText{UserID: 1, Type: "recharge", Provider: "epay", Amount: 50, UnitPriceSnapshot: "provider=epay flatPrice=50.00", RequestID: "req-recharge", CreatedAt: fixed.Add(time.Minute)}
	if err = db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&recharge).Error; err != nil {
		t.Fatal(err)
	}
	before := legacyHistoryRead(t, db, nil)
	legacyHistoryLog(t, "legacy original state", before)
	expectedColumns := []string{"id", "user_id", "type", "provider", "amount", "unit_price_snapshot", "request_id", "created_at"}
	if !reflect.DeepEqual(before.RowColumns, expectedColumns) || len(before.Rows) != 2 || len(before.Settings) != 0 || legacy.ID <= 0 || recharge.ID <= legacy.ID {
		t.Fatal("unexpected original synthetic table/marker/row identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err = ensureBillingQuestionTextSchema(db.WithContext(ctx))
	cancel()
	if err != nil {
		t.Fatalf("actual first ensure helper: %v", err)
	}
	afterFirst := legacyHistoryRead(t, db, before.RowColumns)
	legacyHistoryLog(t, "legacy after first helper", afterFirst)
	legacyHistoryOriginalUnchanged(t, before, afterFirst)
	q := legacyHistoryQuestion(t, db, legacy.ID)
	if !q.Valid || q.String != "历史账单的题目" {
		t.Fatalf("actual historical question backfill differs: %+v", q)
	}
	q = legacyHistoryQuestion(t, db, recharge.ID)
	if q.Valid && q.String != "" {
		t.Fatalf("snapshot without question must stay NULL/empty: %+v", q)
	}
	if len(afterFirst.Settings) != 1 || afterFirst.Settings[0].Key != billingQuestionTextBackfilledKey || afterFirst.Settings[0].ID <= 0 {
		t.Fatal("actual backfill marker missing or unrelated settings written")
	}
	// Marker Value/Remark/timestamps are observed, not guessed from an unread private subhelper.
	// The exact full marker row must then remain stable on the second successful ensure call.
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	err = db.WithContext(ctx).Exec("UPDATE billing_records SET question_text = ? WHERE id = ?", "手工修改", legacy.ID).Error
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	afterManual := legacyHistoryRead(t, db, before.RowColumns)
	legacyHistoryLog(t, "legacy after manual question edit", afterManual)
	legacyHistoryOriginalUnchanged(t, before, afterManual)
	if !reflect.DeepEqual(afterManual.Settings, afterFirst.Settings) {
		t.Fatal("manual question edit changed marker row")
	}
	q = legacyHistoryQuestion(t, db, legacy.ID)
	if !q.Valid || q.String != "手工修改" {
		t.Fatal("controlled manual question edit not present")
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	err = ensureBillingQuestionTextSchema(db.WithContext(ctx))
	cancel()
	if err != nil {
		t.Fatalf("actual retry ensure helper: %v", err)
	}
	afterRetry := legacyHistoryRead(t, db, before.RowColumns)
	legacyHistoryLog(t, "legacy after retry helper", afterRetry)
	legacyHistoryOriginalUnchanged(t, before, afterRetry)
	if !reflect.DeepEqual(afterRetry, afterManual) {
		t.Fatal("retry changed manual question, original historical row fields, schema, or complete marker row")
	}
	q = legacyHistoryQuestion(t, db, legacy.ID)
	if !q.Valid || q.String != "手工修改" {
		t.Fatal("one-shot retry overwrote manual question")
	}
	q = legacyHistoryQuestion(t, db, recharge.ID)
	if q.Valid && q.String != "" {
		t.Fatal("retry rewrote snapshot-without-question row")
	}
	legacyHistoryLog(t, "legacy conservation", struct {
		Rows, OriginalColumns, AllowedAddedColumns, HelperCalls                      int
		AllOriginalValuesPreserved, ManualQuestionPreserved, CompleteMarkerPreserved bool
	}{2, 8, 1, 2, true, true, true})
	t.Log(legacyHistoryComplete)
}
