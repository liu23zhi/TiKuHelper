<template>
  <main class="agreement-support">
    <h1>协议与公开联系方式</h1>
    <p>不需要登录，也不需要同意协议，即可查看本页。请勿向任何渠道发送密码、验证码或访问凭据。</p>
    <section aria-labelledby="agreement-support-channels">
      <h2 id="agreement-support-channels">公开联系方式</h2>
      <p v-if="channels === null" role="status">{{ contactMessage }}</p>
      <p v-else-if="!channels.length">管理员尚未配置公开联系方式。</p>
      <dl v-else>
        <template v-for="row in channels" :key="row.kind">
          <dt>{{ row.label }}</dt><dd>{{ row.value }}</dd>
        </template>
      </dl>
      <p>已登录用户可在 <RouterLink to="/tickets">TiKuHelper 工单反馈</RouterLink> 提交问题。</p>
    </section>
    <section aria-labelledby="agreement-support-release">
      <h2 id="agreement-support-release">当前已发布协议</h2>
      <p v-if="!release" role="status">{{ releaseMessage }}</p>
      <details v-else>
        <summary>{{ release.summary || '阅读正式协议' }}（{{ release.version }}）</summary>
        <pre>{{ release.body }}</pre>
      </details>
    </section>
    <RouterLink to="/login">返回登录页</RouterLink>
  </main>
</template>

<script setup>
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { readAgreementChannels } from '@/utils/agreementContacts.mjs'

defineOptions({ name: 'AgreementPublicSupport' })

const channels = ref(null), release = ref(null)
const contactMessage = ref('正在读取公开联系方式…')
const releaseMessage = ref('正在读取当前已发布协议…')
const abort = new AbortController()
const read = path => fetch(path, { method: 'GET', cache: 'no-store', credentials: 'omit', redirect: 'error', signal: abort.signal })

async function loadContacts() {
  try {
    const result = await read('/api/agreements/contacts')
    const data = result.ok ? readAgreementChannels(await result.json()) : null
    if (!data) throw new Error('invalid public contact catalog')
    if (!abort.signal.aborted) channels.value = data
  } catch {
    if (!abort.signal.aborted) contactMessage.value = '暂时无法读取公开联系方式，请稍后刷新。'
  }
}
async function loadRelease() {
  try {
    const result = await read('/api/agreements/current')
    if (abort.signal.aborted) return
    if (result.status === 404) {
      releaseMessage.value = '尚未发布正式协议。'
      return
    }
    if (!result.ok) throw new Error('published agreement unavailable')
    const data = await result.json()
    if (typeof data?.version !== 'string' || typeof data?.body !== 'string') throw new Error('invalid published agreement')
    if (!abort.signal.aborted) release.value = data
  } catch {
    if (!abort.signal.aborted) releaseMessage.value = '暂时无法读取当前协议，请稍后刷新。'
  }
}
onMounted(() => { void loadContacts(); void loadRelease() })
onBeforeUnmount(() => abort.abort())
</script>

<style scoped>
.agreement-support { max-width: 48rem; margin: 0 auto; padding: min(8vw, 3rem) 1.25rem; color: var(--console-text); }
.agreement-support section { margin: 1.75rem 0; }
.agreement-support dt { font-weight: 600; }
.agreement-support dd { margin: 0 0 .65rem; overflow-wrap: anywhere; }
.agreement-support pre { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; }
</style>
