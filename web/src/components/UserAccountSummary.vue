<template>
  <a-descriptions :column="2" size="small" bordered class="account-summary" title="账号资料">
    <a-descriptions-item v-for="item in fields" :key="item.label" :label="item.label"><span class="account-summary__value">{{ item.value }}</span></a-descriptions-item>
  </a-descriptions>
</template>
<script>
import { computed } from 'vue'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { buildRegisterSourceLabel } from '@/utils/userPortalHelpers.mjs'
export default {
  props: { user: { type: Object, required: true } },
  setup(props) {
    const stamp = value => value ? new Date(value).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }) : '未记录'
    const fields = computed(() => {
      const u = props.user || {}, money = value => Number.isFinite(Number(value)) ? `¥ ${formatMoneyValue(value)}` : '未记录'
      return [
        ['用户 ID', u.id || u.userId], ['用户名', u.username], ['昵称', u.nickname],
        ['账号角色', u.role === 'admin' ? '管理员' : '普通用户'], ['账号状态', ({ enabled: '启用', disabled: '停用', banned: '封禁' })[u.status] || u.status],
        ['余额合计', money(Number(u.balance || 0) + Number(u.giftBalance || 0))], ['充值本金', money(u.balance)], ['赠送余额', money(u.giftBalance)],
        ['注册时间', stamp(u.createdAt)], ['注册来源', buildRegisterSourceLabel(u.registerSource)], ['最近登录', stamp(u.lastLoginAt)],
        ['注册 IP', u.registerIp], ['最近登录 IP', u.lastLoginIp], ['手机号', u.phone], ['微信标识', u.wechatId],
        ['手机绑定时间', stamp(u.phoneBoundAt)], ['微信绑定时间', stamp(u.wechatBoundAt)], ['邀请人', u.invitedByUserId ? `#${u.invitedByUserId}` : '无'],
        ['邀请绑定时间', stamp(u.invitedAt)], ['归档时间', stamp(u.archivedAt || u.deletedAt)], ['归档原因', ({ admin_delete: '管理员删除', user_closure: '用户注销' })[u.archivedReason] || u.archivedReason],
      ].map(([label, value]) => ({ label, value: value === undefined || value === null || value === '' ? '未记录' : value }))
    })
    return { fields }
  },
}
</script>
<style scoped>.account-summary{margin-bottom:20px}.account-summary__value{overflow-wrap:anywhere;white-space:pre-wrap}</style>
