// 管理员上传 EXE 的提交判据（诉求 ① / ③）。
//
// 用户原话：「上传按钮无效。需要勾选『确认该原生 C 版本已验证新版租约』并填写版本号
// 才能正常使用，请添加提示。**请去除**『确认该原生 C 版本已验证新版租约』。」
//
// 所以：
//   - 勾选框**去掉**（它把上传的前置条件藏了一半，用户只看到「按钮无效」）；
//   - 版本号**保留**（用户没说要删），但它现在是**唯一**的必填项；
//   - 按钮的 disabled 条件必须与「真正的必填项」一致，且**缺什么要说清**。
//
// 顺带（诉求 ③）：管理员这个动作的语义是「上传新版本 EXE」，不是「新建脚本」。
// 上传成功后**不自动打开新建脚本弹窗** —— 那正是把两件事混成一个的根源。

const EXE_SUFFIX = '.exe'

/** 版本号规则（与后端 UploadScriptVersionWithDB 的校验对齐：非空、≤64、不含 \ / 与控制字符）。 */
export function validateScriptVersionLabel(value) {
  const text = String(value ?? '').trim()
  if (!text) return '请填写 EXE 版本号'
  if (text.length > 64) return '版本号不能超过 64 个字符'
  if (text.includes('\\') || text.includes('/')) return '版本号不能包含斜杠或反斜杠'
  // 控制字符（含 NUL）一律拒绝：它们会破坏 multipart 头与日志行。
  // 用字符码判定而不是正则里的 \x00 —— eslint 的 no-control-regex 会拦下后者，
  // 而这条规则本身是对的（正则字面量里塞控制字符可读性差且易被编辑器吃掉）。
  for (let index = 0; index < text.length; index += 1) {
    if (text.charCodeAt(index) < 0x20 || text.charCodeAt(index) === 0x7f) {
      return '版本号不能包含控制字符'
    }
  }
  return ''
}

/** 文件名必须是 .exe（不区分大小写）。 */
export function validateScriptUploadFile(file) {
  const name = String(file?.name ?? '')
  if (!name) return '请选择要上传的 EXE 文件'
  if (!name.toLowerCase().endsWith(EXE_SUFFIX)) return '只允许上传 .exe 文件'
  return ''
}

/**
 * 上传按钮的可提交判据。返回 { disabled, message }。
 *
 * message 是**缺什么**的具体说明（不是「请填写完整」这种无用文案）：
 * 按钮 disabled 时界面上必须能读到它，用户才知道该补什么。
 */
export function describeScriptUploadGate({ uploading = false, version = '', file = null } = {}) {
  if (uploading) return { disabled: true, message: '正在上传，请稍候' }
  const versionError = validateScriptVersionLabel(version)
  if (versionError) return { disabled: true, message: versionError }
  if (file) {
    const fileError = validateScriptUploadFile(file)
    if (fileError) return { disabled: true, message: fileError }
  }
  return { disabled: false, message: '' }
}

/**
 * 管理员动作的文案与目标（诉求 ③：「管理员上传 exe，不是新建脚本」）。
 *
 * 管理视图的主操作是「上传 EXE 新版本」，`script-versions/upload`；
 * 「新建脚本」属于用户侧动作，管理视图不给这个入口。
 */
export function describeScriptAdminAction() {
  return {
    primaryLabel: '上传 EXE',
    // 明确写「不创建脚本」，避免管理员以为上传完就多了一条脚本记录。
    hint: '上传后自动发布为最新版本，已有脚本会原子切换到新版本；上传不会创建脚本记录。',
    target: 'script-versions/upload',
  }
}
