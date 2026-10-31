import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildAuthFailureHandling,
  buildResponsePayload,
  isPublicWechatRegisterRequest,
} from './requestHelpers.mjs';

test('buildAuthFailureHandling keeps session for negative-balance default-account suspension', () => {
  assert.deepEqual(buildAuthFailureHandling({
    status: 401,
    data: {
      message: '访问令牌鉴权失败',
      detail: '当前余额不足，系统题库已禁用，请充值后再使用',
    },
  }), {
    shouldLogout: false,
    userMessage: '当前余额不足，系统题库已禁用，请充值后再使用',
  });
});

test('buildAuthFailureHandling keeps session for captcha failures', () => {
  assert.deepEqual(buildAuthFailureHandling({
    status: 400,
    data: { message: '请先完成人机验证' },
  }), {
    shouldLogout: false,
    userMessage: '请先完成人机验证',
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

test('buildAuthFailureHandling explains when another device replaced the session', () => {
  assert.deepEqual(buildAuthFailureHandling({
    status: 401,
    data: { message: '账号已在其他设备登录，请重新登录' },
  }), {
    shouldLogout: true,
    userMessage: '账号已在其他设备登录，请重新登录',
  });
});

test('buildResponsePayload prefers backend detail when present', () => {
  assert.deepEqual(buildResponsePayload({
    status: 401,
    data: {
      message: '访问令牌鉴权失败',
      detail: '当前余额不足，系统题库已禁用，请充值后再使用',
    },
  }), {
    data: {
      message: '访问令牌鉴权失败',
      detail: '当前余额不足，系统题库已禁用，请充值后再使用',
    },
    status: 401,
    message: '当前余额不足，系统题库已禁用，请充值后再使用',
  });
});

test('public registration flow URLs are isolated from login auth handling', () => {
  assert.equal(isPublicWechatRegisterRequest('/adapter-service/public/wechat-register/session'), true);
  assert.equal(isPublicWechatRegisterRequest('/adapter-service/user/login'), false);
  assert.equal(isPublicWechatRegisterRequest(undefined), false);
});
