package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 本文件覆盖「服务器解密加密库」这条新链路（2026-11-14 用户拍板的方案）。
//
// 夹具是**真实加密库**：用 SQLite3MC（apsw 2.6.1）以 aes256cbc / kdf_iter=4001
// 实际 rekey 出来的，不是手搓的假数据。见 testdata/README 的生成命令。
// 这一点很关键 —— 用假数据「证明解密能跑」等于什么都没证明。

func ueFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取夹具 %s 失败: %v", name, err)
	}
	return content
}

// ─────────────────────────── 算法正确性 ───────────────────────────

// 加密库 → 解密 → 与同源明文库逐字节比对。
//
// 断言「差异字节数 ≤ 8 且差异全在 SQLite 自身的计数器字段」而不是「完全相等」：
// 生成夹具时 rekey 会让 SQLite 改写 page1 的 file-change-counter 等 4 个字节，
// 那是**夹具生成过程**的差异，不是解密错误。把这条写成「允许少量已知差异」，
// 同时断言「差异位置必须是那 4 个特定偏移」——否则就成了宽泛的容错断言，
// 解密错几十个字节也可能蒙过。
func TestUserExamDecryptReproducesPlaintext(t *testing.T) {
	for _, name := range []struct {
		enc   string
		plain string
		pw    string
	}{
		{"default_key.enc.db", "default_key.plain.db", UserExamDefaultDecryptKey},
		{"custom_key.enc.db", "custom_key.plain.db", "MY-CUSTOM-KEY-9f3a"},
	} {
		t.Run(name.enc, func(t *testing.T) {
			enc := ueFixture(t, name.enc)
			want := ueFixture(t, name.plain)

			got, err := UserExamDecryptDatabase(enc, UserExamDecryptParams{Password: name.pw})
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("长度不一致: got=%d want=%d", len(got), len(want))
			}
			if !IsPlaintextUserExamDatabase(got) {
				t.Fatalf("解密结果没有 SQLite 魔数")
			}

			// page1 的 4 个允许差异偏移（rekey 时被 SQLite 改写的计数器）。
			allowed := map[int]bool{27: true, 95: true, 98: true, 99: true}
			diffs := []int{}
			for i := range want {
				if got[i] != want[i] {
					diffs = append(diffs, i)
				}
			}
			for _, pos := range diffs {
				if !allowed[pos] {
					t.Fatalf("出现不允许的差异位置 %d（got=%d want=%d）", pos, got[pos], want[pos])
				}
			}
			if len(diffs) > 8 {
				t.Fatalf("差异字节过多: %d (%v)", len(diffs), diffs)
			}
			// 关键内容必须完全一致：表结构区（page1 的 sqlite_master 之后）与 page2/3。
			if !bytes.Equal(got[128:], want[128:]) {
				t.Fatalf("offset 128 之后的内容必须逐字节一致")
			}
		})
	}
}

// 解密结果必须能被既有的巡检器读懂，且读到真实业务数据。
//
// 这是「解密是否真的有用」的判据 —— 只证明「字节像 SQLite」不够，
// 必须证明现有解析链路能读出 baseInfo 里的 schoolId。
func TestUserExamDecryptThenInspectRealData(t *testing.T) {
	enc := ueFixture(t, "default_key.enc.db")
	plain, err := UserExamDecryptDatabase(enc, UserExamDecryptParams{Password: UserExamDefaultDecryptKey})
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	path := filepath.Join(t.TempDir(), "decrypted.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := InspectUserExamDatabase(path)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(result.Tables) == 0 {
		t.Fatalf("解密后未解析出表: other=%v", result.OtherTables)
	}
	rows := result.Tables["baseInfo"].Rows
	if len(rows) != 1 {
		t.Fatalf("baseInfo 行数 = %d, want 1", len(rows))
	}
	if got, _ := rows[0]["schoolId"].(string); got != "196586" {
		t.Fatalf("schoolId = %v, want 196586（解密出的真实数据不对）", rows[0]["schoolId"])
	}
	if got, _ := rows[0]["schoolName"].(string); got != "自检大学" {
		t.Fatalf("schoolName = %v, want 自检大学（UTF-8 是否正确）", rows[0]["schoolName"])
	}
}

