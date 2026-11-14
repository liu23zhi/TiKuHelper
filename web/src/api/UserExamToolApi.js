import request from '../utils/request'

// 考试客户端本地库工具（仅管理员）。
// 服务端不解密：这里只做「下载脱机工具」与「只读预览已解密的明文库」。

export function getUserExamToolVersion() {
  return request({ url: '/api/admin/userexam/tool/version', method: 'get', timeout: 15_000 })
}

// 下载走浏览器原生下载，不经过 axios：避免把 15 MB 二进制塞进 JSON 拦截器。
export function getUserExamToolDownloadURL() {
  return '/api/admin/userexam/tool'
}

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
