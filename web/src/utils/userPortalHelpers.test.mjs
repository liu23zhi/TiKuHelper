import test from 'node:test';
import assert from 'node:assert/strict';
import * as userPortalHelpers from './userPortalHelpers.mjs';

import {
  buildPortalMenuKeys,
  buildRegisterSourceLabel,
  buildResetPasswordPayload,
  buildBillingRecordAmountText,
  buildBillingRecordFinalMultiplierText,
  buildBillingRecordEffectiveMultiplierText,
  buildBillingRecordListSubjectText,
  formatBillingRecordTime,
  buildBillingRecordModeText,
  buildBillingRecordFeeDetailHtml,
  buildBillingRecordFeeDetails,
  buildBillingRecordReferenceText,
  buildBillingRecordSourceText,
  buildBillingRecordSubjectText,
  buildBillingRecordTypeText,
  buildPersonalUsageBillingRecordParams,
  buildPersonalUsageQuestionText,
  buildPersonalUsageAnswerSourceText,
  isStrongAdminPassword,
  runAdminPasswordAction,
  buildUserBalanceUpdatePayload,
  resolveAdapterHomePath,
} from './userPortalHelpers.mjs';

test('billing cache hit rate formats known ratios and leaves missing usage unknown', () => {
  assert.equal(typeof userPortalHelpers.formatBillingCacheHitRate, 'function')
  const format = userPortalHelpers.formatBillingCacheHitRate
  for (const [rate, expected] of [[0, '0%'], [0.1, '10%'], [0.3333333, '33.33%'], [0.46, '46%'], [0.001, '0.1%'], [1, '100%']]) {
    assert.equal(format(rate), expected)
  }
  for (const value of [null, undefined, NaN, Infinity, -0.1, 1.1, '', '0.1']) {
    assert.equal(format(value), '-')
  }
})

test('buildBillingRecordModeText renders deep thinking and ordinary search', () => {
  assert.equal(buildBillingRecordModeText({ searchModeLabel: '深度思考', searchMode: 'deep' }), '深度思考')
  assert.equal(buildBillingRecordModeText({ searchModeLabel: '普通搜索', searchMode: 'normal' }), '普通搜索')
  // 后端只给原始值时前端兜底映射（大小写不敏感）。
  assert.equal(buildBillingRecordModeText({ searchMode: 'DEEP' }), '深度思考')
  assert.equal(buildBillingRecordModeText({ searchMode: 'normal' }), '普通搜索')
  assert.equal(buildBillingRecordModeText({ searchMode: 'deep_native' }), '深度思考 原生工具')
  assert.equal(buildBillingRecordModeText({ searchMode: 'deep_mcp' }), '深度思考 MCP编排')
  // 非搜题账单与历史行没有模式，降级为占位符。
  assert.equal(buildBillingRecordModeText({}), '-')
  assert.equal(buildBillingRecordModeText({ searchMode: 'unknown' }), '-')
  assert.equal(buildBillingRecordModeText(null), '-')
})

test('resolveAdapterHomePath sends admins to component and users to usage page', () => {
  assert.equal(resolveAdapterHomePath({ role: 'admin' }), '/adapter/component');
  assert.equal(resolveAdapterHomePath({ role: 'user' }), '/adapter/user-usage');
  assert.equal(resolveAdapterHomePath(null), '/adapter/user-usage');
});

test('buildRegisterSourceLabel maps system registrations to initialization wording', () => {
  assert.equal(buildRegisterSourceLabel('system'), '系统初始化创建');
  assert.equal(buildRegisterSourceLabel('public'), '公开注册');
});

