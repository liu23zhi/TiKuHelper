import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildPublicSMSCodePayload,
  buildSMSLoginPayload,
  buildPublicRegisterPayload,
  limitUsernameInput,
} from './phoneAuthHelpers.mjs';

test('buildPublicSMSCodePayload trims phone and keeps scene', () => {
  assert.deepEqual(buildPublicSMSCodePayload({ phone: ' 13800000000 ' }, 'login'), {
    phone: '13800000000',
    scene: 'login',
  });
});

test('buildPublicSMSCodePayload includes captcha token', () => {
  assert.deepEqual(buildPublicSMSCodePayload({ phone: ' 13800000000 ', captchaToken: ' token-1 ' }, 'login'), {
    phone: '13800000000',
    scene: 'login',
    captchaToken: 'token-1',
  });
});

test('buildSMSLoginPayload requires phone and sms code', () => {
  assert.deepEqual(buildSMSLoginPayload({ phone: ' 13800000000 ', smsCode: ' 123456 ', captchaToken: ' token-2 ' }), {
    phone: '13800000000',
    smsCode: '123456',
    captchaToken: 'token-2',
  });

  assert.throws(() => buildSMSLoginPayload({ phone: '', smsCode: '123456', captchaToken: 'token-2' }), /请输入手机号/);
  assert.throws(() => buildSMSLoginPayload({ phone: '13800000000', smsCode: '', captchaToken: 'token-2' }), /请输入验证码/);
  assert.throws(() => buildSMSLoginPayload({ phone: '13800000000', smsCode: '123456', captchaToken: '' }), /请先完成人机验证/);
});

test('buildPublicRegisterPayload trims fields and requires phone and sms code', () => {
  assert.deepEqual(buildPublicRegisterPayload({
    username: ' new_user ',
    password: 'Abc12345!',
    nickname: ' 新用户 ',
    phone: ' 13800000000 ',
    smsCode: ' 654321 ',
    captchaToken: ' token-3 ',
  }), {
    username: 'new_user',
    password: 'Abc12345!',
    nickname: '新用户',
    phone: '13800000000',
    smsCode: '654321',
    captchaToken: 'token-3',
  });

  assert.throws(() => buildPublicRegisterPayload({ username: 'user', password: 'Abc12345!', phone: '', smsCode: '654321', captchaToken: 'token-3' }), /请输入手机号/);
  assert.throws(() => buildPublicRegisterPayload({ username: 'user', password: 'Abc12345!', phone: '13800000000', smsCode: '', captchaToken: 'token-3' }), /请输入验证码/);
  assert.throws(() => buildPublicRegisterPayload({ username: 'user', password: 'Abc12345!', phone: '13800000000', smsCode: '654321', captchaToken: '' }), /请先完成人机验证/);
  assert.throws(() => buildPublicRegisterPayload({ username: 'bad-name', password: 'Abc12345!', phone: '13800000000', smsCode: '654321', captchaToken: 'token-3' }), /用户名/);
  assert.throws(() => buildPublicRegisterPayload({ username: '用'.repeat(256), password: 'Abc12345!', phone: '13800000000', smsCode: '654321', captchaToken: 'token-3' }), /用户名/);
});

test('limitUsernameInput counts Unicode characters without rewriting invalid content', () => {
  assert.equal(limitUsernameInput('中'.repeat(256)), '中'.repeat(255))
  assert.equal(limitUsernameInput('https://example.com'), 'https://example.com')
})
