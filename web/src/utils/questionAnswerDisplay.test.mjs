import test from 'node:test'
import assert from 'node:assert/strict'
import { buildQuestionAnswerText, parseQuestionAnswerValues, isObjectiveQuestionType, normalizeEditorQuestionType } from './questionAnswerDisplay.mjs'

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


test('editor objective classification is exactly normalized0/1/3, including legacy decimal strings', () => {
  for (const v of [0,1,3,'0','1','3','01',' +3 ','-0']) assert.equal(isObjectiveQuestionType(v),true,String(v))
  for (const v of [2,4,-4,-1,10,13,15,30,'10','-4','13',2147483647,-2147483648]) {
    assert.equal(isObjectiveQuestionType(v),false,String(v))
    assert.equal(typeof normalizeEditorQuestionType(v),'number')
  }
})
test('malformed type cannot select an objective editor or produce invalid int32 JSON', () => {
  for (const v of [null,undefined,'',true,false,NaN,Infinity,1.1,'1junk','0junk','1e2','0x10','1.0',2147483648,-2147483649,{},[],Object('1')]) {
    assert.equal(normalizeEditorQuestionType(v),undefined)
    assert.equal(isObjectiveQuestionType(v),false)
  }
})
test('type normalization never invokes unknown object coercion or mutates the answer contract', () => {
  const tricky={toString(){throw new Error('coercion must not run')},valueOf(){throw new Error('coercion must not run')}}
  assert.equal(normalizeEditorQuestionType(tricky),undefined)
  const record={type:13,answer:'["same","same",0,false]'}
  assert.deepEqual(parseQuestionAnswerValues(record.answer),['same','same',0,false])
  assert.equal(record.answer,'["same","same",0,false]')
})