test('buildPortalMenuKeys includes personal usage for admin users', () => {
  assert.deepEqual(buildPortalMenuKeys({ role: 'admin' }), [
    'profile',
    'component',
    'import',
    'user-usage',
    'userlist',
    'loglist',
    'recharge-orders',
    'billing-records',
    'search-account-config',
    'ai-search-config',
    'system-settings',
    'access-tokens',
    'scripts',
    'tickets',
  ]);
  assert.deepEqual(buildPortalMenuKeys({ role: 'user' }), [
    'profile',
    'user-usage',
    'access-tokens',
    'scripts',
    'recharge-orders',
    'billing-records',
    'tickets',
  ]);
});

test('buildPersonalUsageBillingRecordParams always requests own records', () => {
  assert.deepEqual(buildPersonalUsageBillingRecordParams(3, 20), {
    page: 3,
    pageSize: 20,
    scope: 'self',
  });
});

test('buildPersonalUsageQuestionText prefers explicit question text and falls back cleanly', () => {
  assert.equal(buildPersonalUsageQuestionText({ questionText: '缓存命中题目', providerLabel: '万能题库（缓存）' }), '缓存命中题目');
  assert.equal(buildPersonalUsageQuestionText({ question: '直接题目字段' }), '直接题目字段');
  assert.equal(buildPersonalUsageQuestionText({ providerLabel: '万能题库（缓存）' }), '万能题库（缓存）');
  assert.equal(buildPersonalUsageQuestionText({}), '-');
});

test('buildPersonalUsageAnswerSourceText prefers answer source label before provider label', () => {
  assert.equal(buildPersonalUsageAnswerSourceText({ answerSourceLabel: '万能题库（缓存）', providerLabel: '本地题库' }), '万能题库（缓存）');
  assert.equal(buildPersonalUsageAnswerSourceText({ providerLabel: 'Icodef 题库' }), 'Icodef 题库');
  assert.equal(buildPersonalUsageAnswerSourceText({ provider: 'icodef' }), 'icodef');
  assert.equal(buildPersonalUsageAnswerSourceText({}), '-');
});

test('buildPersonalUsageAnswerSourceText renders admin balance adjustments by operator id', () => {
  assert.equal(buildPersonalUsageAnswerSourceText({
    type: 'balance_adjustment',
    operatorId: 1,
    detail: '操作管理员：admin (ID=1)；余额 ¥ 1 → ¥ 2',
  }), '操作管理员ID：1');
});

test('admin weak-password check matches the former complexity rule', () => {
  assert.equal(isStrongAdminPassword('Abc12345!'), true);
  assert.equal(isStrongAdminPassword('12345678'), false);
  assert.equal(isStrongAdminPassword('abcdefgh'), false);
  assert.equal(isStrongAdminPassword(''), false);
});

test('weak admin password action waits for confirmation and never submits on cancel', async () => {
  let submitted = 0;
  let asked = 0;
  const canceled = await runAdminPasswordAction('12345678', async () => { submitted += 1 }, async () => {
    asked += 1;
    return false;
  });
  assert.equal(canceled, false);
  assert.equal(asked, 1);
  assert.equal(submitted, 0);

  const confirmed = await runAdminPasswordAction('12345678', async () => { submitted += 1 }, async () => true);
  assert.equal(confirmed, true);
  assert.equal(submitted, 1);
});

test('strong admin password action submits without an extra confirmation', async () => {
  let submitted = 0;
  const result = await runAdminPasswordAction('Abc12345!', async () => { submitted += 1 }, async () => {
    throw new Error('strong passwords should not require confirmation');
  });
  assert.equal(result, true);
  assert.equal(submitted, 1);
});

