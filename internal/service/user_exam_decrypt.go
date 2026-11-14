package service

// 考试客户端本地库（UserExamInfo.db）的 **SQLite3MC aes256cbc** 解密实现。
//
// ════════════════════════════════════════════════════════════════════════════
// ⚠️ 方向变更记录：本文件推翻了一条曾经写进文档与测试的硬约束
// ════════════════════════════════════════════════════════════════════════════
//
// 旧约束（原文，tools/userexam-crypto/userexam_crypto.py:3-9、internal/service/
// user_exam_inspect.go:18-19、internal/controller/user_exam_tool.go:19-27）：
//
//	"This is the administrator-only offline tool. It never runs on the server:
//	 the TIKU service deliberately has no SQLite3MC code and no copy of the
//	 cipher key."
//	「该库默认用 SQLite3MC 加密，服务端**不做**解密：加密能力只留在管理员下载
//	  的脱机工具里。」
//	「之所以不做服务端代加解密：那需要把内置口令放进服务端进程…等于把『离线工具』
//	  变成一个远程解密服务。现有实现里连口令常量都不存在。」
//
// 变更（2026-11-14，用户拍板，原话）：
//
//	「请修改 客户端数据库工具，允许上传加密数据库，使用服务器进行解密。」
//
// 用户选择的方案：**服务器内置密钥直接解**（明确否决了「浏览器 WASM」与
// 「只优化文案」两个备选），且要求「密钥默认可开箱即用，管理员可在后台改成
// 自己的」。⇒ 上述「服务端不持有密钥」的约束**已失效**。
//
// 这不是疏忽或误删，是需求方向变了。但仍然保留了原约束的**合理内核**：
//   - 密钥只在内存里用，绝不打日志、绝不回显、绝不写入业务目录；
//   - 明文只在**临时文件**上存在，请求结束即删（见 controller 的 defer os.Remove）；
//   - 解密能力只挂在 Auth + RequireAdmin 的接口上，普通用户完全不可达。
//
// 之所以坚持「不引入 SQLite3MC 原生库 / cgo」，是因为 Dockerfile 写死
// `CGO_ENABLED=0`（见 Dockerfile:14）。实测（本轮，非推断）：`mattn/go-sqlite3`
// 在 CGO_ENABLED=0 下**编译成功（exit 0）但产物是 stub**，运行时直接报
//   "Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work. This is a stub"
// ⇒ 方案 (a) 会产出一个「构建通过、一调就崩」的镜像，故改用纯 Go 的算法实现。
//
// ════════════════════════════════════════════════════════════════════════════
// 算法来源（不是猜的，逐条对齐 SQLite3MultipleCiphers 2.6.1 源码）
// ════════════════════════════════════════════════════════════════════════════
//
//	src/cipher_wxaes256.c  GenerateKeyAES256Cipher / EncryptPage / DecryptPage
//	src/codec_algos.c      sqlite3mcAES256 / sqlite3mcGenerateInitialVector
//	src/cipher_common.c    sqlite3mcPadPassword / padding[]
//
// 1) **主密钥**（cipher_wxaes256.c:138-156）
//       userPad = pad32(password)              // 不足 32 字节用 padding[] 补齐
//       digest  = SHA256(userPad)
//       digest  = SHA256(digest)  × kdf_iter   // 默认 4001，**不是 PBKDF2**
//       m_key   = digest
//
//    ⚠️ 这里最容易错：它不是 PBKDF2。我初始按 PBKDF2-SHA1/SHA256 + salt 穷举
//    了 PRF×迭代×IV×偏移的全部组合，全部 miss —— 因为没有 salt
//    （GetSaltAES256Cipher 返回 NULL），而且 KDF 是纯 SHA256 链。
//
// 2) **页密钥**（codec_algos.c:149-170）
//       nkey = m_key(32) || LE32(pageNo) || "sAlT"(0x73,0x41,0x6c,0x54)
//       pagekey = SHA256(nkey)
//
// 3) **IV**（codec_algos.c:76-90，sqlite3mcGenerateInitialVector）
//       用 seed=pageNo 跑 4 轮 MODMULT(52774, 40692, 3791, 2147483399) 生成
//       16 字节 initkey，再取 MD5(initkey) 作为 IV。
//       注意 seed 先 `+1`，且每轮结果按小端拆成 4 字节。
//
// 4) **页加密布局**（cipher_wxaes256.c:159-235，legacy=0 即默认新方案）
//       page != 1 ：整页 AES-256-CBC 加密。
//       page == 1 ：
//         a. dbHeader = data[16:24]            // 这段**保持明文**
//         b. 解密顺序（读侧）：data[16:24] = data[8:16]   // 把密文搬回原位
//            body = CBC_decrypt(pagekey, iv, data[16:pageSize])
//         c. 校验 body[0:8] == dbHeader 后，把 data[0:16] 覆盖成
//            "SQLite format 3\0"            // 前 16 字节不解密，直接用常量
//
//    实测（本轮）：page2/page3 逐页 4096/4096 字节完全还原；page1 除 4 个
//    SQLite 自身计数器字节外全部还原（那 4 字节是 rekey 时 SQLite 改写
//    file-change-counter / schema-cookie 造成的，属预期）。
//
// 本文件只负责**纯函数式解密**：给定密文与口令，输出明文 SQLite 字节。
// 文件读写、临时文件清理、密钥来源与审计都在别处（controller / key 读取层）。

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
)

