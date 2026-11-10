# userexam-crypto：考试客户端本地库脱机加解密工具

管理员专用的**脱机**工具，用于处理超星考试客户端的本地库 `UserExamInfo.db`。
它**不在服务端运行**，也不被服务端调用。

## 适用场景

管理员从用户机器取到 `UserExamInfo.db`，需要查看其中的内容时使用：

1. 在本机用本工具解密，得到 `UserExamInfo.decrypted.db`（普通 SQLite）；
2. 用任意 SQLite 工具查看，或上传到 TIKU 管理台「客户端数据库工具」页做只读预览。

加密库只有两业务表：`baseInfo`（`Token` / `schoolId` / `schoolName` / `thirdFild`）与
`macAddress`（`Address` / `Flag`）。它不是题库数据，而是客户端授权信息。

## 边界：口令不进仓库，服务端不解密

- 本仓库是**公开仓库**。工具**不内置口令**，口令在运行时由你提供（见下节），
  仓库里任何位置都不得出现口令字面量。
- TIKU 服务端**没有任何解密实现**：没有 SQLite3MC、没有 cgo、没有口令常量。
  服务端只做两件事 —— 把本工具（或其打包产物）发出去、把管理员**已经解密好**的明文库
  只读解析一遍。上传的加密库会被文件头校验直接拒绝。
- 内置/固定口令只为**文件格式兼容**（产物能被客户端读回），**不是**为个人数据提供
  密码学保护。不要把它宣传成「加密保护用户数据」。

## 提供口令

优先级从高到低：

1. 环境变量 `USEREXAM_PASSWORD`
2. 脚本同目录（打包后是 exe 同目录 / 解包根）的 `password.txt`
3. 当前工作目录的 `password.txt`

`password.txt` 只写口令本身一行，**已在 `.gitignore` 中忽略，不得提交**。

```powershell
# 方式一：环境变量（当前会话有效）
$env:USEREXAM_PASSWORD = "<向管理员索取>"

# 方式二：本地文件
"<向管理员索取>" | Set-Content -Path .\password.txt -NoNewline -Encoding utf8
```

口令不会被打进日志、不会回显、也不会被本工具写入磁盘。

## 运行

Python 3.11 x64 + `_vendor` 运行库（`apsw-sqlite3mc`，随包提供）：

```powershell
# 无参数 → GUI（选文件 → 自动判定加密/解密 → 转换并校验）
py -3.11 userexam_crypto.py

# CLI
py -3.11 userexam_crypto.py --input "C:\path\UserExamInfo.db"
py -3.11 userexam_crypto.py -i ".\UserExamInfo.db" -o ".\out" --json
py -3.11 userexam_crypto.py --inspect -i ".\UserExamInfo.decrypted.db"
```

或用 `run.cmd`（Windows 双击入口）。

## 行为约定

- **自动判定方向**：读文件头 16 字节，等于 `SQLite format 3\0` → 加密；否则 → 解密。
- **源文件只读**：全程不改动源库，所有写操作只落在同目录的临时副本上。
- **拒绝未合并事务**：`-wal` 非空即拒；`-journal` 前 8 字节全零（PERSIST 冷日志）放行。
  理由：未合并的 WAL 里有尚未写回主库的数据，直接复制会**静默丢数据**。
- **三重一致性校验**：复制前/后比 SHA-256；转换后比全表指纹（`integrity_check` +
  每行 SHA-256 累计 `count/sum/xor`）；发布前再复检源文件未变。任一不符就取消输出。
- **输出命名**：`stem.decrypted.db` / `stem.encrypted.db`，重名依次加 ` (1)`、` (2)`，
  并同时避开 `-journal` / `-wal` / `-shm` 三个 sidecar 名。

## 构建单文件 exe

```powershell
pwsh -File .\build-exe.ps1
```

产物在 `dist\userexam-crypto.exe`（约 15 MB，**不进 git**）。脚本会：

1. 校验 Python 3.11 x64 与 PyInstaller；
2. 从 `..\..\..\_vendor-source` 或环境变量 `USEREXAM_VENDOR_DIR` 取 `apsw-sqlite3mc`
   运行库（也可 `pip download apsw-sqlite3mc==3.54.0.0` 后解包）；
3. 用 spec 打包，把 `_vendor` 作为 **datas** 打进包（保证目录结构不被扁平化）；
4. 跑一次 exe 的 `--inspect` 冒烟测试。

**exe 里同样不含口令** —— 部署时把 `password.txt` 放在 exe 同目录，或设环境变量。

## 已知限制

- 只支持 `aes256cbc` / `legacy=0` / `kdf_iter=4001` 这一组参数。参数不匹配直接报错退出，
  不做暴力、不做「任意密码解密器」。
- 运行库只提供 CPython 3.11 / Windows x64。其他版本需自行安装对应 `apsw-sqlite3mc`。
- 只认 `baseInfo` / `macAddress` 两张表；其他表只报名字，不读内容。