// ─────────────────────────── 错误分流（四种必须可区分） ───────────────────────────

// 密钥错误必须报 WrongKey，**不能**报成「不是数据库」或「算法不支持」。
//
// 这是前端四种提示的分界，也是用户能自助解决的唯一一种。
func TestUserExamDecryptWrongKeyIsDistinct(t *testing.T) {
	enc := ueFixture(t, "default_key.enc.db")

	_, err := UserExamDecryptDatabase(enc, UserExamDecryptParams{Password: "THIS-IS-THE-WRONG-KEY"})
	if err == nil {
		t.Fatal("错误密钥必须失败")
	}
	if !errors.Is(err, ErrUserExamDecryptWrongKey) {
		t.Fatalf("err = %v, want ErrUserExamDecryptWrongKey", err)
	}
	// 反向断言：不能被归成另外两类（否则前端的四种提示会塌成一个）。
	if errors.Is(err, ErrUserExamDecryptUnsupported) || errors.Is(err, ErrUserExamDecryptNotEncrypted) {
		t.Fatalf("wrong-key 不能被归为 unsupported/not-encrypted: %v", err)
	}
}

// 明文库必须报 NotEncrypted（调用方据此走明文直通）。
func TestUserExamDecryptPlaintextIsDistinct(t *testing.T) {
	plain := ueFixture(t, "default_key.plain.db")
	_, err := UserExamDecryptDatabase(plain, UserExamDecryptParams{Password: UserExamDefaultDecryptKey})
	if !errors.Is(err, ErrUserExamDecryptNotEncrypted) {
		t.Fatalf("err = %v, want ErrUserExamDecryptNotEncrypted", err)
	}
}

// 不是数据库（随机字节）必须报 Unsupported，而不是 WrongKey。
func TestUserExamDecryptGarbageIsUnsupported(t *testing.T) {
	garbage := make([]byte, 8192)
	for i := range garbage {
		garbage[i] = byte(i * 7)
	}
	// 确保它不是明文 SQLite 头
	if IsPlaintextUserExamDatabase(garbage) {
		t.Fatal("测试数据偶然成了 SQLite 头")
	}
	_, err := UserExamDecryptDatabase(garbage, UserExamDecryptParams{Password: UserExamDefaultDecryptKey})
	if !errors.Is(err, ErrUserExamDecryptUnsupported) {
		t.Fatalf("err = %v, want ErrUserExamDecryptUnsupported", err)
	}
	// 也要能被 LooksEncrypted 判成「不像加密库」→ 前端走「不是数据库」文案。
	if UserExamDecryptLooksEncrypted(garbage) {
		t.Fatal("随机字节不应被判为加密库")
	}
}

// 密钥缺失必须报 KeyMissing（与「密钥错」区分：一个去配、一个去改）。
func TestUserExamDecryptMissingKeyIsDistinct(t *testing.T) {
	enc := ueFixture(t, "default_key.enc.db")
	_, err := UserExamDecryptDatabase(enc, UserExamDecryptParams{Password: ""})
	if !errors.Is(err, ErrUserExamDecryptKeyMissing) {
		t.Fatalf("err = %v, want ErrUserExamDecryptKeyMissing", err)
	}
}

// LooksEncrypted 必须能把真实的加密库认出来（无密钥也能判定结构）。
//
// 这条是「密钥未配置时给出正确提示」的前提：不能解密也要能说清
// 「这是加密库，请去配密钥」而不是「这不是数据库」。
func TestUserExamDecryptLooksEncryptedOnRealFixture(t *testing.T) {
	enc := ueFixture(t, "default_key.enc.db")
	if !UserExamDecryptLooksEncrypted(enc) {
		t.Fatal("真实加密库必须被 LooksEncrypted 判为 true")
	}
	plain := ueFixture(t, "default_key.plain.db")
	if UserExamDecryptLooksEncrypted(plain) {
		t.Fatal("明文库不能被判为加密库")
	}
}