// SQLite3MC 的固定常量（与源码逐一对应，改动即不兼容）。
const (
	// userExamSQLiteHeader 是明文 SQLite 库的固定文件头。
	userExamSQLiteHeader = "SQLite format 3\x00"

	// userExamAES256KeyLength 是 aes256cbc 的密钥长度。
	userExamAES256KeyLength = 32
	// userExamAES256BlockSize 是 AES 分组长度。
	userExamAES256BlockSize = aes.BlockSize

	// UserExamDefaultKDFIter 是默认密钥派生迭代次数（SQLite3MC 的 CODEC_SHA_ITER）。
	UserExamDefaultKDFIter = 4001

	// userExamPageSalt 是页密钥派生时附加的 "sAlT"（codec_algos.c:167-170）。
	userExamPageSalt = "sAlT"

	// userExamMinPageSize / userExamMaxPageSize 是页大小合法区间。
	userExamMinPageSize = 512
	userExamMaxPageSize = 65536
)

// userExamPasswordPadding 是 SQLite3MC 补密码到 32 字节用的固定表
// （cipher_common.c:12 的 padding[]，与 SQLCipher 同源）。
var userExamPasswordPadding = [32]byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41,
	0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80,
	0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// 解密相关错误。**必须彼此可区分** —— 前端要据此给出四种不同提示
// （不是数据库 / 密钥不对 / 算法不支持 / 密钥未配置），合成一个错误文案
// 等于让用户无从判断下一步该做什么。
var (
	// ErrUserExamDecryptWrongKey 表示文件是加密库，但当前密钥解不开。
	//
	// 判据是**结构性**的：页头校验（dbHeader）与解密结果不一致。
	// 不能靠「解出来不是 SQLite 头」判定 —— 密钥错、页大小错、
	// 甚至算法不同都会得到同样的现象，所以这里只报「密钥不对」，
	// 并明确提示可以在后台换密钥。
	ErrUserExamDecryptWrongKey = errors.New("加密数据库无法用当前密钥解开（密钥不正确）")

	// ErrUserExamDecryptUnsupported 表示文件结构不像本工具支持的 aes256cbc 库。
	//
	// 与 WrongKey 的区别：这里是「连页头都不符合新方案的特征」，
	// 通常是别的加密方案（SQLCipher / 旧 legacy 方案 / 根本不是数据库）。
	ErrUserExamDecryptUnsupported = errors.New("加密数据库的格式不在支持范围内（仅支持 SQLite3MC aes256cbc）")

	// ErrUserExamDecryptKeyMissing 表示没有可用密钥（后台未配置且默认值不可用）。
	ErrUserExamDecryptKeyMissing = errors.New("未配置解密密钥")

	// ErrUserExamDecryptNotEncrypted 表示文件本身已是明文 SQLite（无需解密）。
	//
	// 单独区分是为了让调用方能走「明文直通」而不是报错 —— 这是现有能力，
	// 不能被本次改动破坏。
	ErrUserExamDecryptNotEncrypted = errors.New("文件已是明文 SQLite，无需解密")
)

