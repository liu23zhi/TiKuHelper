<template>
  <div class="console-page script-page">
    <a-alert v-if="!canUse()" type="warning" show-icon message="当前身份无法管理脚本，请重新登录。" />
    <div v-else :key="frameKey" ref="pageSurface" :onInputCapture="pageEntry.guardNative" :onChangeCapture="pageEntry.guardNative" :onClickCapture="pageEntry.guardNative" :onKeydownCapture="pageEntry.guardNative">
    <ConsolePageHeading title="脚本管理" description="为账号生成短指令；管理员负责上传 EXE，用户可维护自己的脚本参数。">
      <template #actions>
        <a-space wrap class="section-actions">
          <a-button v-if="isAdmin" :type="showAll ? 'primary' : 'default'" :onClick="pageEntry.bind(toggleScope)">
            {{ showAll ? '只看我的脚本' : '显示全部脚本' }}
          </a-button>
          <a-button :loading="loading" :onClick="pageEntry.bind(refreshScripts)">刷新</a-button>
          <a-button type="primary" :onClick="pageEntry.bind(openCreate)">新建脚本指令</a-button>
        </a-space>
      </template>
    </ConsolePageHeading>

    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="创建脚本前须绑定手机号和微信"
      description="冻结金模式可重复启动，每次使用新的运行令牌；一次性激活码只允许创建并运行一条脚本，不冻结余额。购买和兑换激活码不要求双绑定。"
    />

    <ConsolePanel v-if="isAdmin" title="管理员上传 EXE" description="上传后自动发布为最新版本；已有脚本会原子切换到新版本，正在下载的旧版本会继续完成。">
      <a-space wrap>
        <a-input :id="fieldId('uploadVersion')" :value="uploadVersion" :onUpdate:value="pageEntry.bind(setUploadVersion)" aria-label="EXE 版本号" placeholder="版本号，例如 1.0.0" style="width: 180px" :disabled="uploading" />
        <a-checkbox :checked="nativeLeaseConfirmed" :onUpdate:checked="pageEntry.bind(setNativeLeaseConfirmed)" :disabled="uploading">确认该原生 C 版本已验证新版租约</a-checkbox>
        <a-upload :show-upload-list="false" accept=".exe,application/vnd.microsoft.portable-executable" :custom-request="pageEntry.bind(handleUpload)" :disabled="uploading || !nativeLeaseConfirmed">
          <a-button type="primary" :loading="uploading">
            <UploadOutlined />上传 EXE
          </a-button>
        </a-upload>
        <span v-if="lastUploadKey" class="upload-result">最近上传版本：<code>{{ lastUploadKey }}</code></span>
      </a-space>
      <p class="hint">仅发布已验证的原生 C EXE，不使用冻结的 Node 版本。上传不会创建脚本，也不会改动旧脚本的版本。</p>
    </ConsolePanel>

    <ConsolePanel v-if="isAdmin" title="脚本可用域名" description="管理员可以限制用户创建脚本时可选择的域名，并控制是否允许泛域名短地址。留空域名列表表示兼容旧配置，允许任意域名。">
      <a-textarea :id="fieldId('domains')" :value="domainText" :onUpdate:value="pageEntry.bind(setDomainText)" aria-label="脚本允许域名" :rows="3" placeholder="每行一个域名，例如 example.com\na.example.net" :disabled="domainSaving || !domainReady" />
      <a-space wrap class="domain-settings-actions">
        <a-switch :checked="domainAllowWildcard" :onUpdate:checked="pageEntry.bind(setDomainWildcard)" aria-label="允许泛域名模式" :disabled="domainSaving || !domainReady" />
        <span class="switch-description">允许泛域名模式（a.example.com）</span>
        <a-button type="primary" :loading="domainSaving" :disabled="!domainReady" :onClick="pageEntry.bind(saveDomainSettings)">保存域名设置</a-button>
      </a-space>
    </ConsolePanel>

    <ConsolePanel v-if="isAdmin" title="脚本计费政策" description="冻结金目标、重启最低额和倍率由管理员统一设置。">
      <a-alert v-if="policyError" type="error" show-icon :message="policyError" class="page-alert" />
      <a-form layout="vertical">
        <div class="form-grid">
          <a-form-item label="创建冻结额（元）" :html-for="fieldId('policyTarget')">
            <a-input-number :id="fieldId('policyTarget')" string-mode :value="policyForm.target" :onUpdate:value="pageEntry.bind(setPolicyField, 'target')" :min="0" :precision="6" :disabled="!escrowPolicy || savingPolicy" />
          </a-form-item>
          <a-form-item label="再次启动最低额（元）" :html-for="fieldId('policyMinimum')">
            <a-input-number :id="fieldId('policyMinimum')" string-mode :value="policyForm.minimum" :onUpdate:value="pageEntry.bind(setPolicyField, 'minimum')" :min="0" :precision="6" :disabled="!escrowPolicy || savingPolicy" />
          </a-form-item>
          <a-form-item label="冻结金脚本倍率（X）" :html-for="fieldId('policyMultiplier')">
            <a-input-number :id="fieldId('policyMultiplier')" string-mode :value="policyForm.multiplier" :onUpdate:value="pageEntry.bind(setPolicyField, 'multiplier')" :min="0" :precision="6" :disabled="!escrowPolicy || savingPolicy" />
          </a-form-item>
        </div>
        <a-button type="primary" :loading="savingPolicy" :disabled="!escrowPolicy" :onClick="pageEntry.bind(saveEscrowPolicy)">保存冻结金政策</a-button>
        <a-form-item label="一次性激活码售价（元）" :html-for="fieldId('activationPrice')" class="billing-panel">
          <a-input-number :id="fieldId('activationPrice')" string-mode :value="priceForm" :onUpdate:value="pageEntry.bind(setPriceForm)" :min="0" :precision="6" :disabled="!activationPrice || savingPrice" />
          <div class="hint">使用账户钱包购买；设置为 0 时停售，不影响已购或已兑换的资格。</div>
        </a-form-item>
        <a-button type="primary" :loading="savingPrice" :disabled="!activationPrice" :onClick="pageEntry.bind(saveActivationPrice)">保存激活码售价</a-button>
      </a-form>
    </ConsolePanel>

    <ConsoleSummaryGrid>
      <a-card :bordered="false" class="summary-card"><a-statistic title="脚本数" :value="summary.total" /></a-card>
      <a-card :bordered="false" class="summary-card"><a-statistic title="启用脚本" :value="summary.enabled" /></a-card>
      <a-card :bordered="false" class="summary-card"><a-statistic title="旧版预扣额" prefix="¥" :value="summary.reserved" :precision="6" /></a-card>
    </ConsoleSummaryGrid>

    <ConsolePanel title="脚本指令" :description="scopeDescription">
      <a-alert v-if="readError" type="error" show-icon :message="readError" class="page-alert" />
      <ConsoleTableShell label="脚本指令列表">
        <a-table bordered table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="false" row-key="id" :scroll="{ x: tableScrollX }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'name'">
              <div class="script-name-cell"><strong>{{ record.name }}</strong><a-tag :color="isEscrowClosing(record) ? 'orange' : record.enabled ? 'green' : 'default'">{{ isEscrowClosing(record) ? '待结算' : record.enabled ? '启用' : '停用' }}</a-tag></div>
              <p v-if="record.description" class="table-hint">{{ record.description }}</p>
            </template>
            <span v-else-if="column.key === 'owner'">{{ record.ownerName || '-' }}</span>
            <div v-else-if="column.key === 'command'" class="command-cell">
              <code>{{ buildScriptDownloadURL(record) || '尚未配置短地址' }}</code>
              <span v-if="record.scheme === 'http'" class="table-hint">授权指令仅支持 HTTPS</span>
              <a-button size="small" :onClick="pageEntry.bind(copyCommand, record)" :disabled="!mayCopyCommand(record)">复制授权指令</a-button>
            </div>
            <div v-else-if="column.key === 'billing'">
              <span>{{ scriptBillingText(record) }}</span>
              <p v-if="record.billingMode === 'escrow' && record.escrowAvailableMicros != null && displayMicros(record.escrowAvailableMicros) !== ''" class="table-hint">当前冻结余额 ¥{{ displayMicros(record.escrowAvailableMicros) }}</p>
            </div>
            <span v-else-if="column.key === 'preset'">{{ Object.keys(record.presetParams || {}).length }} 个</span>
            <span v-else-if="column.key === 'updatedAt'">{{ formatDateTime(record.updatedAt) }}</span>
            <a-space v-else-if="column.key === 'operation'" wrap>
              <a-button type="link" style="padding: 0" :disabled="isEscrowClosing(record)" :onClick="pageEntry.bind(openEdit, record)">编辑</a-button>
              <a-popconfirm title="删除此脚本？" :description="record.billingMode === 'escrow' ? '删除后停止受理新请求，已受理请求结算完成后返还剩余冻结金。' : '删除成功后无法继续启动；一次性资格不会重新发放。'" ok-text="删除" cancel-text="取消" :onConfirm="pageEntry.bind(handleDelete, record)">
                <a-button type="link" danger style="padding: 0" :disabled="deleting || saving || !!activeCloseConfirmation || isEscrowClosing(record)">删除</a-button>
              </a-popconfirm>
            </a-space>
            <template v-else>{{ record[column.dataIndex] ?? '-' }}</template>
          </template>
          <template #emptyText><a-empty :description="listReady ? '暂无脚本指令' : '脚本列表尚未读取成功'" /></template>
        </a-table>
      </ConsoleTableShell>
    </ConsolePanel>

    <a-modal v-if="modalOpen" :key="editorKey" :open="modalOpen" :title="editing ? '编辑脚本指令' : '新建脚本指令'" width="760px" ok-text="保存" cancel-text="取消" :confirm-loading="saving" :closable="!saving && !activationBusy" :mask-closable="!saving && !activationBusy" :keyboard="!saving && !activationBusy" :cancel-button-props="{ disabled: saving || activationBusy }" :onOk="editorEntry.bind(handleSubmit)" :onCancel="editorEntry.bind(closeModal)" :wrap-props="{ 'aria-modal': true }">
      <div ref="modalSurface" :onInputCapture="editorEntry.guardNative" :onChangeCapture="editorEntry.guardNative" :onClickCapture="editorEntry.guardNative" :onKeydownCapture="editorEntry.guardNative">
      <a-form layout="vertical" :model="form" :disabled="saving">
        <div class="form-grid">
          <a-form-item label="脚本名称" :html-for="fieldId('name')" required>
            <a-input :id="fieldId('name')" :value="form.name" :onUpdate:value="editorEntry.bind(setFormField, 'name')" :maxlength="80" placeholder="例如：超星答题助手" />
          </a-form-item>
          <a-form-item label="脚本域名" :html-for="fieldId('baseDomain')" required>
            <a-input :id="fieldId('baseDomain')" :value="form.baseDomain" :onUpdate:value="editorEntry.bind(setFormField, 'baseDomain')" placeholder="例如 example.com" />
          </a-form-item>
          <a-form-item label="协议" :html-for="fieldId('scheme')">
            <a-select :id="fieldId('scheme')" :value="form.scheme" :disabled="form.billingMode !== 'legacy'" :onUpdate:value="editorEntry.bind(setFormField, 'scheme')"><a-select-option value="https">HTTPS</a-select-option><a-select-option v-if="form.billingMode === 'legacy'" value="http">HTTP（仅兼容旧环境）</a-select-option></a-select>
          </a-form-item>
        </div>
        <a-form-item label="泛域名模式" :html-for="fieldId('useWildcardDomain')">
          <a-switch :id="fieldId('useWildcardDomain')" :checked="form.useWildcardDomain" :onUpdate:checked="editorEntry.bind(setFormField, 'useWildcardDomain')" />
          <span class="switch-description">{{ form.useWildcardDomain ? '优先使用尽可能短的 a.example.com' : '使用 a.example.com/a 兼容非泛域名部署' }}</span>
          <div class="hint">短标识由服务端按最短可用长度自动分配。</div>
        </a-form-item>
        <a-form-item label="EXE 版本" :html-for="fieldId('versionId')" required>
          <a-select :id="fieldId('versionId')" :value="form.versionId" :onUpdate:value="editorEntry.bind(setFormField, 'versionId')" allow-clear disabled :placeholder="editing ? '跟随当前发布版本' : '请先上传 EXE'">
            <a-select-option v-for="version in versions" :key="version.id" :value="version.id">
              {{ version.version }} · {{ version.filename || version.fileName || 'script.exe' }}
            </a-select-option>
          </a-select>
          <div class="hint">此版本由服务端维护。新建脚本使用当前发布版本，管理员上传新 EXE 后会自动切换所有未删除脚本。</div>
        </a-form-item>
        <a-form-item label="说明" :html-for="fieldId('description')"><a-textarea :id="fieldId('description')" :value="form.description" :onUpdate:value="editorEntry.bind(setFormField, 'description')" :rows="2" placeholder="可选，说明用途或适用客户端" /></a-form-item>
        <a-form-item label="预置参数" :html-for="fieldId('presetText')">
          <a-textarea :id="fieldId('presetText')" :value="form.presetText" :onUpdate:value="editorEntry.bind(setFormField, 'presetText')" :rows="4" placeholder="每行一个 KEY=VALUE，例如\nkey=your-api-key\ncourseId=123" />
          <div class="hint">参数由服务端在脚本执行入口注入；此处只保存键和值，不会写进 PowerShell 命令文本。</div>
        </a-form-item>
        <a-form-item label="启用状态" :html-for="fieldId('enabled')"><a-switch :id="fieldId('enabled')" :checked="form.enabled" :onUpdate:checked="editorEntry.bind(setFormField, 'enabled')" /></a-form-item>
        <ConsolePanel title="脚本计费与运行" description="创建后不能切换计费模式。新模式均要求 HTTPS。" class="billing-panel">
          <a-form-item label="计费模式" :html-for="fieldId('billingMode')">
            <a-select :id="fieldId('billingMode')" :value="form.billingMode" :disabled="editing || activationBusy" :onUpdate:value="editorEntry.bind(setFormField, 'billingMode')">
              <a-select-option value="escrow">冻结金模式</a-select-option>
              <a-select-option value="activation_code">一次性激活码</a-select-option>
              <a-select-option v-if="form.billingMode === 'legacy'" value="legacy">旧版脚本</a-select-option>
            </a-select>
          </a-form-item>
          <template v-if="form.billingMode === 'escrow'">
            <p v-if="escrowPolicy" class="hint">创建冻结 ¥{{ displayMicros(escrowPolicy.targetMicros) }}；再次启动最低 ¥{{ displayMicros(escrowPolicy.minimumMicros) }}；管理员倍率 {{ displayMicros(escrowPolicy.multiplierMicros) }}X。</p>
            <p v-else class="hint">冻结金政策未读取成功，请刷新后再创建。</p>
            <a-form-item label="再次启动时自动补足冻结金" :html-for="fieldId('autoTopUp')">
              <a-switch :id="fieldId('autoTopUp')" :checked="form.autoTopUp" :onUpdate:checked="editorEntry.bind(setFormField, 'autoTopUp')" />
            </a-form-item>
          </template>
          <template v-if="form.billingMode === 'activation_code'">
            <p class="hint">一条脚本只允许成功运行一次，答题倍率为 0；不冻结钱包余额。</p>
            <template v-if="!editing">
              <p v-if="form.entitlementId" role="status">已兑换一次创建资格。</p>
              <template v-else>
                <a-space wrap class="domain-settings-actions">
                  <a-popconfirm :title="purchaseUncertain ? '确认重试同一次购买？' : '确认从钱包购买一次性激活码？'" :description="purchaseUncertain ? '沿用同一个购买标识，不发起新购买。' : '将按显示价格扣除钱包余额。'" ok-text="确认" cancel-text="取消" :onConfirm="editorEntry.bind(buyActivationCode)">
                    <a-button :loading="purchasingCode" :disabled="!canBuyActivationCode">{{ purchaseUncertain ? '重试同一次购买' : activationPrice?.enabled ? '钱包购买 ¥' + activationPrice.price : '购买暂未开放' }}</a-button>
                  </a-popconfirm>
                </a-space>
                <a-form-item label="激活码" :html-for="fieldId('activationCode')" class="billing-panel">
                  <a-input-password :id="fieldId('activationCode')" :value="activationCode" :visibility-toggle="false" autocomplete="off" :maxlength="68" :disabled="activationBusy || Boolean(purchasedCode)" :onUpdate:value="editorEntry.bind(setActivationCode)" placeholder="输入激活码" />
                  <p v-if="purchasedCode" class="hint">本次购买的激活码已填入，可直接兑换；不会写入脚本参数。</p>
                </a-form-item>
                <a-button :loading="redeemingCode" :disabled="activationBusy || !activationCode" :onClick="editorEntry.bind(redeemActivationCode)">兑换创建资格</a-button>
              </template>
            </template>
          </template>
          <template v-if="form.billingMode !== 'legacy'">
            <a-form-item label="运行令牌有效期（分钟）" :html-for="fieldId('runDurationMinutes')" class="billing-panel">
              <a-input-number :id="fieldId('runDurationMinutes')" :value="form.runDurationMinutes" :onUpdate:value="editorEntry.bind(setFormField, 'runDurationMinutes')" :min="1" :max="150" :precision="0" />
            </a-form-item>
            <a-form-item label="启动被拒时在客户端提示" :html-for="fieldId('notifyOnDenied')">
              <a-switch :id="fieldId('notifyOnDenied')" :checked="form.notifyOnDenied" :onUpdate:checked="editorEntry.bind(setFormField, 'notifyOnDenied')" />
            </a-form-item>
          </template>
        </ConsolePanel>
        <ConsolePanel title="指令预览" description="PowerShell + .NET WebClient 可覆盖 Windows 7 至 Windows 11。" class="preview-panel">
          <div class="command-preview"><code>{{ buildScriptDownloadURL(previewRecord) || '设置合法域名后生成' }}</code></div>
          <p class="hint">此处仅预览地址，不分配短标识。保存后可从列表复制实际授权指令；请勿分享指令。</p>
        </ConsolePanel>
      </a-form>
      </div>
    </a-modal>
    <a-modal v-if="activeCloseConfirmation" :open="true" title="客户端正在运行，仍要关闭脚本？" ok-text="确认关闭" cancel-text="取消" :ok-button-props="{ danger: true }" :mask-closable="false" :onOk="pageEntry.bind(confirmActiveScriptClose)" :onCancel="pageEntry.bind(cancelActiveScriptClose)" :wrap-props="{ 'aria-modal': true }">
      <p>立即吊销令牌并通知客户端退出，等已受理请求结清后返还余额。</p>
    </a-modal>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { onBeforeRouteLeave, useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import { UploadOutlined } from '@ant-design/icons-vue'
import { getCurrentUserLogoutEpoch, getStoredCurrentUser, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { useUnsavedDraft } from '@/utils/useUnsavedDraft'
import { createScript, deleteScript, getScripts, getScriptDomainSettings, getScriptVersions, updateScript, updateScriptDomainSettings, uploadScriptVersion, getScriptEscrowPolicy, updateScriptEscrowPolicy, getScriptActivationCodePrice, updateScriptActivationCodePrice, purchaseScriptActivationCode, redeemScriptActivationCode } from '@/api/TikuComponentApi'
import { buildScriptBillingText, buildScriptCommand, buildScriptDownloadURL, buildScriptListParams, buildScriptSavePayload, formatPresetParams, normalizeScriptRecord, normalizeScriptRows } from '@/utils/scriptManagementHelpers.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

defineOptions({ name: 'ScriptManagement' })

const route = useRoute(), mountedPath = route.path
const { currentUser } = useCurrentUserDisplay()
const fieldPrefix = 'script-' + useId(), fieldId = field => fieldPrefix + '-' + field
const pageSurface = ref(null), modalSurface = ref(null), frameKey = ref(0), editorKey = ref(0), draining = ref(false)
const deleting = ref(false), listReady = ref(false), readError = ref('')
const activeCloseConfirmation = shallowRef(null)
const domainReady = ref(false)
let alive = true, mounted = false, epoch = 0, readTicket = 0, domainRevision = 0, retry = null
const principal = () => {
  const user = currentUser.value, id = Number(user?.id)
  return Number.isSafeInteger(id) && id > 0 && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    ? JSON.stringify([id, String(user.role || '').trim().toLowerCase(), user.status ?? '']) : ''
}
let observedPrincipal = principal(), observedLogout = getCurrentUserLogoutEpoch(), authorizedLogout = observedLogout
const canUse = () => alive && route.path === mountedPath && authorizedLogout === getCurrentUserLogoutEpoch() && Boolean(principal())
const canAct = () => canUse() && !draining.value
function captureOwner() {
  const ticket = epoch, identity = principal(), logout = getCurrentUserLogoutEpoch()
  return () => {
    getStoredCurrentUser()
    return canAct() && epoch === ticket && identity === principal() && logout === getCurrentUserLogoutEpoch()
  }
}
function makeEntry(extra = () => true) {
  const owns = captureOwner(), frame = frameKey.value
  const isCurrent = () => owns() && frame === frameKey.value && extra()
  return {
    isCurrent,
    guardNative(event) { if (!isCurrent()) { event.preventDefault(); event.stopImmediatePropagation() } },
    bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined,
  }
}
const pageEntry = computed(() => { frameKey.value; return makeEntry() })
const editorEntry = shallowRef(makeEntry(() => false))
function hidePrivate(element) {
  if (!(element instanceof HTMLElement)) return
  element.style.visibility = 'hidden'; element.setAttribute('aria-hidden', 'true')
  if (element.contains(document.activeElement)) document.activeElement.blur?.()
  element.querySelectorAll('input,textarea').forEach(node => { node.value = ''; node.defaultValue = ''; node.removeAttribute('value') })
  const texts = document.createTreeWalker(element, 4)
  while (texts.nextNode()) texts.currentNode.nodeValue = ''
}
const isAdmin = computed(() => isAdminUser(currentUser.value))
const showAll = ref(false)
const rows = ref([])
const loading = ref(false)
const uploading = ref(false)
const uploadVersion = ref('')
const versions = ref([])
const saving = ref(false)
const modalOpen = ref(false)
const editing = ref(false)
const lastUploadKey = ref('')
const summary = reactive({ total: 0, enabled: 0, reserved: 0 })
const domainText = ref('')
const domainAllowWildcard = ref(true)
const domainSaving = ref(false)
const escrowPolicy = ref(null), activationPrice = ref(null), policyError = ref('')
const policyForm = reactive({ target: '', minimum: '', multiplier: '' }), priceForm = ref('')
const savingPolicy = ref(false), savingPrice = ref(false)
let billingReadTicket = 0, policyRevision = 0, priceRevision = 0
const activationCode = ref(''), purchasedCode = ref(false), purchaseUncertain = ref(false)
const purchasingCode = ref(false), redeemingCode = ref(false)
const activationBusy = computed(() => purchasingCode.value || redeemingCode.value)
const nativeLeaseConfirmed = ref(false)
const currentNativeVersion = computed(() => versions.value.find((item) => item.nativeLeaseProtocol === true && item.isCurrent === true))
let purchaseIntentKey = '', purchaseIntentQuote = ''
const canBuyActivationCode = computed(() => modalOpen.value && !editing.value && form.billingMode === 'activation_code'
  && !saving.value && !activationBusy.value && !purchasedCode.value && !form.entitlementId
  && (purchaseUncertain.value || activationPrice.value?.enabled === true))

const emptyForm = () => ({ id: undefined, name: '', description: '', versionId: undefined, slug: 'a', scheme: 'https', baseDomain: '', useWildcardDomain: true, enabled: true, presetText: '', billingMode: 'escrow', entitlementId: undefined, autoTopUp: false, runDurationMinutes: 150, notifyOnDenied: false })
const form = reactive(emptyForm())
const draft = useUnsavedDraft(form, { focusTriggerAfterClose: false })
const previewRecord = computed(() => normalizeScriptRecord({ ...form, presetParams: form.presetText }))
const scopeDescription = computed(() => isAdmin.value && showAll.value ? '当前显示全部用户的脚本；删除和编辑仍由服务端校验权限。' : '当前只显示自己的脚本。')
const columns = computed(() => [
  { title: '脚本', key: 'name', dataIndex: 'name', width: 220 },
  ...(isAdmin.value && showAll.value ? [{ title: '用户', key: 'owner', dataIndex: 'ownerName', width: 150 }] : []),
  { title: '短指令', key: 'command', dataIndex: 'command', width: 430 },
  { title: '预置参数', key: 'preset', dataIndex: 'preset', width: 100 },
  { title: '答题计费', key: 'billing', dataIndex: 'billing', width: 180 },
  { title: '更新时间', key: 'updatedAt', dataIndex: 'updatedAt', width: 150 },
  { title: '操作', key: 'operation', dataIndex: 'operation', width: 130 },
])
const tableScrollX = computed(() => sumTableColumnWidths(columns.value))

function resetForm(record) {
  const fields = emptyForm()
  if (record) {
    const normalized = normalizeScriptRecord(record)
    for (const name of Object.keys(fields)) if (Object.hasOwn(normalized, name)) fields[name] = normalized[name]
    fields.presetText = formatPresetParams(normalized.presetParams)
    fields.billingMode = ['escrow', 'activation_code'].includes(normalized.billingMode) ? normalized.billingMode : 'legacy'
    if (!fields.runDurationMinutes) fields.runDurationMinutes = 150
  }
  Object.assign(form, fields)
  draft.savedConfigSnapshot.value = draft.currentConfigSnapshot.value
}

function setFormField(name, value) {
  if (!editorEntry.value.isCurrent() || saving.value || !Object.hasOwn(form, name) || name === 'entitlementId') return
  if (name === 'billingMode') {
    if (editing.value || activationBusy.value || !['escrow', 'activation_code'].includes(value)) return
    form.scheme = 'https'
  }
  if (name === 'scheme' && form.billingMode !== 'legacy') return
  form[name] = value
}
function setUploadVersion(value) { if (isAdmin.value && !uploading.value) uploadVersion.value = value }
function setNativeLeaseConfirmed(value) { if (isAdmin.value && !uploading.value) nativeLeaseConfirmed.value = value === true }
function setDomainText(value) { if (isAdmin.value && !domainSaving.value) { domainRevision++; domainText.value = value } }
function setDomainWildcard(value) { if (isAdmin.value && !domainSaving.value) { domainRevision++; domainAllowWildcard.value = value } }

function startEditor(record) {
  if (!canAct() || modalOpen.value || saving.value || deleting.value || activeCloseConfirmation.value || isEscrowClosing(record)) return
  editing.value = Boolean(record)
  resetForm(record)
  if (!record && currentNativeVersion.value?.id) form.versionId = currentNativeVersion.value.id
  draft.savedConfigSnapshot.value = draft.currentConfigSnapshot.value
  const key = ++editorKey.value
  editorEntry.value = makeEntry(() => modalOpen.value && key === editorKey.value)
  modalOpen.value = true
}
function openCreate() { startEditor() }
function openEdit(record) { if (rows.value.includes(record)) startEditor(record) }
function retireEditor() {
  hidePrivate(modalSurface.value)
  activationCode.value = ''; purchasedCode.value = false; purchaseUncertain.value = false; purchaseIntentKey = ''; purchaseIntentQuote = ''
  purchasingCode.value = false; redeemingCode.value = false
  editorKey.value += 1; editorEntry.value = makeEntry(() => false); modalOpen.value = false; saving.value = false; editing.value = false
  draft.cancelPendingLeave(); resetForm()
}
async function closeModal() {
  if (saving.value || activationBusy.value || !editorEntry.value.isCurrent()) return
  const owns = editorEntry.value.isCurrent
  if (await draft.requestRouteLeave({ path: mountedPath }) && owns()) retireEditor()
}

function extractSummary(response, dataRows) {
  const source = response?.data?.summary || {}
  summary.total = Number(source.total ?? dataRows.length) || 0
  summary.enabled = Number(source.enabled ?? dataRows.filter((row) => row.enabled).length) || 0
  summary.reserved = Number(source.reserved ?? source.reservedAmount ?? dataRows.reduce((sum, row) => sum + row.reservedFee, 0)) || 0
}

async function loadScripts() {
  if (!canAct()) return
  const owns = captureOwner(), ticket = ++readTicket
  const scope = buildScriptListParams(isAdmin.value, showAll.value)
  const current = () => owns() && ticket === readTicket
  loading.value = true; readError.value = ''
  try {
    const response = await getScripts(scope)
    if (!current()) return
    if (response?.status !== 200) { readError.value = '读取脚本列表失败，请重试。'; return }
    const normalized = normalizeScriptRows(response)
    rows.value = normalized
    extractSummary(response, normalized)
    listReady.value = true
  } catch {
    if (current()) readError.value = '读取脚本列表失败，请重试。'
  } finally {
    if (current()) loading.value = false
  }
}

function toggleScope() {
  if (!canAct() || !isAdmin.value || modalOpen.value || deleting.value || activeCloseConfirmation.value) return
  showAll.value = !showAll.value
  rows.value = []; listReady.value = false
  Object.assign(summary, { total: 0, enabled: 0, reserved: 0 })
  return loadScripts()
}

async function handleUpload({ file, onSuccess, onError }) {
  if (!canAct() || !isAdmin.value || uploading.value) return
  const owns = captureOwner()
  if (!String(file?.name || '').toLowerCase().endsWith('.exe')) {
    const error = new Error('只允许上传 .exe 文件')
    onError?.(error)
    message.error(error.message)
    return
  }
  if (!String(uploadVersion.value || '').trim()) {
    const error = new Error('请先填写 EXE 版本号')
    onError?.(error)
    message.warning(error.message)
    return
  }
  if (!nativeLeaseConfirmed.value) {
    onError?.(new Error('请先确认客户端租约兼容性'))
    return
  }
  nativeLeaseConfirmed.value = false
  uploading.value = true
  try {
    const response = await uploadScriptVersion(file, uploadVersion.value, true)
    if (!owns()) return
    if (response?.status !== 200) throw new Error('上传 EXE 失败，请刷新核对版本。')
    const data = response.data || {}
    const uploaded = { ...data, filename: data.filename || file.name }
    if (!uploaded.id) throw new Error('上传成功但服务端没有返回版本信息')
    versions.value = [uploaded, ...versions.value.filter((item) => item.id !== uploaded.id)]
    lastUploadKey.value = uploaded.version || uploaded.filename || ''
    if (!modalOpen.value && !saving.value) openCreate()
    onSuccess?.(data)
    message.success('EXE 上传成功，已发布为最新版本')
  } catch {
    if (owns()) {
      const error = new Error('上传 EXE 结果未确认，请刷新版本后再试。')
      onError?.(error); message.error(error.message)
    }
  } finally { if (owns()) uploading.value = false }
}

function secureIntentKey(prefix) {
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  return prefix + Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
}
function createRetryKey(payload) {
  const signature = JSON.stringify(payload)
  if (retry?.signature === signature) return retry.key
  const key = secureIntentKey('script-create-')
  retry = { signature, key }
  return key
}
function scriptErrorCode(error) {
  return String(error?.response?.data?.code || error?.data?.code || '')
}
function setActivationCode(value) {
  if (!editorEntry.value.isCurrent() || editing.value || activationBusy.value || purchasedCode.value || form.billingMode !== 'activation_code') return
  activationCode.value = String(value ?? '').slice(0, 68)
}
function showActivationPurchaseFailure(error) {
  const reason = scriptErrorCode(error)
  if (reason === 'SCRIPT_ACTIVATION_PRICE_CHANGED') {
    purchaseUncertain.value = false; purchaseIntentKey = ''; purchaseIntentQuote = ''
    activationPrice.value = null
    message.warning('激活码售价已变化，请确认最新售价后再购买。')
    void loadBillingSettings()
    return
  }
  purchaseUncertain.value = true
  message.error(reason === 'SCRIPT_ACTIVATION_BALANCE_INSUFFICIENT' ? '钱包余额不足。补足后可重试同一次购买。' : '购买结果未确认，请在此重试同一次购买。')
}
async function buyActivationCode() {
  const owns = editorEntry.value.isCurrent
  if (!owns() || !canBuyActivationCode.value) return
  purchasingCode.value = true
  try {
    if (!purchaseIntentKey) {
      purchaseIntentQuote = activationPrice.value?.price || ''
      if (!purchaseIntentQuote) return
      purchaseIntentKey = secureIntentKey('script-activation-purchase-')
    }
    const response = await purchaseScriptActivationCode(purchaseIntentKey, purchaseIntentQuote)
    if (!owns()) return
    const code = response?.data?.code
    if (response?.status !== 200 || typeof code !== 'string' || !/^sac_[a-f0-9]{64}$/.test(code) ||
      decimalMicros(response.data?.price) !== decimalMicros(purchaseIntentQuote)) {
      showActivationPurchaseFailure(response)
      return
    }
    activationCode.value = code; purchasedCode.value = true; purchaseUncertain.value = false; purchaseIntentKey = ''; purchaseIntentQuote = ''
    message.success('购买成功，请兑换创建资格。')
  } catch (error) {
    if (owns()) showActivationPurchaseFailure(error)
  } finally {
    if (owns()) purchasingCode.value = false
  }
}
async function redeemActivationCode() {
  const owns = editorEntry.value.isCurrent, code = activationCode.value.trim()
  if (!owns() || editing.value || form.billingMode !== 'activation_code' || saving.value || activationBusy.value || form.entitlementId) return
  if (!/^sac_[a-f0-9]{64}$/.test(code)) return message.warning('请输入有效激活码。')
  redeemingCode.value = true
  try {
    const response = await redeemScriptActivationCode({ code })
    if (!owns()) return
    const id = Number(response?.data?.id)
    if (response?.status !== 200 || !Number.isSafeInteger(id) || id <= 0 || response?.data?.scriptId) {
      message.error('未取得可用的创建资格，请确认激活码状态。'); return
    }
    form.entitlementId = id; activationCode.value = ''; purchasedCode.value = false
    message.success('已兑换一次脚本创建资格。')
  } catch {
    if (owns()) message.error('兑换未完成，请核对激活码或重试。')
  } finally {
    if (owns()) redeemingCode.value = false
  }
}
function scriptBillingText(record) {
  if (record.billingMode === 'escrow') return '冻结金模式'
  if (record.billingMode === 'activation_code') return '一次性激活码（0X）'
  return buildScriptBillingText(record)
}

async function handleSubmit() {
  const owns = editorEntry.value.isCurrent
  if (!owns() || saving.value || deleting.value || activationBusy.value) return
  const payload = buildScriptSavePayload({ ...form, presetParams: form.presetText })
  // Managed pricing is server-owned. Do not send legacy multiplier defaults
  // (also preserves historical pricing on edits to existing legacy scripts).
  for (const field of ['billingEnabled', 'billingUseTikuPrice', 'useTikuPrice', 'priceMultiplier', 'answerPriceMultiplier']) delete payload[field]
  if (!payload.name) return message.warning('请输入脚本名称')
  if (!payload.baseDomain || !buildScriptDownloadURL(payload)) return message.warning('请输入有效的脚本域名')
  if (!payload.versionId) return message.warning('请选择 EXE 版本')
  const isEditing = editing.value, sentSnapshot = draft.currentConfigSnapshot.value
  if (form.billingMode !== 'legacy') {
    const minutes = Number(form.runDurationMinutes)
    if (form.runDurationMinutes === null || !Number.isSafeInteger(minutes) || minutes < 1 || minutes > 150) return message.warning('令牌有效期须为 1 至 150 分钟的整数')
    payload.runDurationMinutes = minutes
    payload.notifyOnDenied = form.notifyOnDenied === true
    if (form.billingMode === 'escrow') {
      if (!isEditing && !escrowPolicy.value) return message.warning('请刷新并确认当前冻结金政策。')
      payload.autoTopUp = form.autoTopUp === true
    }
  }
  if (!isEditing) {
    payload.billingMode = form.billingMode
    if (form.billingMode === 'activation_code') {
      if (!form.entitlementId) return message.warning('请先兑换一次性脚本创建资格。')
      payload.entitlementId = form.entitlementId
    }
  }
  saving.value = true
  try {
    const response = isEditing ? await updateScript(payload) : await createScript(payload, createRetryKey(payload))
    if (!owns()) return
    if (response?.status !== 200) {
      message.error(scriptErrorCode(response) === 'SCRIPT_BINDINGS_REQUIRED'
        ? '创建脚本前请绑定手机号和微信。' : '保存脚本未完成，请刷新列表核对后重试。')
      return
    }
    if (!isEditing) retry = null
    draft.savedConfigSnapshot.value = sentSnapshot
    const sameDraft = draft.currentConfigSnapshot.value === sentSnapshot
    if (sameDraft) retireEditor()
    message.success(isEditing ? '脚本已更新' : '脚本已创建')
    await loadScripts()
  } catch (error) {
    if (owns()) message.error(scriptErrorCode(error) === 'SCRIPT_BINDINGS_REQUIRED'
      ? '创建脚本前请绑定手机号和微信。' : '保存结果未确认，请刷新核对；如重试，请保留原表单。')
  } finally { if (owns()) saving.value = false }
}

function isEscrowClosing(record) {
  return record?.billingMode === 'escrow' && Boolean(record.escrowClosingAt)
}

function showScriptCloseFailure(record, response, owns) {
  const failure = response?.response || response
  const code = scriptErrorCode(response)
  if (record.billingMode === 'escrow' && failure?.status === 409 && code === 'SCRIPT_ACTIVE_LEASE_CONFIRM_REQUIRED') {
    const sessionId = failure.data?.activeRunSessionId
    if (typeof sessionId === 'string' && /^[1-9][0-9]{0,18}$/.test(sessionId) && rows.value.includes(record)) {
      activeCloseConfirmation.value = { record, sessionId, owns }
      return
    }
    message.warning('客户端状态已变化，请刷新后重新确认删除。')
    return
  }
  message.error(code === 'SCRIPT_ESCROW_CLOSE_PENDING'
    ? '冻结金仍待结算，请刷新查看状态。' : '删除脚本结果未确认，请刷新列表核对。')
}

function handleDelete(record) {
  if (!canAct() || !rows.value.includes(record) || deleting.value || saving.value || modalOpen.value || activeCloseConfirmation.value || isEscrowClosing(record)) return
  return submitScriptClose(record, captureOwner())
}

function cancelActiveScriptClose() {
  activeCloseConfirmation.value = null
}

function confirmActiveScriptClose() {
  const confirmation = activeCloseConfirmation.value
  if (!confirmation || !confirmation.owns() || deleting.value) return
  cancelActiveScriptClose()
  if (!rows.value.includes(confirmation.record) || isEscrowClosing(confirmation.record)) {
    message.warning('脚本列表已变化，请重新确认删除。')
    return
  }
  return submitScriptClose(confirmation.record, confirmation.owns, confirmation.sessionId)
}

async function submitScriptClose(record, owns, confirmActiveSessionId) {
  deleting.value = true
  try {
    const response = await deleteScript({ id: record.id, ...(confirmActiveSessionId ? { confirmActiveSessionId } : {}) })
    if (!owns()) return
    const state = response?.data?.state
    if (record.billingMode === 'escrow' && [200, 202].includes(response?.status) && state === 'settling') {
      rows.value = rows.value.map(item => item.id === record.id
        ? { ...item, enabled: false, escrowClosingAt: item.escrowClosingAt || true }
        : item)
      message.info('脚本已停止接单，冻结金待结算。')
    } else if (response?.status === 200 && (record.billingMode !== 'escrow' || state === 'closed')) {
      message.success(record.billingMode === 'escrow' ? '脚本已关闭，结算完成。' : '脚本已删除')
    } else {
      showScriptCloseFailure(record, response, owns)
      return
    }
    await loadScripts()
  } catch (error) {
    if (owns()) showScriptCloseFailure(record, error, owns)
  } finally { if (owns()) deleting.value = false }
}

async function copyText(value) {
  if (!value || !canAct()) return
  const owns = captureOwner()
  try {
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(value)
    else {
      const input = document.createElement('textarea'), opener = document.activeElement
      input.value = value; input.style.position = 'fixed'; input.style.opacity = '0'
      input.setAttribute('aria-hidden', 'true'); input.tabIndex = -1
      document.body.appendChild(input)
      try {
        input.focus(); input.select()
        if (!document.execCommand('copy')) throw new Error('Clipboard copy unavailable')
      } finally {
        input.value = ''; input.remove()
        if (owns() && opener instanceof HTMLElement && opener.isConnected) opener.focus({ preventScroll: true })
      }
    }
    if (owns()) message.success('指令已复制')
  } catch {
    if (owns()) message.error('复制失败，请手动选择指令')
  }
}

function mayCopyCommand(record) {
  return String(record.ownerId ?? '') === String(currentUser.value?.id ?? '')
    && Boolean(currentUser.value?.id) && !isEscrowClosing(record) && record.scheme === 'https' && Boolean(buildScriptCommand(record))
}
function copyCommand(record) {
  if (rows.value.includes(record) && mayCopyCommand(record)) return copyText(buildScriptCommand(record))
}
function formatDateTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString('zh-CN', { hour12: false })
}

async function loadVersions() {
  if (!canAct()) return
  const owns = captureOwner()
  try {
    const response = await getScriptVersions()
    if (!owns()) return
    if (response?.status !== 200) { message.warning('EXE 版本未读取成功，请刷新页面。'); return }
    versions.value = Array.isArray(response.data) ? response.data : (response.data?.items || [])
    if (modalOpen.value && !editing.value && !form.versionId && currentNativeVersion.value?.id) {
      const unchanged = !draft.hasUnsavedChanges.value
      form.versionId = currentNativeVersion.value.id
      if (unchanged) draft.savedConfigSnapshot.value = draft.currentConfigSnapshot.value
    }
  } catch {
    if (owns()) message.warning('EXE 版本未读取成功，请刷新页面。')
  }
}

async function loadDomainSettings() {
  if (!canAct() || !isAdmin.value) return
  const owns = captureOwner(), ticket = domainRevision
  try {
    const response = await getScriptDomainSettings()
    if (!owns() || ticket !== domainRevision) return
    if (response?.status !== 200) { message.warning('域名设置未读取成功，请刷新页面。'); return }
    domainText.value = Array.isArray(response.data?.domains) ? response.data.domains.join('\n') : ''
    domainAllowWildcard.value = response.data?.allowWildcard !== false
    domainReady.value = true
  } catch {
    if (owns() && ticket === domainRevision) message.warning('域名设置未读取成功，请刷新页面。')
  }
}

async function saveDomainSettings() {
  if (!canAct() || !isAdmin.value || !domainReady.value || domainSaving.value) return
  const owns = captureOwner(), ticket = ++domainRevision
  domainSaving.value = true
  try {
    const domains = domainText.value.split(/\r?\n/).map(value => value.trim()).filter(Boolean)
    const response = await updateScriptDomainSettings({ domains, allowWildcard: domainAllowWildcard.value })
    if (!owns() || ticket !== domainRevision) return
    if (response?.status !== 200) { message.error('保存域名设置未完成，请刷新核对。'); return }
    domainText.value = Array.isArray(response.data?.domains) ? response.data.domains.join('\n') : domainText.value
    domainAllowWildcard.value = response.data?.allowWildcard !== false
    message.success('脚本域名设置已保存')
  } catch {
    if (owns() && ticket === domainRevision) message.error('域名设置保存结果未确认，请刷新核对。')
  } finally { if (owns() && ticket === domainRevision) domainSaving.value = false }
}

function decimalMicros(value) {
  const parts = /^(\d{1,12})(?:\.(\d{1,6}))?$/.exec(String(value ?? '').trim())
  if (!parts) return null
  const micros = Number(parts[1]) * 1000000 + Number((parts[2] ?? '').padEnd(6, '0'))
  return Number.isSafeInteger(micros) ? micros : null
}
function displayMicros(value) {
  const micros = Number(value)
  if (!Number.isSafeInteger(micros) || micros < 0) return ''
  const fraction = String(micros % 1000000).padStart(6, '0').replace(/0+$/, '')
  return String(Math.floor(micros / 1000000)) + (fraction ? '.' + fraction : '')
}
function normalizeEscrowPolicy(data) {
  const targetMicros = Number(data?.targetMicros), minimumMicros = Number(data?.minimumMicros), multiplierMicros = Number(data?.multiplierMicros)
  if (![targetMicros, minimumMicros, multiplierMicros].every(value => Number.isSafeInteger(value) && value > 0) || minimumMicros > targetMicros) return null
  return { targetMicros, minimumMicros, multiplierMicros }
}
function showEscrowPolicy(policy) {
  escrowPolicy.value = policy
  if (!policy) return
  Object.assign(policyForm, {
    target: displayMicros(policy.targetMicros), minimum: displayMicros(policy.minimumMicros), multiplier: displayMicros(policy.multiplierMicros),
  })
}
function normalizeActivationPrice(data) {
  const price = decimalMicros(data?.price)
  if (price === null || data?.enabled !== (price > 0)) return null
  return { enabled: price > 0, price: displayMicros(price) }
}
function showActivationPrice(price) {
  activationPrice.value = price
  if (price) priceForm.value = price.price
}
async function loadBillingSettings() {
  if (!canAct()) return
  const owns = captureOwner(), ticket = ++billingReadTicket, policyAt = policyRevision, priceAt = priceRevision
  const [policy, price] = await Promise.allSettled([getScriptEscrowPolicy(), getScriptActivationCodePrice()])
  if (!owns() || ticket !== billingReadTicket) return
  if (policyAt === policyRevision) showEscrowPolicy(policy.value?.status === 200 ? normalizeEscrowPolicy(policy.value.data) : null)
  if (priceAt === priceRevision) showActivationPrice(price.value?.status === 200 ? normalizeActivationPrice(price.value.data) : null)
  policyError.value = !escrowPolicy.value || !activationPrice.value ? '部分计费设置未读取成功，请刷新后重试。' : ''
}
function setPolicyField(name, value) {
  if (canAct() && isAdmin.value && escrowPolicy.value && !savingPolicy.value && Object.hasOwn(policyForm, name)) {
    policyRevision++; policyForm[name] = value
  }
}
function setPriceForm(value) {
  if (canAct() && isAdmin.value && activationPrice.value && !savingPrice.value) { priceRevision++; priceForm.value = value }
}
async function saveEscrowPolicy() {
  if (!canAct() || !isAdmin.value || !escrowPolicy.value || savingPolicy.value) return
  const payload = normalizeEscrowPolicy({
    targetMicros: decimalMicros(policyForm.target), minimumMicros: decimalMicros(policyForm.minimum), multiplierMicros: decimalMicros(policyForm.multiplier),
  })
  if (!payload) return message.warning('金额与倍率最多六位小数且必须大于零；最低额不能高于冻结目标。')
  const owns = captureOwner(), revision = ++policyRevision
  savingPolicy.value = true
  try {
    const response = await updateScriptEscrowPolicy(payload)
    if (!owns() || revision !== policyRevision) return
    if (response?.status !== 200) { message.error('保存结果未确认，请刷新核对。'); return }
    showEscrowPolicy(normalizeEscrowPolicy(response.data) || payload)
    message.success('冻结金政策已保存')
  } catch {
    if (owns() && revision === policyRevision) message.error('保存结果未确认，请刷新核对。')
  } finally { if (owns() && revision === policyRevision) savingPolicy.value = false }
}
async function saveActivationPrice() {
  if (!canAct() || !isAdmin.value || !activationPrice.value || savingPrice.value) return
  const micros = decimalMicros(priceForm.value)
  if (micros === null || micros >= 1000000000000000) return message.warning('请输入非负售价（最多六位小数）。')
  const owns = captureOwner(), revision = ++priceRevision
  savingPrice.value = true
  try {
    const response = await updateScriptActivationCodePrice({ price: displayMicros(micros) })
    if (!owns() || revision !== priceRevision) return
    const price = response?.status === 200 ? normalizeActivationPrice(response.data) : null
    if (!price) { message.error('保存结果未确认，请刷新核对。'); return }
    showActivationPrice(price)
    message.success('激活码售价已保存')
  } catch {
    if (owns() && revision === priceRevision) message.error('保存结果未确认，请刷新核对。')
  } finally { if (owns() && revision === priceRevision) savingPrice.value = false }
}
function refreshScripts() { void loadBillingSettings(); return loadScripts() }
function startReads() {
  if (!canAct()) return
  void loadVersions(); void loadDomainSettings(); void loadBillingSettings(); void loadScripts()
}
function retireScope() {
  const ticket = ++epoch
  readTicket += 1; domainRevision += 1; billingReadTicket += 1; policyRevision += 1; priceRevision += 1; retry = null; draining.value = true
  hidePrivate(pageSurface.value); retireEditor(); cancelActiveScriptClose()
  showAll.value = false; rows.value = []; versions.value = []
  Object.assign(summary, { total: 0, enabled: 0, reserved: 0 })
  readError.value = ''; listReady.value = false; loading.value = false; deleting.value = false
  domainText.value = ''; domainAllowWildcard.value = true; domainReady.value = false; domainSaving.value = false
  lastUploadKey.value = ''; uploadVersion.value = ''; uploading.value = false; nativeLeaseConfirmed.value = false
  escrowPolicy.value = null; activationPrice.value = null; policyError.value = ''; priceForm.value = ''
  Object.assign(policyForm, { target: '', minimum: '', multiplier: '' }); savingPolicy.value = false; savingPrice.value = false
  void nextTick().then(() => nextTick()).then(() => {
    if (!alive || epoch !== ticket) return
    frameKey.value += 1; draining.value = false
    if (mounted) startReads()
  })
}

watch(() => [principal(), getCurrentUserLogoutEpoch()], ([next, logout]) => {
  if (next === observedPrincipal && logout === observedLogout) return
  if (logout === observedLogout && next && next !== observedPrincipal) authorizedLogout = logout
  observedPrincipal = next; observedLogout = logout
  retireScope()
}, { flush: 'sync' })
watch(() => route.fullPath, retireScope, { flush: 'sync' })
onBeforeRouteLeave(to => to.path === '/login' || !modalOpen.value || (!saving.value && !activationBusy.value && draft.requestRouteLeave(to)))
onMounted(() => { mounted = true; startReads() })
onBeforeUnmount(() => { alive = false; mounted = false; retireScope() })
</script>

<style scoped>
.page-alert { margin-bottom: 20px; }
.hint, .table-hint { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.7; overflow-wrap: anywhere; }
.upload-result { max-width: 100%; overflow-wrap: anywhere; }
.domain-settings-actions { margin-top: 12px; }
.script-name-cell, .command-cell { display: flex; align-items: center; gap: 8px; min-width: 0; }
.script-name-cell { flex-wrap: wrap; }
.command-cell { align-items: flex-start; flex-direction: column; }
.command-cell code, .command-preview code { display: block; width: 100%; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--console-text); }
.form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 16px; }
.switch-description { margin-left: 10px; color: var(--console-muted); }
.billing-panel, .preview-panel { margin-top: 16px; }
.command-preview { display: flex; align-items: flex-start; gap: 12px; padding: 12px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-surface-muted, #f8faf9); }
@media (max-width: 700px) { .form-grid { grid-template-columns: 1fr; } .command-preview { flex-direction: column; } }
</style>