// 截断的加密库必须被拒（不能被当成「密钥错」而误导用户换密钥）。
func TestUserExamDecryptTruncatedIsUnsupported(t *testing.T) {
	enc := ueFixture(t, "default_key.enc.db")
	truncated := enc[:len(enc)/2] // 12288 → 6144，不是 4096 的整数倍
	_, err := UserExamDecryptDatabase(truncated, UserExamDecryptParams{Password: UserExamDefaultDecryptKey})
	if !errors.Is(err, ErrUserExamDecryptUnsupported) {
		t.Fatalf("err = %v, want ErrUserExamDecryptUnsupported（截断不是密钥问题）", err)
	}
}

// ─────────────────────────── KDF / IV 派生（逐条钉住来源） ───────────────────────────

// 主密钥必须来自 SHA256 链，**不是 PBKDF2**。
//
// 这条断言的意义：我最初按 PBKDF2 穷举全部组合都解不开，后来读源码才发现是
// SHA256 链。把它钉住，防止将来有人「顺手改成 PBKDF2 更标准」——
// 那会让所有现存的加密库突然解不开。
func TestUserExamMasterKeyIsSHA256Chain(t *testing.T) {
	padded := userExamPadPassword("abc")
	expected := sha256.Sum256(padded[:])
	for i := 0; i < UserExamDefaultKDFIter; i++ {
		expected = sha256.Sum256(expected[:])
	}
	got := userExamMasterKey("abc", UserExamDefaultKDFIter)
	if got != expected {
		t.Fatalf("主密钥派生不对: got=%x want=%x", got, expected)
	}
	// 反向：迭代次数必须真的生效（kdfIter=1 与 4001 必须不同）。
	other := userExamMasterKey("abc", 1)
	if other == got {
		t.Fatal("kdfIter 必须影响结果（否则迭代被短路了）")
	}
}

// 口令补齐规则：超过 32 字节截断，不足用 padding 表补齐（cipher_common.c:674）。
func TestUserExamPadPasswordRules(t *testing.T) {
	// 短口令：前 3 字节是原文，第 4 字节起必须是 padding 表
	padded := userExamPadPassword("abc")
	if string(padded[:3]) != "abc" {
		t.Fatalf("前 3 字节 = %q, want abc", string(padded[:3]))
	}
	if padded[3] != userExamPasswordPadding[0] {
		t.Fatalf("补齐首字节 = %02x, want %02x", padded[3], userExamPasswordPadding[0])
	}
	// 长口令：截断到 32 字节，**不报错**
	long := strings.Repeat("x", 100)
	truncated := userExamPadPassword(long)
	if strings.TrimRight(string(truncated[:]), "x") != "" {
		t.Fatalf("长口令必须被截断成 32 个 x，实际 %q", string(truncated[:]))
	}
	if len(truncated) != 32 {
		t.Fatalf("长度 = %d, want 32", len(truncated))
	}
}

// IV 必须由 MODMULT + MD5 生成（codec_algos.c:76-90）。
//
// 用「不同页产生不同 IV、同一页稳定复现」两个性质钉住，
// 再加一条已知向量（page=1 的期望值由算法手算）。
func TestUserExamInitialVectorIsStablePerPage(t *testing.T) {
	first := userExamInitialVector(1)
	again := userExamInitialVector(1)
	if first != again {
		t.Fatal("同一页的 IV 必须稳定复现")
	}
	second := userExamInitialVector(2)
	if first == second {
		t.Fatal("不同页必须得到不同 IV")
	}
	// 已知向量：由 Python 实现（同一份算法）算出的 page=1 IV。
	const wantHex = "20d7420f9c37a35dca6fe92a1c6999a9"
	gotHex := hex.EncodeToString(first[:])
	if gotHex != wantHex {
		t.Fatalf("IV(page1) = %s, want %s", gotHex, wantHex)
	}
}

