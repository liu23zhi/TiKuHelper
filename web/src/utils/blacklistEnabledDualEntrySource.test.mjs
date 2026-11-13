import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const web = process.env.TIKU_WEB_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

const readSystemSettings = () => readFile(path.join(web, 'src/pages/SystemSettings/index.vue'), 'utf8');
const readBlacklist = () => readFile(path.join(web, 'src/pages/Blacklist/index.vue'), 'utf8');

// 这两页读写的是**同一个** system_settings.blacklist_enabled。
//
// 背景（2026-11-13）：黑名单页原本写「由『系统管理』控制」，但系统管理页
// 从来没有这个开关 —— 那是一句指向不存在功能的死引用，用户只能去改库。
// 修法是两处都能开关，且必须保证它们落在同一个 key 上；任何一处改成别的 key
// 或者只做了一半（比如只读不写），都会退化成「界面上能点但配置不生效」。
test('黑名单总开关：系统管理与黑名单两页都能开关同一个配置', async () => {
  const system = await readSystemSettings();
  const blacklist = await readBlacklist();

  // ── 系统管理页：一条完整数据流必须四处齐备 ──────────────────────────
  // 缺任何一处都会表现为「开关显示永远为关」或「改了不落库」。
  assert.match(
    system,
    /blacklistEnabled:\s*false\s*,/,
    '系统管理页 formState 必须有 blacklistEnabled 初始值（缺了会永远是 undefined→关）',
  );
  assert.match(
    system,
    /formState\.blacklistEnabled\s*=\s*data\.blacklistEnabled\s*===\s*true/,
    '系统管理页必须从后端读回 blacklistEnabled（缺了打开页面看不到真实状态）',
  );
  assert.match(
    system,
    /blacklistEnabled:\s*!!formState\.blacklistEnabled\s*,/,
    '系统管理页保存时必须提交 blacklistEnabled（缺了改开关不会落库）',
  );
  assert.match(
    system,
    /v-on="\{\s*\x27update:checked\x27:\s*settingsEntry\.bind\(updateSetting,\s*\x27blacklistEnabled\x27\)\s*\}"/,
    '系统管理页必须有一个绑定到 blacklistEnabled 的开关控件',
  );

  // ── 黑名单页：死引用必须消失，真开关必须存在 ──────────────────────
  assert.doesNotMatch(
    blacklist,
    /由「系统管理」控制/,
    '黑名单页不得再写「由系统管理控制」——那是死引用（该开关当时并不存在）',
  );
  assert.match(
    blacklist,
    /const\s+blacklistEnabled\s*=\s*ref\(false\)/,
    '黑名单页必须有本地开关状态',
  );
  assert.match(
    blacklist,
    /async\s+function\s+toggleSwitch/,
    '黑名单页必须有切换函数（只显示不写入等于假开关）',
  );
  assert.match(
    blacklist,
    /v-on="\{\s*\x27update:checked\x27:\s*toggleSwitch\s*\}"/,
    '黑名单页的开关必须真的绑到 toggleSwitch',
  );

  // ── 两页必须落在同一个 key 上 ────────────────────────────────────
  // 黑名单页直接读写系统设置接口，所以提交的字段名必须是 blacklistEnabled。
  // 若有人把这里的字段名改成 blacklistEnable / enabled 之类，后端会当成
  // 「未提交」而保持现值 —— 表现是「点了没反应」，且不报错。
  assert.match(
    blacklist,
    /blacklistEnabled:\s*next\s*===\s*true/,
    '黑名单页提交的字段名必须是 blacklistEnabled（否则后端视为未提交，静默无效）',
  );
  assert.match(
    blacklist,
    /updateSystemSettings\(/,
    '黑名单页必须调用 updateSystemSettings（与系统管理页同一个接口）',
  );
  assert.match(
    blacklist,
    /getSystemSettings\(/,
    '黑名单页必须调用 getSystemSettings 读取当前值',
  );

  // ── 失败必须回滚，否则界面显示的开关状态与真实配置不一致 ──────────
  assert.match(
    blacklist,
    /blacklistEnabled\.value\s*=\s*previous/,
    '保存失败必须回滚到切换前的值（否则界面显示已生效但实际没落库）',
  );

  console.log(
    'BLACKLIST_SWITCH_DUAL_ENTRY 系统管理页四处齐备；黑名单页死引用已删且读写同 key；失败回滚已断言',
  );
});
