// 管理员页「默认是否显示全站数据」的钉子。
//
// 真实事故：用户报「令牌管理和脚本管理是管理员页面，需要显示所有用户的数据」。
// 后端完全正确（controller/access_token.go:62 硬与 scope=='all' && isAdminUser；
// service 层管理员分支不带 user_id 过滤）。问题在前端默认值：
//   AccessTokenManagement/index.vue  showAllTokens = ref(false)
// 唯一置 true 的地方是用户**手动点按钮**。所以管理员进「令牌管理」看到的是自己那一条，
// 必须再点一次「显示全部令牌」才能看全站 —— 用户抱怨字面为真。
//
// 这组用例钉：默认值必须跟随路由声明的视图模式，而不是硬编码 false。

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const webSrc = path.join(here, '..');

const read = (rel) => readFile(path.join(webSrc, rel), 'utf8');

// 去掉注释，避免把「解释为什么不能这样写」的说明误判成实现。
const stripComments = (source) =>
  source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '');

test('令牌管理页的「显示全部」默认值必须跟随 viewMode=admin', async () => {
  const raw = await read('pages/AccessTokenManagement/index.vue');
  const source = stripComments(raw);
  // 判定的是「有没有一条路径会在 admin 视图下把 showAllTokens 设为 true」，
  // 不钉具体写法（ref 初值 / watch immediate 都算合格）。
  //
  // 为什么钉行为而不是钉写法：我最初写的是 ref(routeViewMode.value === 'admin')，
  // 但 routeViewMode 在下方才定义，直接引用会踩 TDZ，所以实现改成了
  // watch(routeViewMode, ..., {immediate:true})。钉写法的断言会在这种等价重构下假红。
  assert.match(
    source,
    /showAllTokens\.value\s*=\s*true/,
    '必须有代码在管理员视图下把 showAllTokens 置 true —— ' +
      '否则管理员进「令牌管理」只看到自己的令牌，必须再点一次按钮才看全站（用户抱怨的就是这个）',
  );
  // 关键：那条置 true 必须与 admin 视图判断挂在一起，不能是无条件置 true
  // （无条件置 true 会让「我的令牌」也请求全站数据，属于越权）。
  assert.match(
    source,
    /mode\s*===\s*'admin'[\s\S]{0,120}?showAllTokens\.value\s*=\s*true|showAllTokens\.value\s*=\s*true[\s\S]{0,120}?mode\s*===\s*'admin'/,
    '置 true 必须受 admin 视图判断约束（无条件置 true 会让「我的令牌」越权看全站）',
  );
});

test('切到用户入口必须仍然复位成「只看自己」（防越权，这条不能被上面的改动破坏）', async () => {
  const raw = await read('pages/AccessTokenManagement/index.vue');
  const source = stripComments(raw);
  // 既有的 watch(isAdmin) 复位逻辑必须保留：管理员勾了「显示全部」再点「我的令牌」，
  // 若不复位就会带着 scope=all 请求 ⇒「我的令牌」显示全站数据，正好违反另一条诉求。
  assert.match(
    source,
    /watch\(\s*isAdmin\s*,/,
    '必须保留 watch(isAdmin) 的复位逻辑',
  );
  assert.match(
    source,
    /if\s*\(\s*!\s*adminNow[^)]*\)\s*showAllTokens\.value\s*=\s*false/,
    '切到非管理员视图时必须把 showAllTokens 置 false（否则「我的令牌」会显示全站数据）',
  );
});

test('my-tokens 与 token-admin 必须用 meta.viewMode 区分（不是靠路径猜）', async () => {
  const router = await read('router/router.js');
  const myTokens = router.match(/name:\s*'my-tokens'[\s\S]{0,400}?viewMode:\s*'(\w+)'/);
  const tokenAdmin = router.match(/name:\s*'token-admin'[\s\S]{0,400}?viewMode:\s*'(\w+)'/);
  assert.ok(myTokens, 'my-tokens 路由必须声明 viewMode');
  assert.ok(tokenAdmin, 'token-admin 路由必须声明 viewMode');
  assert.equal(myTokens[1], 'self', 'my-tokens 的 viewMode 必须是 self');
  assert.equal(tokenAdmin[1], 'admin', 'token-admin 的 viewMode 必须是 admin');
});
