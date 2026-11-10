# userexam-crypto

管理员专用的**脱机**工具，用于处理考试客户端本地库 `UserExamInfo.db`。不在服务端运行，
也不被服务端调用。转换后得到普通 SQLite 库（`stem.decrypted.db` / `stem.encrypted.db`），
源文件全程只读，输出前做全表指纹校验。

## 提供口令

口令在运行时提供，**不得写进仓库或提交**：

1. 环境变量 `USEREXAM_PASSWORD`
2. 脚本（打包后是 exe）同目录的 `password.txt`
3. 当前工作目录的 `password.txt`

```powershell
$env:USEREXAM_PASSWORD = "<向管理员索取>"
```

## 运行

需要 Python 3.11 x64；`_vendor` 运行库随包提供。

```powershell
py -3.11 userexam_crypto.py                      # 无参数 → GUI
py -3.11 userexam_crypto.py -i ".\UserExamInfo.db" -o ".\out"
```

Windows 也可直接双击 `run.cmd`。

## 构建单文件 exe

```powershell
pwsh -File .\build-exe.ps1
```

产物在 `dist\userexam-crypto.exe`（不进 git）。exe 同样不含口令，部署时把 `password.txt`
放在 exe 同目录或设环境变量。
