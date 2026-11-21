// 「我的脚本」页「跳过绑定门槛」按钮的纯函数层（2026-11-17 用户需求）。
//
// 用户原话：「请添加一个按钮，在我的脚本里面，该按钮为仅管理员可见，用于跳过这一限制」。
//
// # 为什么单独一层 .mjs
//
// `npm run test:unit` 只跑 `src/utils/*.test.mjs`。而这条功能的判据全是纯计算：
//   - 按钮该不该显示（仅管理员）
//   - 点了之后要不要二次确认（这是绕过合规门槛，不是普通操作）
//   - 提交时 payload 里该带什么
// 不需要 DOM 就能测，写在 index.vue 里就完全没有单测入口。
//
// # 三条纪律
//
//  1. **后端才是真正拦人的地方**。这里只决定「看不看得见按钮」，
//     绝不能把它当成授权判断（前端隐藏只是可用性）。
//  2. **判据写字面量**，不引用后端常量 —— 常量漂移时前端测试应当红。
//  3. **默认不跳过**。忘记传参时必须保持门槛生效，不能因为「没读到」而放行。

/** 后端路由返回的稳定业务码：缺少手机/微信绑定。 */
const BINDINGS_REQUIRED_CODE = 'SCRIPT_BINDINGS_REQUIRED';

/** 后端错误文案。仅用于在提示里原样回显，不作为判据。 */
const BINDINGS_REQUIRED_MESSAGE = '创建脚本前请绑定手机号和微信';

/**
 * 创建脚本时是否应该显示「跳过绑定门槛」按钮。
 *
 * @param {{isAdmin: boolean}} ctx
 * @returns {boolean} 仅管理员为 true
 */
export function shouldShowSkipBindingButton(ctx) {
  return Boolean(ctx && ctx.isAdmin === true);
}

/**
 * 合并「跳过绑定」意图到创建脚本的 payload。
 *
 * 关键：**只有显式 true 才写字段**。false / undefined / 非布尔值一律不写，
 * 让后端看到「未请求跳过」而不是「请求不跳过」。两者对后端等价，
 * 但少一个字段意味着前端不会因为默认值写错而意外表达意图。
 *
 * @param {object} payload 原始创建脚本 payload
 * @param {boolean} skip 是否请求跳过
 * @returns {object} 新对象，不修改入参
 */
export function buildCreateScriptPayload(payload, skip) {
  const base = payload && typeof payload === 'object' ? { ...payload } : {};
  if (skip === true) {
    base.skipBindingCheck = true;
  } else {
    // 即便调用方传进来的 payload 里已经带了脏值，也要清掉 ——
    // 否则「上一次勾选过，这次没勾」会继续带着 true 提交。
    delete base.skipBindingCheck;
  }
  return base;
}

/**
 * 判断一次创建失败是否属于「缺绑定」。
 *
 * 用途：后端 403 时提示用户「你可以让管理员跳过」或「去绑定」，
 * 而不是把原始业务码抛给用户看。
 *
 * @param {unknown} error 请求错误（可能是 axios 风格或已解析的响应体）
 * @returns {boolean}
 */
export function isBindingsRequiredError(error) {
  if (!error || typeof error !== 'object') return false;
  const body = error.response && typeof error.response === 'object' && error.response.data
    ? error.response.data
    : error;
  if (!body || typeof body !== 'object') return false;
  const code = typeof body.code === 'string' ? body.code.trim() : '';
  if (code === BINDINGS_REQUIRED_CODE) return true;
  // 没有 code 时退回文案匹配：老后端/网关改写场景下 code 可能丢失，
  // 此时至少能给出同样的引导，而不是把 403 显示成「创建失败」。
  const message = typeof body.message === 'string' ? body.message.trim() : '';
  return message === BINDINGS_REQUIRED_MESSAGE;
}

/**
 * 二次确认的内容。绕过的是合规门槛，必须说清后果。
 *
 * @param {{isAdmin: boolean}} ctx
 * @returns {{title: string, content: string, okText: string, cancelText: string}}
 */
export function buildSkipBindingConfirmText(ctx) {
  const isAdmin = Boolean(ctx && ctx.isAdmin === true);
  return {
    title: '跳过手机号/微信绑定校验？',
    content: isAdmin
      ? '本次创建将不校验这个账号是否绑定了手机号和微信。该账号若未绑定，出问题时不便于追溯责任人。仅建议在自用或已线下核实身份的情况下使用。'
      : '只有管理员可以跳过该限制。',
    okText: '仍然跳过',
    cancelText: '取消',
  };
}
