import test from "node:test";
import assert from "node:assert/strict";
import {
  ARCHIVE_REASON_MIN_LENGTH,
  ARCHIVE_REASON_PRESETS,
  ARCHIVE_REASON_CODES,
  archiveReasonRuneLength,
  normalizeArchiveReason,
  validateArchiveReason,
  buildDeleteQuestionParams,
} from "./questionArchiveReason.mjs";

// 删除（= 归档）原因的前端契约单元测试。
// 与后端口径的对齐点：internal/controller/tiku_archive.go:23 archiveReasonMinLength = 2，
// :453 archivedReasonRuneLength = utf8.RuneCountInString(strings.TrimSpace(reason))。

test("ARCHIVE_REASON_MIN_LENGTH 与后端 archiveReasonMinLength 对齐", () => {
  // 这个数字必须与 Go 侧常量一致；改动任一侧都会让两端校验分叉。
  assert.equal(ARCHIVE_REASON_MIN_LENGTH, 2);
});

test("预设选项与后端 archiveReasonCode 的四类逐字对齐", () => {
  // 后端按中文字面量 switch 匹配（tiku_archive.go:34-42）。
  // 写成缩写（'违规' / '重复'）会落进默认分支 admin_delete，归档列表按分类筛不出来。
  assert.deepEqual(ARCHIVE_REASON_PRESETS, ['内容错误', '重复题库', '违规内容', '其他']);
  for (const preset of ARCHIVE_REASON_PRESETS) {
    assert.ok(ARCHIVE_REASON_CODES[preset], `预设 ${preset} 必须有对应的归档分类代码`);
  }
  assert.equal(ARCHIVE_REASON_CODES['内容错误'], 'content_error');
  assert.equal(ARCHIVE_REASON_CODES['重复题库'], 'duplicate');
  assert.equal(ARCHIVE_REASON_CODES['违规内容'], 'violation');
  assert.equal(ARCHIVE_REASON_CODES['其他'], 'other');
});

test("archiveReasonRuneLength 按字符计数（中文 1 字 = 1），且忽略首尾空格", () => {
  assert.equal(archiveReasonRuneLength(''), 0);
  assert.equal(archiveReasonRuneLength('   '), 0);
  assert.equal(archiveReasonRuneLength('重'), 1);
  assert.equal(archiveReasonRuneLength('重复'), 2);
  // 4 个汉字的字节长度是 12；若误用字节口径，这里会算成 12。
  assert.equal(archiveReasonRuneLength('内容错误'), 4);
  assert.equal(archiveReasonRuneLength('  重复  '), 2);
  // 非字符串按 0 处理，不抛错。
  assert.equal(archiveReasonRuneLength(null), 0);
  assert.equal(archiveReasonRuneLength(undefined), 0);
  assert.equal(archiveReasonRuneLength(42), 0);
});

test("normalizeArchiveReason 只去首尾空格，保留中间空格", () => {
  assert.equal(normalizeArchiveReason('  内容错误  '), '内容错误');
  // 中间空格必须保留：后端也只 TrimSpace，两边都压中间空格会算出不同字符串。
  assert.equal(normalizeArchiveReason('  重复  题库  '), '重复  题库');
  assert.equal(normalizeArchiveReason(null), '');
  assert.equal(normalizeArchiveReason(undefined), '');
  assert.equal(normalizeArchiveReason(1), '');
});

test("validateArchiveReason 的门槛是 2 个字符，文案与后端 400 message 一致", () => {
  const expected = '请填写删除原因（至少 2 个字符）';
  assert.equal(validateArchiveReason(''), expected);
  assert.equal(validateArchiveReason('   '), expected);
  assert.equal(validateArchiveReason(null), expected);
  // 1 个字符拒绝
  assert.equal(validateArchiveReason('重'), expected);
  // 2 个字符通过（中文按字符、不按字节）
  assert.equal(validateArchiveReason('重复'), null);
  assert.equal(validateArchiveReason('  重复  '), null);
  // 4 个汉字也通过
  assert.equal(validateArchiveReason('内容错误'), null);
});

test("buildDeleteQuestionParams 产出可直接放进 axios params 的对象", () => {
  // 后端读 c.Query("reason")，所以必须是 { reason } 这个形状。
  assert.deepEqual(buildDeleteQuestionParams('内容错误'), { reason: '内容错误' });
  // 归一化发生在参数构造里，调用方不必先 trim。
  assert.deepEqual(buildDeleteQuestionParams('  内容错误  '), { reason: '内容错误' });
  // 缺省时给空串（由后端统一回 400），而不是 undefined ——
  // undefined 会被 axios 直接从 query 里省略，后端与「传了空值」无法区分。
  assert.deepEqual(buildDeleteQuestionParams(), { reason: '' });
  assert.deepEqual(buildDeleteQuestionParams(null), { reason: '' });
});

test("buildDeleteQuestionParams 的结果键只有 reason（不夹带其他 config 键）", () => {
  // 若这里多出键（如 reason 拼错成 reasonText），后端必然 400。
  assert.deepEqual(Object.keys(buildDeleteQuestionParams('重复题库')), ['reason']);
});