test('billing record helpers render recharge and search ledger rows', () => {
  const recharge = {
    type: 'recharge',
    typeLabel: '充值入账',
    direction: 'income',
    subject: '账户充值',
    detail: '账户充值',
    providerLabel: 'epay',
    referenceNo: 'rcg_user_20260621002829_100_ABC12345',
    amount: 1,
  };
  assert.equal(buildBillingRecordTypeText(recharge), '充值入账');
  assert.equal(buildBillingRecordSubjectText(recharge), '账户充值');
  assert.equal(buildBillingRecordSourceText(recharge), '账户充值');
  assert.equal(buildBillingRecordReferenceText(recharge), 'rcg_user_20260621002829_100_ABC12345');
  // 金额最多 6 位小数并去掉末尾多余的 0，因此整数金额显示为 '+¥ 1'。
  assert.equal(buildBillingRecordAmountText(recharge), '+¥ 1');

  const search = {
    type: 'normal_search',
    typeLabel: '题目消费',
    direction: 'expense',
    questionText: '搜索题目',
    answerSourceLabel: 'Icodef 题库',
    referenceNo: 'req-search',
    amount: 1.25,
  };
  assert.equal(buildBillingRecordTypeText(search), '题目消费');
  assert.equal(buildBillingRecordTypeText({ type: 'normal_search' }), '题目消费');
  assert.equal(buildBillingRecordTypeText({ type: 'script_reservation' }), '脚本预扣');
  assert.equal(buildBillingRecordTypeText({ type: 'script_refund' }), '脚本退款');
  assert.equal(buildBillingRecordTypeText({ type: 'script_activation_purchase' }), '脚本激活码购买');
  assert.equal(buildBillingRecordSubjectText(search), '搜索题目');
  assert.equal(buildBillingRecordSourceText(search), 'Icodef 题库');
  assert.equal(buildBillingRecordReferenceText(search), 'req-search');
  assert.equal(buildBillingRecordAmountText(search), '-¥ 1.25');
});

test('billing record fee helpers render per-provider and total charges', () => {
  const record = {
    subject: '搜索答案，题目：混合题目',
    direction: 'expense',
    amount: 2,
    feeDetails: [
      { provider: 'icodef', providerLabel: 'Icodef 题库', amount: 1.25 },
      { provider: 'ai', providerLabel: 'AI 搜索', amount: 0.75 },
    ],
  };

  assert.deepEqual(buildBillingRecordFeeDetails(record), record.feeDetails);

  const html = buildBillingRecordFeeDetailHtml(record);
  assert.match(html, /class="billing-fee-detail"/);
  assert.match(html, /class="billing-fee-card billing-fee-card--question billing-fee-overview"/);
  assert.match(html, /class="billing-fee-card billing-fee-card--cost"/);
  assert.match(html, /class="billing-fee-section-title"/);
  assert.match(html, /支出账单/);
  assert.match(html, /题目消费/);
  assert.match(html, />费用详情</);
  assert.match(html, /2 项/);
  assert.match(html, /搜索答案，题目：混合题目/);
  assert.match(html, /Icodef 题库/);
  assert.match(html, /-¥ 1\.25/);
  assert.match(html, /AI 模型调用/);
  assert.match(html, /-¥ 0\.75/);
  assert.match(html, /class="billing-fee-item"/);
  assert.match(html, /class="billing-fee-total"/);
  assert.match(html, /合计/);
  // 合计 2 元按去尾零规则显示为 -¥ 2。
  assert.match(html, /-¥ 2(?!\.)/);
});

test('buildBillingRecordFinalMultiplierText renders server-provided final multipliers', () => {
  assert.equal(buildBillingRecordFinalMultiplierText({ finalMultiplier: '4x' }), '4x');
  assert.equal(buildBillingRecordFinalMultiplierText({ finalMultiplier: '4x、1.3x' }), '4x、1.3x');
  assert.equal(buildBillingRecordFinalMultiplierText({ type: 'normal_search' }), '1x');
  assert.equal(buildBillingRecordFinalMultiplierText({ type: 'recharge' }), '-');
});

