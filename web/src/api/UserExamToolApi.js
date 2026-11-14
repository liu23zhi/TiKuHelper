import request from '../utils/request'

// 考试客户端本地库工具（仅管理员）。
//
// ⚠️ 2026-11-14 方向变更：本注释原文是「服务端不解密：这里只做『下载脱机工具』与
// 『只读预览已解密的明文库』」。用户拍板「允许上传加密数据库，使用服务器进行解密」
// 之后，服务端**会**解密加密库，因此本文件新增了密钥配置两个接口。
//
// 加解密能力的现状：
//   - 加密库：直接上传，服务端用配置的密钥（默认内置、管理员可改）解密后预览；
//   - 明文库：仍然直通预览（能力未变）；
//   - 脱机工具：仍然保留（可下载），用于「不想把库上传到服务器」的场景。

export function getUserExamToolVersion() {
  return request({ url: '/api/admin/userexam/tool/version', method: 'get', timeout: 15_000 })
}

// 下载走浏览器原生下载，不经过 axios：避免把 15 MB 二进制塞进 JSON 拦截器。
export function getUserExamToolDownloadURL() {
  return '/api/admin/userexam/tool'
}

// 上传数据库做只读预览。**明文库与加密库都可以**，服务端会自行判断并解密。
export function inspectUserExamDatabase(file) {
  const form = new FormData()
  form.append('file', file)
  return request({
    url: '/api/admin/userexam/inspect',
    method: 'post',
    data: form,
    timeout: 60_000,
  })
}

// 读取解密密钥的**状态**。响应刻意不含密钥本身，只有：
//   configured（是否配过）/ usingDefault（是否在用内置默认值）
//   keyHint / defaultKeyHint（各自的 SHA-256 前 8 位弱指纹，用于确认「改的生效了没」）
//   message（给人看的一句话说明）
export function getUserExamDecryptKey() {
  return request({ url: '/api/admin/userexam/decrypt-key', method: 'get', timeout: 15_000 })
}

// 设置解密密钥。传空字符串 = 清空并回落到内置默认值（不是禁用功能）。
export function updateUserExamDecryptKey(key) {
  return request({
    url: '/api/admin/userexam/decrypt-key',
    method: 'put',
    data: { key },
    timeout: 15_000,
  })
}
