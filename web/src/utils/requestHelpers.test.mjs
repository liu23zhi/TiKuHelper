import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildAuthFailureHandling,
  buildResponsePayload,
} from './requestHelpers.mjs';

test('buildAuthFailureHandling keeps session for negative-balance default-account suspension', () => {
  assert.deepEqual(buildAuthFailureHandling({
    status: 401,
    data: {
      message: '访问令牌鉴权失败',
      detail: '当前余额不足，默认账号已停用，请传入渠道参数后重试',
    },
  }), {
    shouldLogout: false,
    userMessage: '当前余额不足，默认账号已停用，请传入渠道参数后重试',
  });
});

test('buildAuthFailureHandling logs out for expired login responses', () => {
  assert.deepEqual(buildAuthFailureHandling({
    status: 401,
    data: {
      message: '访问令牌鉴权失败',
      detail: '访问令牌不存在',
    },
  }), {
    shouldLogout: true,
    userMessage: '当前登陆过期，请重新登陆。',
  });
});

test('buildResponsePayload prefers backend detail when present', () => {
  assert.deepEqual(buildResponsePayload({
    status: 401,
    data: {
      message: '访问令牌鉴权失败',
      detail: '当前余额不足，默认账号已停用，请传入渠道参数后重试',
    },
  }), {
    data: {
      message: '访问令牌鉴权失败',
      detail: '当前余额不足，默认账号已停用，请传入渠道参数后重试',
    },
    status: 401,
    message: '当前余额不足，默认账号已停用，请传入渠道参数后重试',
  });
});
