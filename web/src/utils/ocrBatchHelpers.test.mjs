import test from 'node:test'
import assert from 'node:assert/strict'
import { parseOCRBatchURLs } from './ocrBatchHelpers.mjs'

test('OCR TXT URLs preserve query values, remove blank duplicates, and reject a malformed batch before any OCR', () => {
  assert.deepEqual(parseOCRBatchURLs('\uFEFFhttps://img.example/a.png?token=a%2Fb\r\n\nhttps://img.example/a.png?token=a%2Fb\nhttps://img.example/b.png'), ['https://img.example/a.png?token=a%2Fb', 'https://img.example/b.png'])
  for (const input of ['', 'file:///secret', 'https://user:pass@img.example/a.png', 'https://img.example/a.png#part', 'https://img.example/a.png\ninvalid-url']) assert.throws(() => parseOCRBatchURLs(input))
  assert.throws(() => parseOCRBatchURLs(Array.from({ length: 51 }, (_, index) => `https://img.example/${index}.png`).join('\n')), /50/)
  assert.deepEqual(parseOCRBatchURLs('题目图片 ![图](https://img.example/a.png)\n<img src="https://img.example/b.png">', { extract: true }), ['https://img.example/a.png', 'https://img.example/b.png'])
})