test('billing list uses the comprehensive multiplier while fee detail keeps each original round', () => {
  const record = {
    type: 'normal_search',
    effectiveMultiplier: '1.84x',
    finalMultiplier: '4x、1.3x',
    questionText: '题目原文',
    subject: '搜索答案，题目：题目原文',
    direction: 'expense',
    feeDetails: [{ provider: 'ai', category: 'AI模型调用', callIndex: 1, amount: 0.01, detail: 'totalTokens=100；finalMultiplier=4x' },
      { provider: 'ai', category: 'AI模型调用', callIndex: 2, amount: 0.02, detail: 'totalTokens=200；finalMultiplier=1.3x' }],
  };
  assert.equal(buildBillingRecordEffectiveMultiplierText(record), '1.84x');
  assert.equal(buildBillingRecordFinalMultiplierText(record), '4x、1.3x');
  assert.equal(buildBillingRecordListSubjectText(record), '题目原文');
  assert.equal(buildBillingRecordSubjectText(record), '搜索答案，题目：题目原文');
  const html = buildBillingRecordFeeDetailHtml(record);
  assert.match(html, /题目原文/);
  assert.match(html, /4x/);
  assert.match(html, /1\.3x/);
  assert.doesNotMatch(html, /1\.84x/);
  assert.equal(buildBillingRecordEffectiveMultiplierText({ type: 'normal_search', finalMultiplier: '4x' }), '-');
});

test('billing time is centered-ready two-line Shanghai time', () => {
  assert.equal(formatBillingRecordTime('2026-10-25T02:54:04Z'), '2026-10-25\n10:54:04');
  assert.equal(formatBillingRecordTime(null), '-');
});