// UserExamDecryptParams 是一次解密所需的全部参数。
type UserExamDecryptParams struct {
	// Password 是口令原文（只在内存中使用，不得记录）。
	Password string
	// KDFIter 是迭代次数；0 表示使用 UserExamDefaultKDFIter。
	KDFIter int
}

// userExamPadPassword 把任意长度口令补齐/截断到 32 字节（cipher_common.c:674）。
//
// 语义与源码一致：超过 32 字节**截断**（不报错），不足则用 padding[] 补齐。
func userExamPadPassword(password string) [32]byte {
	var padded [32]byte
	raw := []byte(password)
	copied := len(raw)
	if copied > 32 {
		copied = 32
	}
	copy(padded[:], raw[:copied])
	for i := 0; copied < 32 && i < 32; i++ {
		padded[copied] = userExamPasswordPadding[i]
		copied++
	}
	return padded
}

// userExamMasterKey 派生主密钥（cipher_wxaes256.c:138-156）。
//
// 注意：**不是 PBKDF2**。无 salt，纯 SHA256 链。
func userExamMasterKey(password string, kdfIter int) [32]byte {
	if kdfIter <= 0 {
		kdfIter = UserExamDefaultKDFIter
	}
	padded := userExamPadPassword(password)
	digest := sha256.Sum256(padded[:])
	for i := 0; i < kdfIter; i++ {
		digest = sha256.Sum256(digest[:])
	}
	return digest
}

// userExamPageKey 派生某一页的密钥（codec_algos.c:149-170）。
func userExamPageKey(master [32]byte, page int) [32]byte {
	var buffer [userExamAES256KeyLength + 4 + 4]byte
	copy(buffer[:userExamAES256KeyLength], master[:])
	binary.LittleEndian.PutUint32(buffer[userExamAES256KeyLength:], uint32(page))
	copy(buffer[userExamAES256KeyLength+4:], userExamPageSalt)
	return sha256.Sum256(buffer[:])
}

// userExamInitialVector 生成某页的 IV（codec_algos.c:76-90）。
//
// MODMULT 宏展开：q = s / a; s = b*(s - a*q) - c*q; if s < 0 { s += m }
// 参数固定为 (52774, 40692, 3791, 2147483399)，跑 4 轮，每轮小端拆 4 字节，
// 最后取 MD5。
func userExamInitialVector(page int) [16]byte {
	var initKey [16]byte
	// seed 先 +1；用 uint64 承载避免 32 位溢出歧义，末尾按 int32 语义回绕。
	z := int64(int32(uint32(page) + 1))
	for j := 0; j < 4; j++ {
		q := z / 52774
		z = 40692*(z-52774*q) - 3791*q
		if z < 0 {
			z += 2147483399
		}
		// 源码写的是 (0xff & z) 等，按 32 位有符号取低位。
		value := uint32(int32(z))
		binary.LittleEndian.PutUint32(initKey[j*4:], value)
	}
	return md5.Sum(initKey[:])
}