// 页密钥必须绑定页号与 "sAlT" 后缀（codec_algos.c:149-170）。
func TestUserExamPageKeyBindsPageAndSalt(t *testing.T) {
	master := userExamMasterKey("k", UserExamDefaultKDFIter)
	k1 := userExamPageKey(master, 1)
	k2 := userExamPageKey(master, 2)
	if k1 == k2 {
		t.Fatal("不同页必须有不同页密钥")
	}
	// 手工按公式复算 page=1 的页密钥
	buffer := make([]byte, 0, 40)
	buffer = append(buffer, master[:]...)
	buffer = append(buffer, 1, 0, 0, 0) // LE32(1)
	buffer = append(buffer, 's', 'A', 'l', 'T')
	expected := sha256.Sum256(buffer)
	if k1 != expected {
		t.Fatalf("页密钥构造不对: got=%x want=%x", k1, expected)
	}
}

// ─────────────────────────── 密钥来源优先级（核心需求） ───────────────────────────

func newUserExamKeyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:userexam-key-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// AutoMigrate 逐个调用（一次传多个模型会合并建表语句 —— 已知事故）。
	for _, model := range []any{&entity.SystemSetting{}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate %T: %v", model, err)
		}
	}
	return db
}

// 后台未配置 → 用内置默认密钥，且能真的解开用默认密钥加密的库。
//
// 「能解开」是行为断言，不是只看返回值 —— 后者是假钉子。
func TestUserExamDecryptKeyFallsBackToDefault(t *testing.T) {
	db := newUserExamKeyTestDB(t)

	key, usingDefault := GetUserExamDecryptKeyWithDB(db)
	if !usingDefault {
		t.Fatal("空库必须回落到默认密钥")
	}
	if key != UserExamDefaultDecryptKey {
		t.Fatalf("key = %q, want 默认密钥", key)
	}
	// 行为断言：用这把钥匙必须能解开默认密钥加密的真实夹具。
	enc := ueFixture(t, "default_key.enc.db")
	if _, err := UserExamDecryptDatabase(enc, UserExamDecryptParams{Password: key}); err != nil {
		t.Fatalf("默认密钥必须能解开默认夹具: %v", err)
	}
}

// 后台配了自定义密钥 → 必须优先用它，且默认密钥**不再**能解开自定义夹具。
//
// 反向断言（默认密钥解不开）很关键：只断言「自定义能解」时，
// 一个「无论配置什么都用默认密钥 + 自定义夹具恰好也是默认密钥」的实现也能过。
func TestUserExamDecryptKeyPrefersConfigured(t *testing.T) {
	db := newUserExamKeyTestDB(t)
	const custom = "MY-CUSTOM-KEY-9f3a"

	state, err := SetUserExamDecryptKeyWithDB(db, custom)
	if err != nil {
		t.Fatalf("set key: %v", err)
	}
	if !state.Configured || state.UsingDefault {
		t.Fatalf("state = %+v, want configured=true usingDefault=false", state)
	}

	key, usingDefault := GetUserExamDecryptKeyWithDB(db)
	if usingDefault {
		t.Fatal("配置后必须不再使用默认密钥")
	}
	if key != custom {
		t.Fatalf("key = %q, want %q", key, custom)
	}

	// 行为断言：自定义密钥解开自定义夹具。
	customEnc := ueFixture(t, "custom_key.enc.db")
	if _, err := UserExamDecryptDatabase(customEnc, UserExamDecryptParams{Password: key}); err != nil {
		t.Fatalf("自定义密钥必须能解开自定义夹具: %v", err)
	}
	// 反向断言：默认密钥解不开自定义夹具（证明配置真的生效了，而不是仍用默认）。
	if _, err := UserExamDecryptDatabase(customEnc, UserExamDecryptParams{Password: UserExamDefaultDecryptKey}); !errors.Is(err, ErrUserExamDecryptWrongKey) {
		t.Fatalf("默认密钥不应能解开自定义夹具，err = %v", err)
	}
}

