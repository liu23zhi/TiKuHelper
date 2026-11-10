# userexam-crypto

管理员专用的**脱机**工具，用于处理考试客户端本地库 `UserExamInfo.db`。不在服务端运行，
也不被服务端调用。转换后得到普通 SQLite 库（`stem.decrypted.db` / `stem.encrypted.db`），
源文件全程只读，输出前做全表指纹校验。

## 提供口令

口令在运行时提供，**不得写进仓库或提交**：

1. 环境变量 `USEREXAM_PASSWORD`
2. exe（或脚本）同目录的 `password.txt`
3. 当前工作目录的 `password.txt`

```powershell
$env:USEREXAM_PASSWORD = "<向管理员索取>"
```

## 运行

**单文件 exe（推荐，目标机器无需安装 Python）**

```powershell
.\userexam-crypto.exe                             # 无参数 → 图形界面
.\userexam-crypto.exe -i ".\UserExamInfo.db" --json
.\userexam-crypto.exe --inspect -i ".\UserExamInfo.decrypted.db"
.\userexam-crypto.exe --selftest                  # 验证随包 SQLite3MC 可加载并完成往返
```

**Python 脚本（需要 64 位 CPython 3.11 + `_vendor` 运行库）**

```powershell
py -3.11 userexam_crypto.py
py -3.11 -m unittest test_userexam_crypto -v
```

Windows 也可直接双击 `run.cmd`。

## 构建单文件 exe

```powershell
pwsh -File .\build-exe.ps1
```

产物是 `dist\userexam-crypto.exe`（约 15 MB，不进 git）。构建脚本除了打包，还会**实际运行**
这个 exe 两次：一次在把 PATH 去掉 Python 的情况下跑 `--selftest`（证明随包的 apsw /
SQLite3MC 原生扩展能在解包目录里加载并完成加密→解密往返），一次把它单独复制到空目录里跑，
证明它是真单文件、不需要同目录的 `_vendor`。再加 `-RealSample <真实加密库> -Password <口令>`
还会用真实样本做一次 decrypt → inspect → encrypt。

exe 同样不含口令：部署时把 `password.txt` 放在 exe 同目录，或设环境变量。