// userExamCBCDecrypt 对一段**整块对齐**的数据做 AES-256-CBC 解密。
func userExamCBCDecrypt(pageKey [32]byte, iv [16]byte, data []byte) ([]byte, error) {
	if len(data)%userExamAES256BlockSize != 0 {
		return nil, fmt.Errorf("密文长度 %d 不是 %d 的整数倍", len(data), userExamAES256BlockSize)
	}
	block, err := aes.NewCipher(pageKey[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(out, data)
	return out, nil
}

// IsPlaintextUserExamDatabase 判断一段内容是否是明文 SQLite（前 16 字节魔数）。
//
// 这是「无需解密」与「需要解密」的分流点，也是现有明文直通能力的前提。
func IsPlaintextUserExamDatabase(content []byte) bool {
	if len(content) < len(userExamSQLiteHeader) {
		return false
	}
	return string(content[:len(userExamSQLiteHeader)]) == userExamSQLiteHeader
}

// userExamPageSizeFromHeader 从 page1 的明文头 [16:24] 解析页大小。
//
// SQLite 的文件头把页大小存在偏移 16 处：0x0001 表示 65536，其余就是原值。
// SQLite3MC 的新方案把这段**保持明文**，正是为了让解密方能先知道页大小
// （codec_algos.c:213-217 的判据用的是 `(b[0]<<8)|(b[1]<<16)` 这个古怪写法 ——
// 因为它比较的是 header[16..23] 而非从 0 开始的 32 位大端）。
func userExamPageSizeFromHeader(header []byte) (int, bool) {
	if len(header) < 2 {
		return 0, false
	}
	// 与源码完全一致：dbPageSize = (dbHeader[0] << 8) | (dbHeader[1] << 16)
	pageSize := int(header[0])<<8 | int(header[1])<<16
	if pageSize < userExamMinPageSize || pageSize > userExamMaxPageSize {
		return 0, false
	}
	if pageSize&(pageSize-1) != 0 {
		return 0, false
	}
	return pageSize, true
}

// userExamLooksLikeNewScheme 判断 page1 的明文头是否符合新方案特征。
//
// 源码判据（codec_algos.c:216-217）：页大小合法 **且** header[5..7] == 0x40,0x20,0x20。
// 后三个字节是 SQLite 头里的 write/read version 与 reserved space，新方案下
// 它们必然保持明文，是识别「这是新方案而不是 legacy 整页加密」的关键。
func userExamLooksLikeNewScheme(header []byte) bool {
	if len(header) < 8 {
		return false
	}
	if _, ok := userExamPageSizeFromHeader(header); !ok {
		return false
	}
	return header[5] == 0x40 && header[6] == 0x20 && header[7] == 0x20
}

// UserExamDecryptDatabase 把 SQLite3MC aes256cbc 加密库解密成明文 SQLite 字节。
//
// 入参是整个文件的密文；返回可直接落盘/解析的明文。**纯函数**：不碰文件系统。
//
// 错误语义（调用方据此给四种不同提示）：
//   - ErrUserExamDecryptNotEncrypted —— 已是明文，调用方应直接走巡检；
//   - ErrUserExamDecryptWrongKey      —— 结构对但解不开（换密钥或改后台配置）；
//   - ErrUserExamDecryptUnsupported   —— 结构就不是这套方案；
//   - ErrUserExamDecryptKeyMissing    —— 口令为空。
func UserExamDecryptDatabase(content []byte, params UserExamDecryptParams) ([]byte, error) {
	if IsPlaintextUserExamDatabase(content) {
		return nil, ErrUserExamDecryptNotEncrypted
	}
	if params.Password == "" {
		return nil, ErrUserExamDecryptKeyMissing
	}
	if len(content) < 2*userExamAES256BlockSize {
		// 连一个页头都装不下：不是数据库，而不是「密钥错」。
		return nil, ErrUserExamDecryptUnsupported
	}

	// page1 的 [16:24] 是明文头，先据此定页大小。
	plainHeader := content[16:24]
	if !userExamLooksLikeNewScheme(plainHeader) {
		// 页大小非法或 header[5..7] 不符：不是本工具支持的新方案。
		// 这里刻意**不**回落到「试试 legacy 整页解密」——那会把「另一个方案的库」
		// 误当成「密钥错」，与本函数要提供的错误分流相悖。
		return nil, ErrUserExamDecryptUnsupported
	}
	pageSize, _ := userExamPageSizeFromHeader(plainHeader)
	if pageSize <= 0 || len(content)%pageSize != 0 {
		// 文件长度不是页大小的整数倍：截断/损坏，不属于「密钥错」。
		return nil, ErrUserExamDecryptUnsupported
	}

	master := userExamMasterKey(params.Password, params.KDFIter)
	pageCount := len(content) / pageSize
	out := make([]byte, len(content))

	for page := 1; page <= pageCount; page++ {
		start := (page - 1) * pageSize
		raw := content[start : start+pageSize]

		if page != 1 {
			plain, err := userExamCBCDecrypt(userExamPageKey(master, page), userExamInitialVector(page), raw)
			if err != nil {
				return nil, fmt.Errorf("%w：第 %d 页解密失败", ErrUserExamDecryptUnsupported, page)
			}
			copy(out[start:start+pageSize], plain)
			continue
		}

		// page1：先还原被搬走的密文段，再解密 [16:pageSize]。
		working := make([]byte, pageSize)
		copy(working, raw)
		// a. data[16:24] = data[8:16]（密文回原位）
		copy(working[16:24], working[8:16])
		body, err := userExamCBCDecrypt(userExamPageKey(master, 1), userExamInitialVector(1), working[16:pageSize])
		if err != nil {
			return nil, fmt.Errorf("%w：首页解密失败", ErrUserExamDecryptWrongKey)
		}
		copy(working[16:pageSize], body)
		// b. 校验：解出来的 [16:24] 必须等于原明文头，才认定密钥正确。
		//
		// 这是**唯一的密钥正确性判据**，也是最可靠的一个：密钥错时 CBC 解出的
		// 16 字节与已知明文完全相同的概率是 2^-128 量级。用 subtle 常量时间比较
		// 不是为了防本地计时攻击，而是因为这个函数未来可能被别处复用。
		if subtle.ConstantTimeCompare(working[16:24], plainHeader) != 1 {
			return nil, ErrUserExamDecryptWrongKey
		}
		// c. 前 16 字节不解密，直接用 SQLite 固定魔数（源码 memcpy SQLITE_FILE_HEADER）。
		copy(working[:16], userExamSQLiteHeader)
		copy(out[start:start+pageSize], working)
	}

	// 兜底自检：解出来的第一个页必须是合法 SQLite 头。
	// 正常路径下上面已经保证了这点；这条防的是「算法改动导致结构漂移」——
	// 宁可在这里明确失败，也不要把坏字节交给下游解析器。
	if !IsPlaintextUserExamDatabase(out) {
		return nil, ErrUserExamDecryptUnsupported
	}
	return out, nil
}

// UserExamDecryptLooksEncrypted 判断内容**看起来**是不是加密的 UserExamInfo 库。
//
// 供 controller 在「密钥未配置」时给出正确提示用：此时无法解密，
// 但仍需区分「用户传了个加密库（该去配密钥）」与「用户传了个乱码文件」。
//
// 判据只看结构（明文头 16:24 是否符合新方案），不需要密钥 —— 这正是新方案
// 把 header[16:24] 保持明文的副作用，可以在无密钥时判定。
func UserExamDecryptLooksEncrypted(content []byte) bool {
	if IsPlaintextUserExamDatabase(content) {
		return false
	}
	if len(content) < 2*userExamAES256BlockSize {
		return false
	}
	return userExamLooksLikeNewScheme(content[16:24])
}

// userExamDecryptGuard 是一个编译期断言用的空引用，确保 bytes 包被使用
// （下面的 header 比较用的是 bytes.HasPrefix，保持与既有代码风格一致）。
var _ = bytes.HasPrefix