// 清空配置 → 回落默认（「清空 = 恢复出厂」，不是「功能不可用」）。
func TestUserExamDecryptKeyClearRestoresDefault(t *testing.T) {
	db := newUserExamKeyTestDB(t)
	if _, err := SetUserExamDecryptKeyWithDB(db, "SOME-KEY-1234"); err != nil {
		t.Fatalf("set: %v", err)
	}
	state, err := SetUserExamDecryptKeyWithDB(db, "")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if state.Configured || !state.UsingDefault {
		t.Fatalf("清空后 state = %+v, want configured=false usingDefault=true", state)
	}
	key, usingDefault := GetUserExamDecryptKeyWithDB(db)
	if !usingDefault || key != UserExamDefaultDecryptKey {
		t.Fatalf("清空后 key = %q usingDefault=%v, want 默认值", key, usingDefault)
	}
}

// 密钥**永不回显**：状态结构里只有布尔位与弱指纹。
//
// 这条用「响应字段里不含密钥原文」直接钉住，并额外断言弱指纹不等于密钥。
func TestUserExamDecryptKeyIsNeverEchoed(t *testing.T) {
	db := newUserExamKeyTestDB(t)
	const secret = "SUPER-SECRET-DO-NOT-LEAK"
	if _, err := SetUserExamDecryptKeyWithDB(db, secret); err != nil {
		t.Fatalf("set: %v", err)
	}
	state := GetUserExamDecryptKeyStateWithDB(db)

	// 结构体里所有字符串字段都不能等于密钥（也没法「包含」它 —— 类型层面没有）
	if state.KeyHint == secret {
		t.Fatal("KeyHint 不能等于密钥")
	}
	if len(state.KeyHint) != 8 {
		t.Fatalf("KeyHint 长度 = %d, want 8（SHA-256 前 8 位十六进制）", len(state.KeyHint))
	}
	if state.KeyHint == state.DefaultKeyHint {
		t.Fatal("自定义密钥的指纹不应等于默认密钥指纹（否则指纹没反映实际值）")
	}
	// 弱指纹必须是确定性的（否则管理员无法用它确认「改的值生效了」）
	if again := GetUserExamDecryptKeyStateWithDB(db); again.KeyHint != state.KeyHint {
		t.Fatalf("KeyHint 必须稳定: %q vs %q", state.KeyHint, again.KeyHint)
	}
}

// 超长密钥必须**明确拒绝**（而不是截断后静默生效）。
//
// SQLite3MC 的 padPassword 会在 32 字节截断，若这里不拦，
// 管理员设一个 40 字节密钥会「保存成功、实际只用前 32 字节」，极难排查。
func TestUserExamDecryptKeyRejectsTooLong(t *testing.T) {
	db := newUserExamKeyTestDB(t)
	_, err := SetUserExamDecryptKeyWithDB(db, strings.Repeat("x", 33))
	if !errors.Is(err, ErrUserExamDecryptKeyInvalid) {
		t.Fatalf("err = %v, want ErrUserExamDecryptKeyInvalid", err)
	}
	// 32 字节必须放行（边界）
	if _, err := SetUserExamDecryptKeyWithDB(db, strings.Repeat("x", 32)); err != nil {
		t.Fatalf("32 字节必须接受: %v", err)
	}
}

// 含控制字符的密钥必须拒绝（不可见差异会导致「同样的密钥解不开」）。
func TestUserExamDecryptKeyRejectsControlChars(t *testing.T) {
	db := newUserExamKeyTestDB(t)
	_, err := SetUserExamDecryptKeyWithDB(db, "abc\x00def")
	if !errors.Is(err, ErrUserExamDecryptKeyInvalid) {
		t.Fatalf("err = %v, want ErrUserExamDecryptKeyInvalid", err)
	}
}

// 数据库不可用时读取密钥必须回落到默认值而不 panic（降级要求）。
func TestUserExamDecryptKeyNilDBSafelyFallsBack(t *testing.T) {
	key, usingDefault := GetUserExamDecryptKeyWithDB(nil)
	if !usingDefault || key != UserExamDefaultDecryptKey {
		t.Fatalf("nil db 必须回落到默认值，got=%q default=%v", key, usingDefault)
	}
}
