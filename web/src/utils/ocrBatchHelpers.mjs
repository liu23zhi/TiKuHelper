export const OCR_BATCH_MAX_ITEMS = 50

export function parseOCRBatchURLs(content, { extract = false } = {}) {
  if (typeof content !== 'string' || content.length > 1024 * 1024) throw new Error('TXT 文件不能为空且不能超过 1 MiB')
  if (extract) content = [...content.matchAll(/https?:\/\/[^\s<>"`]+/g)].map(match => match[0].replace(/[)\],.;，。；]+$/, '')).join('\n')
  const urls = [], seen = new Set()
  for (const [index, line] of content.replace(/^\uFEFF/, '').split(/\r?\n/).entries()) {
    const value = line.trim()
    if (!value) continue
    let parsed
    try { parsed = new URL(value) } catch { throw new Error(`第 ${index + 1} 行不是有效图片 URL`) }
    if (!['http:', 'https:'].includes(parsed.protocol) || !parsed.hostname || parsed.username || parsed.password || parsed.hash || value.length > 2048) throw new Error(`第 ${index + 1} 行须为不含用户信息和片段的 HTTP/HTTPS 图片 URL`)
    if (seen.has(value)) continue
    seen.add(value); urls.push(value)
    if (urls.length > OCR_BATCH_MAX_ITEMS) throw new Error(`单次最多识别 ${OCR_BATCH_MAX_ITEMS} 张图片`)
  }
  if (!urls.length) throw new Error('TXT 文件中没有图片 URL，请每行填写一个地址')
  return urls
}

export function readOCRBatchText(file) {
  if (typeof file?.text === 'function') return file.text()
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result || ''))
    reader.onerror = () => reject(new Error('无法读取 TXT 文件'))
    reader.readAsText(file, 'UTF-8')
  })
}

export function imageFileFromOCRDocument(item) {
  if (!item || typeof item.name !== 'string' || typeof item.data !== 'string' || !item.contentType?.startsWith('image/') || item.data.length > 7 * 1024 * 1024) throw new Error('文档图片响应无效')
  const decoded = atob(item.data)
  if (!decoded.length || decoded.length > 5 * 1024 * 1024) throw new Error('文档图片不能为空且不能超过 5 MiB')
  const bytes = Uint8Array.from(decoded, char => char.charCodeAt(0))
  return new File([bytes], item.name, { type: item.contentType })
}
