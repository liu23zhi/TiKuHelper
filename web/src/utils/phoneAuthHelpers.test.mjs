import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildPublicSMSCodePayload,
  buildSMSLoginPayload,
  buildPublicRegisterPayload,
} from './phoneAuthHelpers.mjs';

test('buildPublicSMSCodePayload trims phone and keeps scene', () => {
  assert.deepEqual(buildPublicSMSCodePayload({ phone: ' 13800000000 ' }, 'login'), {
    phone: '13800000000',
    scene: 'login',
  });
});

test('buildSMSLoginPayload requires phone and sms code', () => {
  assert.deepEqual(buildSMSLoginPayload({ phone: ' 13800000000 ', smsCode: ' 123456 ' }), {
    phone: '13800000000',
    smsCode: '123456',
  });

  assert.throws(() => buildSMSLoginPayload({ phone: '', smsCode: '123456' }), /请输入手机号/);
  assert.throws(() => buildSMSLoginPayload({ phone: '13800000000', smsCode: '' }), /请输入验证码/);
});

test('buildPublicRegisterPayload trims fields and requires phone and sms code', () => {
  assert.deepEqual(buildPublicRegisterPayload({
    username: ' new-user ',
    password: 'Abc12345!',
    nickname: ' 新用户 ',
    phone: ' 13800000000 ',
    smsCode: ' 654321 ',
  }), {
    username: 'new-user',
    password: 'Abc12345!',
    nickname: '新用户',
    phone: '13800000000',
    smsCode: '654321',
  });

  assert.throws(() => buildPublicRegisterPayload({ username: 'user', password: 'Abc12345!', phone: '', smsCode: '654321' }), /请输入手机号/);
  assert.throws(() => buildPublicRegisterPayload({ username: 'user', password: 'Abc12345!', phone: '13800000000', smsCode: '' }), /请输入验证码/);
});