test('billing record fee details expose per-search query and render it as escaped text', () => {
  const record = {
    direction: 'expense',
    amount: 0.04,
    feeDetails: [{
      provider: 'ai_search',
      providerLabel: '联网搜索',
      amount: 0.02,
      detail: '搜索词：<加法验算>；状态：success；结果：2',
    }],
  };

  assert.equal(buildBillingRecordFeeDetails(record)[0].detail, '搜索词：<加法验算>；状态：success；结果：2');
  const html = buildBillingRecordFeeDetailHtml(record);
  assert.match(html, /billing-fee-meta-label">关键词：<\/span>\s*<span class="billing-fee-meta-value">&lt;加法验算&gt;<\/span>/);
});

test('personal billing detail hides AI pricing metadata while retaining keyword and web URL', () => {
  const record = {
    direction: 'expense',
    amount: 0.04,
    feeDetails: [
      {
        provider: 'ai_search',
        providerLabel: '联网搜索',
        amount: 0.01,
        detail: '搜索词：社会媒体；状态：success',
      },
      {
        provider: 'ai',
        category: 'AI模型调用',
        amount: 0.02,
        detail: 'model=gpt-6.1-sol；totalTokens=5949；finalMultiplier=2.06x',
      },
      {
        provider: 'web_read',
        category: '网页转Markdown',
        amount: 0.01,
        url: 'https://example.com/article?id=42&lang=zh',
        detail: '状态：completed',
      },
    ],
    showInStatement: true,
    canEditStatement: true,
  }

  const html = buildBillingRecordFeeDetailHtml(record, {
    showAIModelMeta: false,
    showStatementControl: false,
  })
  assert.match(html, /关键词：<\/span>\s*<span class="billing-fee-meta-value">社会媒体<\/span>/)
  assert.match(html, /网页：<\/span>\s*<span class="billing-fee-meta-value">https:\/\/example\.com\/article\?id=42&amp;lang=zh<\/span>/)
  assert.doesNotMatch(html, /gpt-6\.1-sol/)
  assert.doesNotMatch(html, /5949/)
  assert.doesNotMatch(html, /2\.06x/)
  assert.doesNotMatch(html, /显示在用户账单流水/)
})

test('web read fee details prefer the structured URL and retain it in normalized items', () => {
  const record = {
    direction: 'expense',
    amount: 0.01,
    feeDetails: [{
      provider: 'web_read',
      providerLabel: '网页读取',
      amount: 0.01,
      url: 'https://example.com/read?q=中 文&part=1',
      detail: '网页：旧详情地址；状态：completed',
    }],
  }
  assert.equal(buildBillingRecordFeeDetails(record)[0].url, 'https://example.com/read?q=中 文&part=1')
  const html = buildBillingRecordFeeDetailHtml(record)
  assert.match(html, />浏览网页</)
  assert.match(html, /https:\/\/example\.com\/read\?q=中 文&amp;part=1/)
  assert.doesNotMatch(html, /旧详情地址/)
})

test('legacy web read detail retains query parameters and escapes URL text', () => {
  const html = buildBillingRecordFeeDetailHtml({
    direction: 'expense',
    amount: 0.01,
    feeDetails: [{
      provider: 'web_read',
      providerLabel: '网页读取',
      amount: 0.01,
      detail: '网页：https://example.com/read?key=value&next=%3Cscript%3E；状态：completed',
    }],
  })
  assert.match(html, />浏览网页</)
  assert.match(html, /网页：<\/span>\s*<span class="billing-fee-meta-value">https:\/\/example\.com\/read\?key=value&amp;next=%3Cscript%3E<\/span>/)
})

test('billing fee item labels distinguish model calls, internet search, local Tiku search, browsing and OCR', () => {
  const html = buildBillingRecordFeeDetailHtml({
    direction: 'expense',
    amount: 0.03,
    feeDetails: [
      { provider: 'ai', category: 'AI模型调用', callIndex: 1, amount: 0.01 },
      { provider: 'ai_search', category: '联网搜索', callIndex: 1, amount: 0.01 },
      { provider: 'tiku_search', category: '本地题库检索', callIndex: 1, amount: 0.01 },
      { provider: 'web_read', category: '网页转Markdown', callIndex: 1, amount: 0.01 },
      { provider: 'ai_ocr', category: '图片 OCR', callIndex: 1, amount: 0 },
    ],
  })
  assert.match(html, />AI 模型调用 第1次</)
  assert.match(html, />联网搜索</)
  assert.match(html, />本地题库检索 第1次</)
  assert.match(html, />浏览网页 第1次</)
  assert.match(html, />图片 OCR 第1次</)
})

test('billing local Tiku search detail shows escaped query and candidate count', () => {
  const html = buildBillingRecordFeeDetailHtml({
    direction: 'expense',
    amount: 0.01,
    feeDetails: [{
      provider: 'tiku_search',
      category: '本地题库检索',
      amount: 0.01,
      detail: 'query=比较 <script>；candidate_count=3',
    }],
  })

  assert.match(html, />本地题库检索</)
  assert.match(html, /检索词：<\/span>\s*<span class="billing-fee-meta-value">比较 &lt;script&gt;<\/span>/)
  assert.match(html, /候选数：<\/span>\s*<span class="billing-fee-meta-value">3<\/span>/)
  assert.doesNotMatch(html, /比较 <script>/)
})

test('billing OCR fee metadata keeps the image URL and recognized character count', () => {
  const html = buildBillingRecordFeeDetailHtml({
    direction: 'expense',
    amount: 0,
    feeDetails: [{
      provider: 'ai_ocr',
      category: '图片 OCR',
      callIndex: 1,
      amount: 0,
      detail: '图片：https://images.example/question.webp；文字字符数：42；状态：success',
    }],
  })
  assert.match(html, />图片 OCR 第1次</)
  assert.match(html, /图片：<\/span>\s*<span class="billing-fee-meta-value">https:\/\/images\.example\/question\.webp<\/span>/)
  assert.match(html, /文字字符数：<\/span>\s*<span class="billing-fee-meta-value">42<\/span>/)
  assert.match(html, /-¥ 0/)
})

test('billing record fee details render friendly AI search and model call labels', () => {
  const record = {
    direction: 'expense',
    amount: 0.03,
    feeDetails: [
      {
        provider: 'ai_search',
        providerLabel: '联网搜索',
        amount: 0.02,
        detail: '搜索词：怎么弄；状态：success；结果：2',
      },
      {
        provider: 'ai',
        providerLabel: 'AI 搜索',
        category: 'AI模型调用',
        callIndex: 1,
        amount: 0.01,
        detail: 'model=gpt-6-sol；round=1；attempt=1；totalTokens=2000；stageMultiplier=1.9700；finalMultiplier=1.9700',
      },
    ],
  }

  const html = buildBillingRecordFeeDetailHtml(record)
  assert.match(html, />联网搜索</)
  assert.match(html, /billing-fee-meta-label">关键词：<\/span>\s*<span class="billing-fee-meta-value">怎么弄<\/span>/)
  assert.match(html, /AI 模型调用 第1次/)
  assert.match(html, /billing-fee-meta-label">模型：<\/span>\s*<span class="billing-fee-meta-value">gpt-6-sol<\/span>/)
  assert.match(html, /billing-fee-meta-label">最终倍率：<\/span>\s*<span class="billing-fee-meta-value">1\.97x<\/span>/)
  assert.match(html, /billing-fee-meta-label">Token数量：<\/span>\s*<span class="billing-fee-meta-value">2,000<\/span>/)
  assert.match(html, /-¥ 0\.02/)
  assert.match(html, /-¥ 0\.01/)
  assert.match(html, /合计/)
  assert.match(html, /-¥ 0\.03/)
  assert.doesNotMatch(html, /model=gpt-6-sol/)
  assert.doesNotMatch(html, /round=1/)
  assert.doesNotMatch(html, /attempt=1/)
  assert.doesNotMatch(html, /stageMultiplier=1\.9700/)
})

test('billing record fee helpers fall back to the row provider for legacy records', () => {
  assert.deepEqual(buildBillingRecordFeeDetails({
    providerLabel: 'AI 搜索',
    amount: 0.06,
  }), [
    {
      provider: '',
      providerLabel: 'AI 搜索',
      amount: 0.06,
    },
  ]);
});

test('buildResetPasswordPayload requires non-empty password', () => {
  assert.deepEqual(buildResetPasswordPayload('  New12345!  '), { password: 'New12345!' });
  assert.throws(() => buildResetPasswordPayload(''), /请输入新密码/);
  assert.deepEqual(buildResetPasswordPayload('12345678'), { password: '12345678' });
  assert.throws(() => buildResetPasswordPayload('a'.repeat(73)), /72/);
});

test('admin password helper distinguishes strong and weak non-empty passwords', () => {
  assert.equal(userPortalHelpers.isStrongAdminPassword('Abc12345!'), true);
  assert.equal(userPortalHelpers.isStrongAdminPassword('12345678'), false);
  assert.equal(userPortalHelpers.isStrongAdminPassword(''), false);
});

test('buildAdminCreateUserPayload trims fields and keeps admin-created defaults', () => {
  assert.equal(typeof userPortalHelpers.buildAdminCreateUserPayload, 'function');
  assert.deepEqual(userPortalHelpers.buildAdminCreateUserPayload({
    username: '  new_user  ',
    password: '  Abc12345!  ',
    nickname: '  测试用户  ',
    role: ' admin ',
    status: ' disabled ',
    registerSource: '  ',
    giftEnabled: true,
    giftAmount: ' 6.50 ',
  }), {
    username: 'new_user',
    password: 'Abc12345!',
    nickname: '测试用户',
    role: 'admin',
    status: 'disabled',
    registerSource: 'admin',
    giftEnabled: true,
    giftAmount: 6.5,
  });
});

test('buildAdminCreateUserPayload rejects invalid and overlong usernames', () => {
  assert.throws(() => userPortalHelpers.buildAdminCreateUserPayload({ username: 'user-name', password: 'Abc12345!' }), /用户名/)
  assert.throws(() => userPortalHelpers.buildAdminCreateUserPayload({ username: 'https://example.com', password: 'Abc12345!' }), /用户名/)
  assert.throws(() => userPortalHelpers.buildAdminCreateUserPayload({ username: '用'.repeat(256), password: 'Abc12345!' }), /用户名/)
})

test('buildAdminCreateUserPayload accepts non-empty weak password and enforces bcrypt byte limit', () => {
  assert.equal(userPortalHelpers.buildAdminCreateUserPayload({ username: 'weak_user', password: '12345678', giftEnabled: false }).password, '12345678');
  assert.equal(userPortalHelpers.buildAdminCreateUserPayload({ username: 'ascii_72', password: 'a'.repeat(72), giftEnabled: false }).password.length, 72);
  assert.throws(() => userPortalHelpers.buildAdminCreateUserPayload({ username: 'ascii_73', password: 'a'.repeat(73), giftEnabled: false }), /72/);
  assert.throws(() => userPortalHelpers.buildAdminCreateUserPayload({ username: 'unicode_limit', password: '中'.repeat(25), giftEnabled: false }), /72/);
});

test('billing record fee details render call category and admin metadata', () => {
  const record = {
    type: 'balance_adjustment',
    direction: 'income',
    subject: '管理员调整余额',
    amount: 2,
    balanceBefore: 1,
    balanceAfter: 3,
    adjustmentMode: 'increase',
    operatorId: 7,
    operatorUsername: 'admin',
    showInStatement: false,
    canEditStatement: true,
    feeDetails: [{ provider: 'ai', providerLabel: 'AI 搜索', category: 'AI模型调用', callIndex: 1, amount: 2, detail: 'model=gpt-6-sol；totalTokens=10' }],
  }
  const html = buildBillingRecordFeeDetailHtml(record)
  assert.match(html, /AI 模型调用 第1次/)
  assert.match(html, /操作管理员：admin（ID 7）/)
  assert.match(html, /调整模式：添加余额/)
  assert.match(html, /余额变化：¥ 1 → ¥ 3/)
  assert.match(html, /账单流水展示：隐藏/)
  assert.match(html, /<button type="button" role="switch" aria-label="显示在用户账单流水" aria-checked="false" class="billing-statement-switch billing-statement-toggle"/)
  assert.match(html, /billing-statement-switch__thumb/)
  assert.doesNotMatch(html, /type="checkbox"/)
})

test('buildUserBalanceUpdatePayload trims fields and keeps edit mode data', () => {
  assert.deepEqual(buildUserBalanceUpdatePayload({
    id: 9,
    nickname: '  管理员修改  ',
    role: ' admin ',
    status: ' disabled ',
    phone: ' 13800000000 ',
    forceReassignPhone: true,
    wechatId: ' openid-edit ',
    forceReassignWechat: true,
    balance: ' 12.50 ',
    balanceMode: 'increase',
  }), {
    id: 9,
    nickname: '管理员修改',
    role: 'admin',
    status: 'disabled',
    phone: '13800000000',
    forceReassignPhone: true,
    wechatId: 'openid-edit',
    forceReassignWechat: true,
    balance: 12.5,
    balanceMode: 'increase',
  });
});

test('buildUserBalanceUpdatePayload omits balance when empty but keeps phone clearing payload', () => {
  assert.deepEqual(buildUserBalanceUpdatePayload({
    id: 10,
    nickname: 'test',
    role: 'user',
    status: 'enabled',
    phone: '   ',
    forceReassignPhone: false,
    wechatId: '   ',
    forceReassignWechat: false,
    balance: '   ',
    balanceMode: 'override',
  }), {
    id: 10,
    nickname: 'test',
    role: 'user',
    status: 'enabled',
    phone: '',
    forceReassignPhone: false,
    wechatId: '',
    forceReassignWechat: false,
  });
});

test('buildUserBalanceUpdatePayload rejects invalid balance values', () => {
  assert.throws(() => buildUserBalanceUpdatePayload({
    id: 11,
    nickname: 'test',
    role: 'user',
    status: 'enabled',
    balance: 'abc',
    balanceMode: 'decrease',
  }), /请输入有效的余额数值/);
});
