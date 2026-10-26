import test from 'node:test'
import assert from 'node:assert/strict'
import { buildQuestionAnswerText, parseQuestionAnswerValues } from './questionAnswerDisplay.mjs'

test('answer display preserves long text, explicit newlines, ordered and repeated answers', () => {
  const answers = ['长中文答案'.repeat(50), 'https://example.com/' + 'a'.repeat(180), '第一行\n第二行', '相同', '相同']
  assert.equal(buildQuestionAnswerText({ answer: JSON.stringify(answers) }), answers.join('\n'))
  assert.equal(buildQuestionAnswerText({ answer: '["a","","a"]' }), 'a\n\na')
})

test('empty and malformed answers cannot break the question list', () => {
  for (const answer of [null, undefined, '', '[]', 'null']) assert.equal(buildQuestionAnswerText({ answer }), '')
  for (const answer of ['["未结束', '普通文本', 'x'.repeat(600)]) assert.equal(buildQuestionAnswerText({ answer }), answer)
  assert.equal(buildQuestionAnswerText({ answer: '{"answer":"文本"}' }), '{"answer":"文本"}')
  assert.equal(buildQuestionAnswerText({ answer: '0' }), '0')
  assert.deepEqual(parseQuestionAnswerValues('"文字"'), ['文字'])
})

test('source 2 keeps its established zero-based display branch without changing stored answers', () => {
  const record = { source: 2, answer: '["答案一","答案二","答案二"]' }
  assert.equal(buildQuestionAnswerText(record), '0\n1\n2')
  assert.equal(record.answer, '["答案一","答案二","答案二"]')
  assert.equal(buildQuestionAnswerText({ ...record, source: 1 }), '答案一\n答案二\n答案二')
})
