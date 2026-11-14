<template>
  <div class="console-page script-page">
    <a-alert v-if="!canUse()" type="warning" show-icon message="当前身份无法管理脚本，请重新登录。" />
    <div v-else :key="frameKey" ref="pageSurface" :onInputCapture="pageEntry.guardNative" :onChangeCapture="pageEntry.guardNative" :onClickCapture="pageEntry.guardNative" :onKeydownCapture="pageEntry.guardNative">
    <!-- 标题按角色切换：普通用户菜单里这个入口叫「我的脚本」（123 号侧边栏分区），
         页面却写「脚本管理」会让用户以为进错了地方。与 Tickets（工单管理/工单反馈）、
         BillingRecordList（账单明细/我的账单）保持同一口径。 -->
    <ConsolePageHeading :title="isAdmin ? '脚本管理' : '我的脚本'" description="为账号生成短指令；管理员负责上传 EXE，用户可维护自己的脚本参数。">
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

    <!-- 诉求 ②：这行绑定提示只属于用户侧「我的脚本」。
         管理员视图（script-admin）是运维入口，提示他去绑手机/微信没有意义 ——
         服务端也按同一判据放行（CreateScript 的 SkipOwnerBindingCheck）。 -->
    <a-alert
      v-if="!isAdmin"
      type="info"
      show-icon
      class="page-alert"
      message="创建脚本前须绑定手机号和微信"
      description="冻结金模式可重复启动，每次使用新的运行令牌；一次性激活码只允许创建并运行一条脚本，不冻结余额。购买和兑换激活码不要求双绑定。"
    />

    <!-- 诉求 ③：管理员的动作是「上传新版本 EXE」，不是「新建脚本」。
         这个面板就是管理视图的主操作，标题与提示都按这个语义写死。 -->
    <ConsolePanel v-if="isAdmin" title="管理员上传 EXE" description="上传后自动发布为最新版本；已有脚本会原子切换到新版本，正在下载的旧版本会继续完成。">
      <a-space wrap>
        <a-input :id="fieldId('uploadVersion')" :value="uploadVersion" :onUpdate:value="pageEntry.bind(setUploadVersion)" aria-label="EXE 版本号" placeholder="版本号，例如 1.0.0" style="width: 180px" :disabled="uploading" />
        <a-upload :show-upload-list="false" accept=".exe,application/vnd.microsoft.portable-executable" :custom-request="pageEntry.bind(handleUpload)" :disabled="uploadGate.disabled">
          <a-button type="primary" :loading="uploading" :disabled="uploadGate.disabled">
            <UploadOutlined />上传 EXE
          </a-button>
        </a-upload>
        <span v-if="lastUploadKey" class="upload-result">最近上传版本：<code>{{ lastUploadKey }}</code></span>
      </a-space>
      <!-- 诉求 ①：去掉「确认该原生 C 版本已验证新版租约」勾选框后，版本号是唯一必填项。
           缺什么必须写清（不是「请填写完整」）；这条提示同时充当 disabled 的原因说明。 -->
      <p class="hint" role="status" aria-live="polite">
        <strong>必填：版本号</strong>（最多 64 个字符，不能含斜杠）。它决定新版本在版本列表里的标识，上传后所有未删除脚本会原子切换到该版本。
      </p>
      <p v-if="uploadGate.message" class="hint upload-gate" role="status" aria-live="polite">{{ uploadGate.message }}</p>
      <p class="hint">{{ scriptAdminAction.hint }}</p>
    </ConsolePanel>

    <!-- 诉求 ④：域名语法重写为 `*.a.com`（泛域名）/ `a.com/*`（路径通配），旧格式全部舍弃。
         `AllowWildcard` 开关被删除：泛域名是否可用由「列表里有没有 *.a.com」直接表达，
         两个开关表达同一件事必然出现互相矛盾的状态。 -->
    <ConsolePanel v-if="isAdmin" title="脚本可用域名" description="用户只能从这里的格式中选择并填写占位内容。每行一条，只支持两种写法：*.a.com（泛域名，匹配任意子域）或 a.com/*（路径通配，匹配任意非空路径）。留空表示不限制域名。">
      <a-textarea :id="fieldId('domains')" :value="domainText" :onUpdate:value="pageEntry.bind(setDomainText)" aria-label="脚本允许域名格式" :rows="4" placeholder="每行一条，例如：&#10;*.a.com&#10;a.com/*" :disabled="domainSaving || !domainReady" />
      <p class="hint">
        <strong>*.a.com</strong> 匹配 a.com 的任意子域（x.a.com、x.y.a.com），<strong>不含 a.com 本身</strong>；
        <strong>a.com/*</strong> 匹配 a.com 下的任意非空路径（a.com/p、a.com/p/q），<strong>不含根路径</strong>。
        旧写法（裸域名 a.com、a.com/a/*、a.com/**）已不再支持，保存时会直接报错。
      </p>
      <a-alert v-if="domainError" type="error" show-icon :message="domainError" class="page-alert" role="alert" />
      <a-space wrap class="domain-settings-actions">
        <a-button type="primary" :loading="domainSaving" :disabled="!domainReady || !domainDirty" :onClick="pageEntry.bind(saveDomainSettings)">保存域名设置</a-button>
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
          <!-- 诉求 ⑤：删除了「泛域名模式 / 优先使用尽可能短的 a.example.com」开关。
               域名格式由管理员在「脚本可用域名」里配置，用户只能选择并在占位框里填写。
               一个脚本可以有多个域名格式 ⇒ 这里是一组可增删的行。 -->
          <a-form-item label="脚本域名" required>
            <div v-if="!domainFormatOptions.length" class="hint">
              管理员尚未配置任何域名格式，暂时无法创建脚本。请联系管理员在「脚本可用域名」中设置。
            </div>
            <div v-else class="domain-rows">
              <div v-for="(row, index) in form.domainRows" :key="index" class="domain-row">
                <a-select :value="row.format" :onUpdate:value="editorEntry.bind(setDomainRowFormat, index)" :disabled="saving" style="width: 200px" :aria-label="`第 ${index + 1} 个域名格式`">
                  <a-select-option v-for="option in domainFormatOptions" :key="option.raw" :value="option.raw">{{ option.raw }}</a-select-option>
                </a-select>
                <!-- 占位输入框的位置由格式决定：*.a.com → [输入框].a.com；a.com/* → a.com/[输入框]。
                     两侧的字面量都直接渲染出来，用户看到的就是最终域名形状。 -->
                <template v-if="domainRowMeta(row).slot === 'prefix'">
                  <a-input :value="row.slot" :onUpdate:value="editorEntry.bind(setDomainRowSlot, index)" :disabled="saving" :maxlength="63" placeholder="子域" :aria-label="`第 ${index + 1} 个子域`" style="width: 160px" />
                  <code class="domain-suffix">.{{ domainRowMeta(row).host }}</code>
                </template>
                <template v-else>
                  <code class="domain-suffix">{{ domainRowMeta(row).host }}/</code>
                  <a-input :value="row.slot" :onUpdate:value="editorEntry.bind(setDomainRowSlot, index)" :disabled="saving" :maxlength="200" placeholder="路径，例如 p/q" :aria-label="`第 ${index + 1} 个路径`" style="width: 220px" />
                </template>
                <a-button type="link" danger :disabled="saving || form.domainRows.length <= 1" :onClick="editorEntry.bind(removeDomainRow, index)">删除</a-button>
              </div>
              <a-button type="dashed" :disabled="saving || form.domainRows.length >= domainFormatOptions.length" :onClick="editorEntry.bind(addDomainRow)">添加域名</a-button>
            </div>
            <p v-if="domainRowError" class="hint domain-error" role="alert">{{ domainRowError }}</p>
            <div class="hint">域名格式由管理员统一配置，你只能选择并填写占位内容。生成的域名：<code>{{ previewDomainText || '尚未填写' }}</code></div>
          </a-form-item>
          <a-form-item label="协议" :html-for="fieldId('scheme')">
            <a-select :id="fieldId('scheme')" :value="form.scheme" :disabled="form.billingMode !== 'legacy'" :onUpdate:value="editorEntry.bind(setFormField, 'scheme')"><a-select-option value="https">HTTPS</a-select-option><a-select-option v-if="form.billingMode === 'legacy'" value="http">HTTP（仅兼容旧环境）</a-select-option></a-select>
          </a-form-item>
        </div>
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
import { createScript, deleteScript, getScripts, getScriptDomainFormats, getScriptDomainSettings, getScriptVersions, updateScript, updateScriptDomainSettings, uploadScriptVersion, getScriptEscrowPolicy, updateScriptEscrowPolicy, getScriptActivationCodePrice, updateScriptActivationCodePrice, purchaseScriptActivationCode, redeemScriptActivationCode } from '@/api/TikuComponentApi'
import { buildScriptBillingText, buildScriptCommand, buildScriptDownloadURL, buildScriptListParams, buildScriptSavePayload, formatPresetParams, normalizeScriptRecord, normalizeScriptRows } from '@/utils/scriptManagementHelpers.mjs'
import { buildScriptDomainForms, parseScriptDomainFormat, splitScriptDomainForFormat } from '@/utils/scriptDomainFormats.mjs'
import { describeScriptAdminAction, describeScriptUploadGate } from '@/utils/scriptUploadGate.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

defineOptions({ name: 'ScriptManagement' })

const route = useRoute()

// ── 为什么不能用 route.path === mountedPath 做授权判据（2026-11-14 实测缺陷）──────
//
// 本页被 **两条路由共用同一个组件**：
//     scripts        (meta.viewMode='self')  → 普通用户菜单「我的脚本」
//     script-admin   (meta.viewMode='admin') → 管理员菜单「脚本管理」
// 两条路由指向同一个 component，所以 Vue 在两者之间切换时**复用同一个实例、不会重跑 setup**
// （实测 uid 不变：3 → 3）。于是 setup 期间固化的 `mountedPath` 永远停在挂载时那条路径上，
// 换到另一条后 `route.path === mountedPath` 恒为 false ⇒ canUse() 恒 false ⇒ 顶部出现
// 「当前身份无法管理脚本，请重新登录。」并且所有操作被静默拦掉。用户看到的是"权限没了"，
// 实际是判据把自己判成了外人。
//
// 正确判据是「**当前路由是否仍是本页自己的路由之一**」，而不是「路径有没有变」：
// 路径相等只能表达"还停在同一处"，而本页合法地拥有两个地址。用 route.name 白名单表达
// 「属于本页」既覆盖两条路由，又不会因为换 URL 而误判；路由改名时这里会跟着名字一起改，
// 不会像 mountedPath 那样悄悄失效。
//
// 若同一个组件被复制出第三条路由，必须把它的 name 也加进这个集合 —— 否则那条路由会
// 重新踩同一个坑。sidebarSectionSource 那类"拆路由引入的坑"都源于此：拆路由时只想着
// 侧边栏能单独选中，忘了组件内任何"挂载时快照"都会变成一次性快照。
const OWN_ROUTE_NAMES = Object.freeze(['scripts', 'script-admin'])

// 用 route.name 判「是否仍属本页」。route.name 为空（未命名路由/初次解析中）时
// 退回 matched 比对：当前匹配链里是否还挂着本页组件的路由记录。
const stillOwnRoute = () => {
  const name = String(route?.name || '')
  if (name) return OWN_ROUTE_NAMES.includes(name)
  const matched = route?.matched || []
  return matched.some(record => OWN_ROUTE_NAMES.includes(String(record?.name || '')))
}
const { currentUser } = useCurrentUserDisplay()
const fieldPrefix = 'script-' + useId(), fieldId = field => fieldPrefix + '-' + field
const pageSurface = ref(null), modalSurface = ref(null), frameKey = ref(0), editorKey = ref(0), draining = ref(false)
const deleting = ref(false), listReady = ref(false), readError = ref('')
const activeCloseConfirmation = shallowRef(null)
let alive = true, mounted = false, epoch = 0, readTicket = 0, domainRevision = 0, retry = null
const principal = () => {
  const user = currentUser.value, id = Number(user?.id)
  return Number.isSafeInteger(id) && id > 0 && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    ? JSON.stringify([id, String(user.role || '').trim().toLowerCase(), user.status ?? '']) : ''
}
let observedPrincipal = principal(), observedLogout = getCurrentUserLogoutEpoch(), authorizedLogout = observedLogout
const canUse = () => alive && stillOwnRoute() && authorizedLogout === getCurrentUserLogoutEpoch() && Boolean(principal())
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
// 视图模式由**入口**决定，不是只由角色决定（2026-11-14 用户实测报出的同类缺陷）。
//
// 管理员侧边栏同时有「我的脚本」（普通用户可见区）与「脚本管理」（仅管理员可见区）。
// 原来只判角色 ⇒ 管理员点「我的脚本」看到的仍是管理视图（含上传 EXE、计费政策），
// 而「脚本管理」那个入口因共用同一 name 也无法被单独选中。
//
// 现在：`scripts` → meta.viewMode='self'，`script-admin` → 'admin'。
// 权限边界不在前端 —— `script-admin` 已登记进 router 的 ADMIN_ONLY_ROUTE_NAMES，
// 服务端仍按角色校验上传与计费政策写入。
const routeViewMode = computed(() => {
  const meta = route?.meta || {}
  return typeof meta.viewMode === 'string' ? meta.viewMode : ''
})
const isAdmin = computed(() => {
  if (routeViewMode.value === 'self') return false
  if (routeViewMode.value === 'admin') return true
  return isAdminUser(currentUser.value)
})
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
const domainReady = ref(false)
const domainSaving = ref(false)
// 诉求 ④：`domainAllowWildcard` 已删除（泛域名可用性由列表内容表达）。
const domainError = ref('')
// 保存按钮的 dirty 判据：只有内容真的变了才允许保存，避免「点了一下但什么都没改」的无效往返。
const domainSavedText = ref('')
const domainDirty = computed(() => domainText.value.trim() !== domainSavedText.value.trim())
// 诉求 ⑤：用户可选的域名格式模板（管理员配置），以及编辑中的域名行。
const domainFormats = ref([])
const domainRowError = ref('')
const scriptAdminAction = describeScriptAdminAction()
const escrowPolicy = ref(null), activationPrice = ref(null), policyError = ref('')
const policyForm = reactive({ target: '', minimum: '', multiplier: '' }), priceForm = ref('')
const savingPolicy = ref(false), savingPrice = ref(false)
let billingReadTicket = 0, policyRevision = 0, priceRevision = 0
const activationCode = ref(''), purchasedCode = ref(false), purchaseUncertain = ref(false)
const purchasingCode = ref(false), redeemingCode = ref(false)
const activationBusy = computed(() => purchasingCode.value || redeemingCode.value)
// 诉求 ①：`nativeLeaseConfirmed` 已删除 —— 那个确认框是「上传按钮无效」的根因之一
// （按钮 disabled 依赖一个用户不知道要勾的框）。现在 gate 只看真正的必填项。
const uploadGate = computed(() => describeScriptUploadGate({ uploading: uploading.value, version: uploadVersion.value }))
const currentNativeVersion = computed(() => versions.value.find((item) => item.nativeLeaseProtocol === true && item.isCurrent === true))
// 诉求 ⑤：用户可选的格式来自管理员配置，且只保留能解析的新语法条目。
const domainFormatOptions = computed(() => domainFormats.value.filter(Boolean))
function domainRowMeta(row) {
  return parseScriptDomainFormat(row?.format) || { slot: '', host: '' }
}
// 表单里的域名行 → 最终域名列表（多格式并存，诉求 ⑤ 最后一句）。
// ⚠️ 这两个 computed 只依赖 form，但 form 在下面才声明；它们本身是惰性的（不被求值就不读 form），
// 所以可以放在前面。真正**不能**放在前面的是下面那个 immediate watch —— 它会在 setup 期间
// 立即求值 domainFormsResult，从而在 form 初始化前访问它（TDZ：Cannot access 'form' before initialization）。
const domainFormsResult = computed(() => buildScriptDomainForms(form.domainRows))
const previewDomainText = computed(() => domainFormsResult.value.domains.join('  '))
// 「域名格式」现在是脚本的**唯一**域名来源：baseDomain 由第一行生成，
// 供既有的下载 URL 拼装继续工作（它仍按 <shortCode>.<baseDomain> 生成）。
let purchaseIntentKey = '', purchaseIntentQuote = ''
const canBuyActivationCode = computed(() => modalOpen.value && !editing.value && form.billingMode === 'activation_code'
  && !saving.value && !activationBusy.value && !purchasedCode.value && !form.entitlementId
  && (purchaseUncertain.value || activationPrice.value?.enabled === true))

const emptyForm = () => ({ id: undefined, name: '', description: '', versionId: undefined, slug: 'a', scheme: 'https', baseDomain: '', enabled: true, presetText: '', billingMode: 'escrow', entitlementId: undefined, autoTopUp: false, runDurationMinutes: 150, notifyOnDenied: false, domainRows: [{ format: '', slot: '' }] })
const form = reactive(emptyForm())
// 域名行 → baseDomain 的同步。必须放在 `form` 声明**之后**：
// 配了 immediate:true 的 watch 会在 setup 期间立即求值 domainFormsResult，
// 进而读取 form.domainRows；放在 form 之前会抛 TDZ
// （Cannot access 'form' before initialization，实测于本轮）。
watch(domainFormsResult, (result) => {
  domainRowError.value = result.errors.length ? result.errors[0].message : ''
  const first = result.domains[0] || ''
  // 只取主机部分：`x.a.com` → `a.com`，`a.com/p` → `a.com`。
  const host = first.includes('/') ? first.slice(0, first.indexOf('/')) : first
  if (host) form.baseDomain = host
}, { immediate: true })
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
    // 编辑态回填：把库里的具体域名反解成「格式 + 占位内容」（诉求 ⑤ 的逆运算）。
    fields.domainRows = restoreDomainRows(normalized.baseDomain)
  } else {
    fields.domainRows = defaultDomainRows()
  }
  Object.assign(form, fields)
  draft.savedConfigSnapshot.value = draft.currentConfigSnapshot.value
}

// 新建时的默认行：取第一个可用格式（管理员一般先配 `*.a.com`）。
function defaultDomainRows() {
  const first = domainFormatOptions.value[0]
  return [{ format: first?.raw || '', slot: '' }]
}

// 把一条具体域名反解成能匹配的「格式 + 槽位」。多条格式时优先取能解析的。
function restoreDomainRows(domain) {
  const value = String(domain ?? '').trim()
  if (!value) return defaultDomainRows()
  for (const option of domainFormatOptions.value) {
    const slot = splitScriptDomainForFormat(option, value)
    if (slot) return [{ format: option.raw, slot }]
  }
  // 反解不出来（存量域名不符合当前格式）时退回默认行，让用户重新选择 ——
  // 不能把不匹配的值硬塞进某个槽位，那会生成一个没人预期的域名。
  return defaultDomainRows()
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
function setDomainText(value) { if (isAdmin.value && !domainSaving.value) { domainRevision++; domainText.value = value } }
// 诉求 ⑤：域名行的增删改。一个脚本可以有多个域名格式，所以这里是列表操作。
function setDomainRowFormat(index, format) {
  if (!editorEntry.value.isCurrent() || saving.value) return
  const row = form.domainRows[index]
  if (!row) return
  row.format = format
  // 换格式后旧槽位可能不再合法（子域 vs 路径），清空让用户重填，
  // 而不是把 `p/q` 留在子域槽里再默默生成非法域名。
  row.slot = ''
}
function setDomainRowSlot(index, value) {
  if (!editorEntry.value.isCurrent() || saving.value) return
  const row = form.domainRows[index]
  if (row) row.slot = value
}
function addDomainRow() {
  if (!editorEntry.value.isCurrent() || saving.value) return
  if (form.domainRows.length >= domainFormatOptions.value.length) return
  const used = new Set(form.domainRows.map((row) => row.format))
  const next = domainFormatOptions.value.find((option) => !used.has(option.raw))
  form.domainRows.push({ format: next?.raw || '', slot: '' })
}
function removeDomainRow(index) {
  if (!editorEntry.value.isCurrent() || saving.value) return
  if (form.domainRows.length <= 1) return
  form.domainRows.splice(index, 1)
}

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
  // 传给 requestRouteLeave 的只是「是否登录页」的判据（它内部只比 to.path === '/login'）。
  // 这里用活的 route.path 而不是挂载快照：本页有两个地址，快照会指向另一条路由。
  if (await draft.requestRouteLeave({ path: route.path }) && owns()) retireEditor()
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
  // 诉求 ①：「确认该原生 C 版本已验证新版租约」勾选框已删除，这里不再有任何
  // nativeLeaseConfirmed 前置判断。上传仍按原生租约协议发布（nativeLeaseProtocol=true），
  // 因为本仓只发布原生 C EXE —— 那个事实由**服务端与版本列表**表达，不需要用户勾选确认。
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
    // 诉求 ③：上传 ≠ 新建脚本。旧实现在这里 `openCreate()`，把两件事混成一个，
    // 管理员以为「上传完就多了一条脚本」。现在只刷新版本列表，不打开任何弹窗。
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
  // 诉求 ⑤：域名只能由「管理员格式 + 用户占位内容」生成。
  // 逐行校验，错误要指出**是哪一行缺什么**，不能只说「请输入有效域名」。
  const domainForms = buildScriptDomainForms(form.domainRows)
  if (domainForms.errors.length) {
    const first = domainForms.errors[0]
    return message.warning(`第 ${first.index + 1} 个域名：${first.message}`)
  }
  if (!domainForms.domains.length) return message.warning('请至少填写一个脚本域名')
  // baseDomain 仍按「主机」提交（后端按主机校验管理员格式），
  // 完整域名列表另存为 domains 供展示与后续扩展。
  const primaryHost = domainForms.domains[0].includes('/')
    ? domainForms.domains[0].slice(0, domainForms.domains[0].indexOf('/'))
    : domainForms.domains[0]
  payload.baseDomain = primaryHost
  payload.domains = domainForms.domains
  if (!payload.baseDomain || !buildScriptDownloadURL(payload)) return message.warning('请填写有效的脚本域名')
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
    domainSavedText.value = domainText.value
    domainReady.value = true
  } catch {
    if (owns() && ticket === domainRevision) message.warning('域名设置未读取成功，请刷新页面。')
  }
}

// 诉求 ⑤：用户侧的格式来源。管理员与用户都要读它 —— 管理员用它渲染占位预览，
// 用户用它作为唯一可选项。失败时不阻塞页面，只在创建脚本时提示无可用格式。
async function loadDomainFormats() {
  if (!canAct()) return
  const owns = captureOwner()
  try {
    const response = await getScriptDomainFormats()
    if (!owns()) return
    if (response?.status !== 200) { domainFormats.value = []; return }
    const list = Array.isArray(response.data?.formats) ? response.data.formats : []
    // 只保留能解析的新语法条目：服务端已过滤，这里再挡一次是为了让
    // 「界面给了一个填不出域名的选项」这种不可用状态不可能出现。
    domainFormats.value = list
      .map((item) => parseScriptDomainFormat(item?.raw ?? item?.pattern ?? ''))
      .filter(Boolean)
  } catch {
    if (owns()) domainFormats.value = []
  }
}

async function saveDomainSettings() {
  if (!canAct() || !isAdmin.value || !domainReady.value || domainSaving.value) return
  const owns = captureOwner(), ticket = ++domainRevision
  domainSaving.value = true
  domainError.value = ''
  try {
    const domains = domainText.value.split(/\r?\n/).map(value => value.trim()).filter(Boolean)
    const response = await updateScriptDomainSettings({ domains })
    if (!owns() || ticket !== domainRevision) return
    if (response?.status !== 200) {
      // 诉求 ④：旧格式必须**明确报错**，让管理员知道要改成新写法，
      // 而不是「保存成功但规则不生效」。
      const detail = response?.data?.message || '保存域名设置未完成，请刷新核对。'
      domainError.value = detail
      message.error(detail)
      return
    }
    domainText.value = Array.isArray(response.data?.domains) ? response.data.domains.join('\n') : domainText.value
    domainSavedText.value = domainText.value
    // 保存后立刻刷新用户侧可选项，避免管理员改完而用户仍看到旧列表。
    void loadDomainFormats()
    message.success('脚本域名设置已保存')
  } catch (error) {
    if (owns() && ticket === domainRevision) {
      const detail = error?.response?.data?.message || '域名设置保存结果未确认，请刷新核对。'
      domainError.value = detail
      message.error(detail)
    }
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
  void loadVersions(); void loadDomainSettings(); void loadDomainFormats(); void loadBillingSettings(); void loadScripts()
}
function retireScope() {
  const ticket = ++epoch
  readTicket += 1; domainRevision += 1; billingReadTicket += 1; policyRevision += 1; priceRevision += 1; retry = null; draining.value = true
  hidePrivate(pageSurface.value); retireEditor(); cancelActiveScriptClose()
  showAll.value = false; rows.value = []; versions.value = []
  Object.assign(summary, { total: 0, enabled: 0, reserved: 0 })
  readError.value = ''; listReady.value = false; loading.value = false; deleting.value = false
  domainText.value = ''; domainReady.value = false; domainSaving.value = false; domainError.value = ''
  domainFormats.value = []; domainRowError.value = ''
  lastUploadKey.value = ''; uploadVersion.value = ''; uploading.value = false
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