<template>
  <div class="console-page script-page">
    <a-alert v-if="!canUse()" type="warning" show-icon message="当前身份无法管理脚本，请重新登录。" />
    <div v-else :key="frameKey" ref="pageSurface" :onInputCapture="pageEntry.guardNative" :onChangeCapture="pageEntry.guardNative" :onClickCapture="pageEntry.guardNative" :onKeydownCapture="pageEntry.guardNative">
    <!-- 标题按角色切换：普通用户菜单里这个入口叫「我的脚本」（123 号侧边栏分区），
         页面却写「脚本管理」会让用户以为进错了地方。与 Tickets（工单管理/工单反馈）、
         BillingRecordList（账单明细/我的账单）保持同一口径。 -->
    <ConsolePageHeading :title="isAdmin ? '脚本管理' : '我的脚本'" description="默认生成随机长命令，可自行设置短命令；每条脚本同时只允许一个客户端在线。">
      <template #actions>
        <a-space wrap class="section-actions">
          <a-button data-script-activation-purchases :onClick="pageEntry.bind(openPurchases)">购买激活码 / 购买记录</a-button>
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
      <ConsolePanel v-if="isAdmin" title="脚本可用域名" description="每行一条。长命令地址由服务端按配置生成。留空时使用当前站点的非根路径。">
      <a-textarea :id="fieldId('domains')" :value="domainText" :onUpdate:value="pageEntry.bind(setDomainText)" aria-label="脚本允许域名格式" :rows="4" placeholder="每行一条，例如：&#10;*.example.test&#10;example.test/*" :disabled="domainSaving || !domainReady" />
      <p class="hint">
        <strong>*.example.test</strong> 使用独立子域名，例如 <code>a.example.test/</code>；根路径按访问的主机名区分。
        <strong>example.test/*</strong> 使用同一主机下的非根路径，例如 <code>example.test/team</code>。
        未被脚本占用的主机名仍可访问管理界面与常规 API；已占用的独立脚本主机名只提供脚本入口。
      </p>
      <div class="denied-domain-field">
        <label :for="fieldId('deniedDomains')">禁止设置的域名</label>
        <a-textarea :id="fieldId('deniedDomains')" :value="deniedDomainText" :onUpdate:value="pageEntry.bind(setDeniedDomainText)" aria-label="禁止设置的域名" :rows="4" placeholder="每行一条，例如：&#10;api.example.test&#10;*.internal.example.test" :disabled="domainSaving || !domainReady" />
        <p class="hint">每行填写完整域名或最左通配域名，不含协议、端口或路径。<code>*.internal.example.test</code> 只匹配它的子域名。</p>
        <p class="hint">如需保留某个主机名供站点使用，可在此禁止脚本使用该域名，或通过部署配置 <code>siteHosts</code> 保留它的根地址。</p>
        <p class="hint"><code>localhost</code>、其子域名、<code>localhost.localdomain</code>、回环 IP 和未指定 IP 内置禁止用于脚本，仍可访问管理界面与常规 API。</p>
        <p class="hint">禁止规则仅限制脚本，优先于可用规则，两份列表可交叠。保存后对新命令和已有命令立即生效。</p>
      </div>
      <a-alert v-if="domainError" type="error" show-icon :message="domainError" class="page-alert" role="alert" />
      <a-space wrap class="domain-settings-actions">
        <a-button type="primary" :loading="domainSaving" :disabled="!domainReady || !domainDirty" :onClick="pageEntry.bind(saveDomainSettings)">保存域名设置</a-button>
      </a-space>
    </ConsolePanel>

    <ConsolePanel v-if="isAdmin" title="命令有效期设置" description="分别控制长、短命令是否允许永不过期。修改后限制新建或更新授权，已有授权保留原有效期。">
      <a-form layout="vertical">
        <div class="form-grid">
          <a-form-item label="允许长命令永不过期" :html-for="fieldId('fullForever')">
            <a-switch :id="fieldId('fullForever')" :checked="allowFullNeverExpire" :disabled="!domainReady || domainSaving" :onUpdate:checked="pageEntry.bind(setExpiryPolicy, 'full')" />
          </a-form-item>
          <a-form-item label="允许短命令永不过期" :html-for="fieldId('shortForever')">
            <a-switch :id="fieldId('shortForever')" :checked="allowShortNeverExpire" :disabled="!domainReady || domainSaving" :onUpdate:checked="pageEntry.bind(setExpiryPolicy, 'short')" />
          </a-form-item>
        </div>
        <a-button type="primary" :loading="domainSaving" :disabled="!domainReady || !domainDirty" :onClick="pageEntry.bind(saveDomainSettings)">保存授权期限设置</a-button>
      </a-form>
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
      <!-- 通知区（2026-11-17 用户需求）。
           「显示因为没有心跳被撤销令牌等信息」——只展示需要用户知道的撤销，
           正常到寿与用户自己关闭都不出现（那时候说什么都是噪音）。
           排序由 collectScriptRevokeNotices 保证：warning 永远在最前。 -->
      <a-alert
        v-for="notice in revokeNotices"
        :key="'revoke-' + notice.scriptId + '-' + notice.reason"
        :type="scriptNoticeAlertType(notice.level)"
        show-icon
        class="page-alert"
        :message="'脚本「' + notice.scriptName + '」：' + notice.message"
        :description="notice.hint"
      />
      <ConsoleTableShell label="脚本指令列表">
        <a-table bordered table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="false" row-key="id" :scroll="{ x: tableScrollX }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'name'">
              <div class="script-name-cell"><strong>{{ record.name }}</strong><a-tag :color="isEscrowClosing(record) ? 'orange' : record.enabled ? 'green' : 'default'">{{ isEscrowClosing(record) ? '待结算' : record.enabled ? '启用' : '停用' }}</a-tag></div>
              <p v-if="record.description" class="table-hint">{{ record.description }}</p>
            </template>
            <span v-else-if="column.key === 'owner'">{{ record.ownerName || '-' }}</span>
            <div v-else-if="column.key === 'command'" class="command-cell">
              <template v-if="isOwnScript(record)">
              <span v-if="isScriptRunConsumed(record)" class="table-hint">一次性资格已使用，仅当前客户端可继续本次运行</span>
              <span v-else class="table-hint">长命令</span>
              <code v-if="!isScriptRunConsumed(record)">{{ mayCopyCommand(record) ? buildScriptCommand(record) : buildScriptDownloadURL(record) || '暂无地址' }}</code>
              <span v-if="record.scheme === 'http'" class="table-hint">授权指令仅支持 HTTPS</span>
              <a-space wrap class="command-actions">
                <a-button size="small" :onClick="pageEntry.bind(copyCommand, record)" :disabled="!mayCopyCommand(record)">复制长命令</a-button>
                <a-button size="small" :onClick="pageEntry.bind(openShortAuthorization, record, 'full')" :disabled="isScriptRunConsumed(record) || isEscrowClosing(record) || record.scheme !== 'https'">长命令授权设置</a-button>
              </a-space>
              <template v-if="record.shortAuthorization?.address && buildScriptShortCommand(record)">
                <span class="table-hint">短命令</span>
                <code>{{ buildScriptShortCommand(record) }}</code>
              </template>
              <a-space wrap class="command-actions">
                <a-button size="small" :onClick="pageEntry.bind(openShortAuthorization, record, 'short')" :disabled="isScriptRunConsumed(record) || isEscrowClosing(record) || record.scheme !== 'https'">{{ record.shortAuthorization?.address ? '短命令授权设置' : '生成短命令' }}</a-button>
                <a-button v-if="record.shortAuthorization?.address" size="small" :disabled="isEscrowClosing(record) || !buildScriptShortCommand(record)" :onClick="pageEntry.bind(copyShortRecordCommand, record)">复制短命令</a-button>
              </a-space>
              <span v-if="record.launcherAuthorization" class="table-hint">长命令{{ shortAuthorizationStatus(record.launcherAuthorization.status) }} · {{ authorizationUsesText(record.launcherAuthorization) }}<template v-if="record.launcherAuthorization.expiresAt"> · 有效至 {{ formatDateTime(record.launcherAuthorization.expiresAt) }}</template></span>
              <span v-if="record.shortAuthorization" class="table-hint">短命令{{ shortAuthorizationStatus(record.shortAuthorization.status) }} · {{ authorizationUsesText(record.shortAuthorization) }}<template v-if="record.shortAuthorization.expiresAt"> · 有效至 {{ formatDateTime(record.shortAuthorization.expiresAt) }}</template></span>
              </template>
              <span v-else class="table-hint">命令与授权仅脚本所有者可查看</span>
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
          <a-form-item v-if="!editing && domainFormatOptions.length > 1" label="长命令地址格式" :html-for="fieldId('domainFormat')">
            <a-select :id="fieldId('domainFormat')" :value="form.domainFormat || domainFormatOptions[0].raw" :onUpdate:value="editorEntry.bind(setFormField, 'domainFormat')">
              <a-select-option v-for="option in domainFormatOptions" :key="option.raw" :value="option.raw">{{ option.raw }}</a-select-option>
            </a-select>
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
              <p v-if="form.entitlementId" role="status">已兑换，待创建。<template v-if="activationFrozenUntil">请在 {{ formatDateTime(activationFrozenUntil) }} 前创建；超时后激活码恢复未使用。</template><template v-else>未创建的资格将在兑换 30 分钟后释放。</template></p>
              <template v-else>
                <a-space wrap class="domain-settings-actions">
                  <a-popconfirm title="确认从钱包购买一次性激活码？" description="按显示价格扣除钱包余额，购买后可在购买记录找回或分享给他人。" ok-text="确认" cancel-text="取消" :onConfirm="editorEntry.bind(buyActivationCode)">
                    <a-button :loading="purchasingCode" :disabled="!canBuyActivationCode">{{ activationPrice?.enabled ? '钱包购买 ¥' + activationPrice.price : '购买暂未开放' }}</a-button>
                  </a-popconfirm>
                  <a-button :onClick="editorEntry.bind(openPurchases)">从购买记录选择</a-button>
                </a-space>
                <a-form-item label="激活码" :html-for="fieldId('activationCode')" class="billing-panel">
                  <a-input :id="fieldId('activationCode')" :value="activationCode" autocomplete="off" :maxlength="68" :disabled="activationBusy || Boolean(purchasedCode)" :onUpdate:value="editorEntry.bind(setActivationCode)" placeholder="输入激活码" />
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
        <!-- 跳过手机号/微信绑定校验（2026-11-17 用户需求）。
             「仅管理员可见」：showSkipBindingButton 按**角色**判断（不是视图模式）。
             只在新建时出现 —— 编辑已有脚本不涉及绑定门槛。

             为什么用 switch + popconfirm 而不是一个普通按钮：
             绕过的是合规门槛，必须让用户看到后果说明再确认；
             用 checkbox 语义（当前状态可见）比一次性按钮更不容易误触。 -->
        <ConsolePanel v-if="showSkipBindingButton && !editing" title="管理员选项" description="仅管理员可见。用于在创建脚本时跳过手机号/微信绑定校验。">
          <a-form-item label="跳过绑定校验" :html-for="fieldId('skipBinding')">
            <a-popconfirm
              :title="buildSkipBindingConfirmText({ isAdmin: true }).title"
              :description="buildSkipBindingConfirmText({ isAdmin: true }).content"
              :ok-text="buildSkipBindingConfirmText({ isAdmin: true }).okText"
              :cancel-text="buildSkipBindingConfirmText({ isAdmin: true }).cancelText"
              :onConfirm="editorEntry.bind(confirmSkipBinding)"
            >
              <a-switch
                :id="fieldId('skipBinding')"
                :checked="skipBindingCheck"
                :disabled="saving"
                checked-children="已跳过"
                un-checked-children="校验中"
              />
            </a-popconfirm>
            <p class="hint">开启后本次创建不校验该账号是否绑定手机号和微信。普通用户看不到此开关，服务端也会按角色拒绝非管理员传入的跳过请求。</p>
          </a-form-item>
        </ConsolePanel>
        <ConsolePanel title="长命令地址" description="保存后生成随机 HTTPS 长地址，可在列表复制长命令。" class="preview-panel">
          <div class="command-preview"><code>{{ editingAddress || '保存后自动生成 HTTPS 地址' }}</code></div>
        </ConsolePanel>
      </a-form>
      </div>
    </a-modal>
    <a-modal v-if="shortAuthorizationRecord" :open="true" :title="shortAuthorizationMode === 'full' ? '长命令授权设置' : '短命令授权设置'" width="660px" :footer="null" :closable="!shortAuthorizationBusy" :mask-closable="!shortAuthorizationBusy" :keyboard="!shortAuthorizationBusy" :onCancel="pageEntry.bind(closeShortAuthorization)" :wrap-props="{ 'aria-modal': true }">
      <div ref="shortAuthorizationSurface" :onInputCapture="pageEntry.guardNative" :onChangeCapture="pageEntry.guardNative" :onClickCapture="pageEntry.guardNative" :onKeydownCapture="pageEntry.guardNative">
        <a-alert type="info" show-icon message="同一脚本同时只允许一个客户端在线" description="长短命令共用在线名额。已有客户端在线时拒绝新接入，请先退出原客户端。命令地址即授权，请妥善保管。" />
        <a-alert v-if="isScriptRunConsumed(shortAuthorizationRecord)" type="warning" show-icon class="consumed-authorization-alert" message="一次性资格已使用，仅当前客户端可继续本次运行" description="不能复制命令、生成短命令或重新授权。当前客户端可以继续完成本次运行。" />
        <p v-if="shortAuthorizationMode === 'full'" class="hint">更新授权会重新生成长命令，旧命令随即失效。更新后请重新复制。</p>
        <a-form layout="vertical" class="short-authorization-form" :disabled="shortAuthorizationBusy || isScriptRunConsumed(shortAuthorizationRecord)">
          <template v-if="shortAuthorizationMode === 'short'">
            <a-form-item label="短地址格式" :html-for="fieldId('shortFormat')" required>
              <a-select :id="fieldId('shortFormat')" :value="shortAuthorizationForm.domainFormat" :onUpdate:value="pageEntry.bind(setShortAuthorizationField, 'domainFormat')" :options="authorizationFormatChoices" option-filter-prop="label" :show-search="authorizationFormatChoices.length > 5" data-script-short-format />
              <p class="hint">先选择管理员允许的格式，再填写子域名或路径。完整地址会自动补上 HTTPS。</p>
              <p class="hint">短地址全站唯一，已被占用的地址不能抢占，是否可用以保存结果为准。</p>
            </a-form-item>
            <a-form-item v-if="selectedAuthorizationFormat" :label="selectedAuthorizationFormat.kind === 'wildcard' ? '子域名' : '路径'" :html-for="fieldId('shortAddress')" required>
              <div class="short-address-slot">
                <span class="short-address-affix">{{ selectedAuthorizationFormat.kind === 'wildcard' ? 'https://' : 'https://' + selectedAuthorizationFormat.host + '/' }}</span>
                <a-input :id="fieldId('shortAddress')" :value="shortAuthorizationForm.slot" :onUpdate:value="pageEntry.bind(setShortAuthorizationField, 'slot')" autocomplete="off" :maxlength="512" :placeholder="selectedAuthorizationFormat.kind === 'wildcard' ? '例如 a' : '例如 team/run'" data-script-short-address />
                <span v-if="selectedAuthorizationFormat.kind === 'wildcard'" class="short-address-affix">.{{ selectedAuthorizationFormat.host }}</span>
              </div>
              <p class="hint">{{ selectedAuthorizationFormat.kind === 'wildcard' ? '仅填写子域名前缀，例如 a；访问生成的子域名根地址即可，不要重复填写域名。' : '仅填写路径，例如 team/run；不要填写域名或协议。' }}</p>
            </a-form-item>
            <div v-else-if="shortAuthorizationForm.address" class="short-existing-address" data-script-short-existing>
              <span>现有短地址</span><code>{{ shortAuthorizationForm.address }}</code>
              <p class="hint">此地址不在当前允许格式中，已保留原值。选择上方的新格式后才会替换，是否可继续使用由服务端校验。</p>
            </div>
            <p class="short-address-preview" data-script-short-preview>完整地址：<code>{{ authorizationAddress || '填写后显示完整地址' }}</code></p>
            <p v-if="authorizationReservedError" class="form-error" role="alert">{{ authorizationReservedError }}</p>
          </template>
          <a-form-item label="有效期" :html-for="fieldId('authorizationDurationMode')">
            <a-radio-group :id="fieldId('authorizationDurationMode')" class="authorization-mode-options" :value="shortAuthorizationForm.durationMode" :onUpdate:value="pageEntry.bind(setShortAuthorizationField, 'durationMode')" button-style="solid">
              <a-radio-button value="limited">限时</a-radio-button>
              <a-radio-button value="permanent" :disabled="!authorizationAllowsForever">永久有效</a-radio-button>
              <a-radio-button value="windows">多个时段</a-radio-button>
            </a-radio-group>
            <p v-if="!authorizationAllowsForever" class="hint">管理员未允许此类命令永久有效，请设置时长或多个时段。</p>
          </a-form-item>
          <div v-if="shortAuthorizationForm.durationMode === 'windows'" class="authorization-windows">
            <p class="hint">在任一指定日期时段内可启动，时段之间暂停接入。最多 32 段，每段{{ shortAuthorizationMode === 'short' ? '最长 24 小时' : '最长 365 天' }}，开始时间不晚于未来 365 天；可以首尾相接，不能重叠。所有时段共用下方的可使用次数。</p>
            <div v-for="(window, index) in shortAuthorizationForm.windows" :key="window.key" class="authorization-window" data-script-authorization-window>
              <div class="authorization-window-heading"><strong>时段 {{ index + 1 }}</strong><a-button size="small" danger :disabled="shortAuthorizationForm.windows.length === 1" :onClick="pageEntry.bind(removeAuthorizationWindow, index)">删除</a-button></div>
              <div class="authorization-window-fields">
                <a-form-item label="开始时间" :html-for="fieldId('window-' + index + '-start')" required>
                  <a-input :id="fieldId('window-' + index + '-start')" type="datetime-local" step="any" :value="window.startsAt" :onUpdate:value="pageEntry.bind(setAuthorizationWindowField, index, 'startsAt')" />
                </a-form-item>
                <a-form-item label="结束时间" :html-for="fieldId('window-' + index + '-end')" required>
                  <a-input :id="fieldId('window-' + index + '-end')" type="datetime-local" step="any" :value="window.expiresAt" :onUpdate:value="pageEntry.bind(setAuthorizationWindowField, index, 'expiresAt')" />
                </a-form-item>
              </div>
            </div>
            <a-button :disabled="shortAuthorizationForm.windows.length >= 32" :onClick="pageEntry.bind(addAuthorizationWindow)">添加时段（{{ shortAuthorizationForm.windows.length }} / 32）</a-button>
            <p class="hint">所有时间使用本机时区；这里只设置指定日期，不会每天重复。</p>
          </div>
          <a-form-item v-if="shortAuthorizationForm.durationMode !== 'windows'" label="生效方式" :html-for="fieldId('authorizationStartMode')">
            <a-radio-group :id="fieldId('authorizationStartMode')" :value="shortAuthorizationForm.startMode" :onUpdate:value="pageEntry.bind(setShortAuthorizationField, 'startMode')" button-style="solid">
              <a-radio-button value="immediate">立即生效</a-radio-button>
              <a-radio-button value="scheduled">指定时间</a-radio-button>
            </a-radio-group>
          </a-form-item>
          <a-form-item v-if="shortAuthorizationForm.durationMode !== 'windows' && shortAuthorizationForm.startMode === 'scheduled'" label="生效时间" :html-for="fieldId('authorizationStartsAt')" required>
            <a-input :id="fieldId('authorizationStartsAt')" type="datetime-local" step="any" :value="shortAuthorizationForm.startsAt" :onUpdate:value="pageEntry.bind(setShortAuthorizationField, 'startsAt')" />
            <p class="hint">使用本机时区。</p>
          </a-form-item>
          <a-form-item v-if="shortAuthorizationForm.durationMode === 'limited'" label="持续时长（分钟）" :html-for="fieldId('shortDuration')" required>
            <a-space wrap>
              <a-input-number :id="fieldId('shortDuration')" :value="shortAuthorizationForm.durationMinutes" :onUpdate:value="pageEntry.bind(setShortAuthorizationField, 'durationMinutes')" :min="1" :max="shortAuthorizationMode === 'short' ? 1440 : 525600" :precision="0" />
              <a-button size="small" :onClick="pageEntry.bind(setShortAuthorizationField, 'durationMinutes', 15)">15 分钟</a-button>
              <a-button size="small" :onClick="pageEntry.bind(setShortAuthorizationField, 'durationMinutes', 60)">1 小时</a-button>
              <a-button size="small" :onClick="pageEntry.bind(setShortAuthorizationField, 'durationMinutes', 1440)">1 天</a-button>
            </a-space>
            <p class="hint">从生效时开始计时，{{ shortAuthorizationMode === 'short' ? '最长 24 小时。' : '最长 365 天。' }}</p>
          </a-form-item>
          <a-form-item label="可使用次数" :html-for="fieldId('authorizationMaxUses')">
            <a-input-number :id="fieldId('authorizationMaxUses')" :value="shortAuthorizationForm.maxUses" :onUpdate:value="pageEntry.bind(setShortAuthorizationField, 'maxUses')" :min="0" :max="1000000" :precision="0" />
            <p class="hint">0 表示不限次数。{{ shortAuthorizationForm.durationMode === 'windows' ? '所有时段共用此次数，不会随时段重置。' : '' }}每次成功下发启动兑换票计为 1 次，后续兑换、下载与运行不再扣次数。</p>
          </a-form-item>
          <p v-if="authorizationError" class="form-error" role="alert">{{ authorizationError }}</p>
        </a-form>
        <div v-if="selectedAuthorization" class="short-authorization-result">
          <a-tag :color="selectedAuthorization.status === 'active' ? 'green' : 'default'">{{ shortAuthorizationStatus(selectedAuthorization.status) }}</a-tag>
          <p class="hint">{{ authorizationUsesText(selectedAuthorization) }}<template v-if="selectedAuthorization.windows?.length"> · 已设置 {{ selectedAuthorization.windows.length }} 个时段，共用次数</template><template v-else-if="selectedAuthorization.startsAt"> · 生效于 {{ formatDateTime(selectedAuthorization.startsAt) }}</template><template v-if="selectedAuthorization.expiresAt"> · 有效至 {{ formatDateTime(selectedAuthorization.expiresAt) }}</template><template v-else> · 永久有效</template></p>
          <code v-if="shortAuthorizationMode === 'short' && buildScriptShortCommand(shortAuthorizationRecord)" class="short-command">{{ buildScriptShortCommand(shortAuthorizationRecord) }}</code>
          <a-button v-if="selectedAuthorization.command" :disabled="isScriptRunConsumed(shortAuthorizationRecord)" :onClick="pageEntry.bind(copyShortCommand)">{{ shortAuthorizationMode === 'short' ? '复制短命令' : '复制长命令' }}</a-button>
        </div>
        <a-space wrap class="short-authorization-actions">
          <a-button :disabled="shortAuthorizationBusy" :onClick="pageEntry.bind(closeShortAuthorization)">关闭</a-button>
          <a-button v-if="['active', 'scheduled', 'paused'].includes(selectedAuthorization?.status)" danger :disabled="isScriptRunConsumed(shortAuthorizationRecord)" :loading="shortAuthorizationBusy" :onClick="pageEntry.bind(saveShortAuthorization, false)">撤销授权</a-button>
          <a-button type="primary" :disabled="isScriptRunConsumed(shortAuthorizationRecord)" :loading="shortAuthorizationBusy" :onClick="pageEntry.bind(saveShortAuthorization, true)">{{ ['active', 'scheduled', 'paused'].includes(selectedAuthorization?.status) ? '更新授权' : shortAuthorizationMode === 'short' ? '生成短命令' : '启用授权' }}</a-button>
        </a-space>
      </div>
    </a-modal>
    <ScriptActivationPurchases v-if="purchasesOpen" :key="principal()" :can-use="canAct" @close="purchasesOpen = false" @use="usePurchasedCode" />
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
import { createScript, deleteScript, getScripts, getScriptDomainFormats, getScriptDomainSettings, getScriptVersions, updateScript, updateScriptDomainSettings, uploadScriptVersion, getScriptEscrowPolicy, updateScriptEscrowPolicy, getScriptActivationCodePrice, updateScriptActivationCodePrice, purchaseScriptActivationCode, redeemScriptActivationCode, configureScriptShortAuthorization, configureScriptLauncherAuthorization } from '@/api/TikuComponentApi'
import { isRedeemableScriptCode, isScriptRunConsumed, buildScriptBillingText, buildScriptCommand, buildScriptShortCommand, buildScriptDownloadURL, buildScriptListParams, parsePresetParams, formatPresetParams, normalizeScriptRecord, normalizeScriptRows } from '@/utils/scriptManagementHelpers.mjs'
import { collectScriptRevokeNotices, scriptNoticeAlertType } from '@/utils/scriptRevokeNotice.mjs'
import { shouldShowSkipBindingButton, buildCreateScriptPayload, isBindingsRequiredError, buildSkipBindingConfirmText } from '@/utils/scriptSkipBinding.mjs'
import { parseScriptDomainFormat, buildScriptDomainFromFormat, splitScriptDomainForFormat, isBuiltinScriptHostReserved } from '@/utils/scriptDomainFormats.mjs'
import { validateScriptAuthorizationWindows } from '@/utils/scriptAuthorizationWindows.mjs'
import { describeScriptAdminAction, describeScriptUploadGate } from '@/utils/scriptUploadGate.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import ScriptActivationPurchases from './ScriptActivationPurchases.vue'
import { scriptPurchaseIntent, clearScriptPurchaseIntent } from '@/utils/scriptPurchaseIntent.mjs'

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
const shortAuthorizationRecord = ref(null), shortAuthorizationBusy = ref(false), shortAuthorizationSurface = ref(null)
const shortAuthorizationMode = ref('short')
const selectedAuthorization = computed(() => shortAuthorizationRecord.value?.[shortAuthorizationMode.value === 'full' ? 'launcherAuthorization' : 'shortAuthorization'])
let authorizationWindowKey = 0
const emptyAuthorizationWindow = () => ({ key: ++authorizationWindowKey, startsAt: '', expiresAt: '' })
const emptyAuthorizationForm = () => ({ address: '', domainFormat: '', slot: '', durationMode: 'limited', durationMinutes: 5, startMode: 'immediate', startsAt: '', maxUses: 1, windows: [emptyAuthorizationWindow()] })
const shortAuthorizationForm = reactive(emptyAuthorizationForm())
const authorizationError = ref('')
const DEFAULT_RESERVED_SHORT_PREFIXES = Object.freeze(['api', 'adapter-service', 'sqp', 'health', 'healthz', 'readyz', 'metrics', 'assets', 'static', 'dist', 'css', 'js', 'img', 'fonts', 'adapter', 'favicon.ico', 'index.html', 'theme-init.js'])
const reservedShortPrefixes = ref([...DEFAULT_RESERVED_SHORT_PREFIXES])
const authorizationAddress = computed(() => {
  if (selectedAuthorizationFormat.value) return normalizeShortAuthorizationAddress(buildScriptDomainFromFormat(selectedAuthorizationFormat.value, shortAuthorizationForm.slot))
  return shortAuthorizationForm.domainFormat === '__existing__' ? normalizeShortAuthorizationAddress(shortAuthorizationForm.address) : ''
})
const allowFullNeverExpire = ref(true), allowShortNeverExpire = ref(false)
const savedExpiryPolicy = ref('true:false')
const authorizationAllowsForever = computed(() => shortAuthorizationMode.value === 'full' ? allowFullNeverExpire.value : allowShortNeverExpire.value)
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
// The route selects a view; it cannot grant administrator privileges during a role change.
const isAdmin = computed(() => routeViewMode.value !== 'self' && isAdminUser(currentUser.value))
const rows = ref([])
// 通知区数据（2026-11-17）。从 rows 派生而不是独立请求：
//   1. 撤销状态本来就随列表一起返回（后端 revokeNotice 字段），再查一次会不一致；
//   2. rows 刷新时通知自动跟着更新，不需要额外维护失效逻辑。
// warning 排在 info 前的排序由 collectScriptRevokeNotices 负责（见其单测）。
const revokeNotices = computed(() => collectScriptRevokeNotices(rows.value))
const loading = ref(false)
const uploading = ref(false)
const uploadVersion = ref('')
const versions = ref([])
const saving = ref(false)
const modalOpen = ref(false)
const editing = ref(false)
// 「跳过手机号/微信绑定校验」按钮的勾选状态（2026-11-17 用户需求）。
//
// 只影响**新建**：编辑已有脚本不涉及绑定门槛，所以按钮也只在新建时显示。
// 默认 false —— 忘记初始化时必须保持门槛生效，不能因为「没读到」而放行。
const skipBindingCheck = ref(false)
// 按钮可见性：仅管理员。
//
// ⚠️ 这里必须用 `isAdminUser(currentUser.value)`（角色），**不能**用上面的
// `isAdmin` computed —— 后者按**视图模式**判断：管理员切到「我的脚本」
// （routeViewMode==='self'）时它返回 false，而那个场景恰恰是本按钮要服务的
// （用户在「我的脚本」里创建自己的脚本时被绑定门槛拦住）。
// 用错会表现为「按钮在别的视图里有、在自己要用的视图里消失」。
const showSkipBindingButton = computed(() => shouldShowSkipBindingButton({ isAdmin: isAdminUser(currentUser.value) }))
const lastUploadKey = ref('')
const summary = reactive({ total: 0, enabled: 0, reserved: 0 })
const domainText = ref('')
const deniedDomainText = ref('')
const domainReady = ref(false)
const domainSaving = ref(false)
// 诉求 ④：`domainAllowWildcard` 已删除（泛域名可用性由列表内容表达）。
const domainError = ref('')
// 保存按钮的 dirty 判据：只有内容真的变了才允许保存，避免「点了一下但什么都没改」的无效往返。
const domainSavedText = ref('')
const deniedDomainSavedText = ref('')
const domainDirty = computed(() => domainText.value.trim() !== domainSavedText.value.trim() || deniedDomainText.value.trim() !== deniedDomainSavedText.value.trim() || savedExpiryPolicy.value !== `${allowFullNeverExpire.value}:${allowShortNeverExpire.value}`)
// 诉求 ⑤：用户可选的域名格式模板（管理员配置），以及编辑中的域名行。
const domainFormats = ref([])
const scriptAdminAction = describeScriptAdminAction()
const escrowPolicy = ref(null), activationPrice = ref(null), policyError = ref('')
const policyForm = reactive({ target: '', minimum: '', multiplier: '' }), priceForm = ref('')
const savingPolicy = ref(false), savingPrice = ref(false)
let billingReadTicket = 0, policyRevision = 0, priceRevision = 0
const activationCode = ref(''), purchasedCode = ref(false), purchaseUncertain = ref(false)
const activationFrozenUntil = ref(null), purchasesOpen = ref(false)
const purchasingCode = ref(false), redeemingCode = ref(false)
const activationBusy = computed(() => purchasingCode.value || redeemingCode.value)
// 诉求 ①：`nativeLeaseConfirmed` 已删除 —— 那个确认框是「上传按钮无效」的根因之一
// （按钮 disabled 依赖一个用户不知道要勾的框）。现在 gate 只看真正的必填项。
const uploadGate = computed(() => describeScriptUploadGate({ uploading: uploading.value, version: uploadVersion.value }))
const currentNativeVersion = computed(() => versions.value.find((item) => item.nativeLeaseProtocol === true && item.isCurrent === true))
// 诉求 ⑤：用户可选的格式来自管理员配置，且只保留能解析的新语法条目。
const domainFormatOptions = computed(() => domainFormats.value.filter(Boolean))
const authorizationDomainFormats = computed(() => domainFormatOptions.value.length ? domainFormatOptions.value : [{ raw: `${window.location.host}/*`, host: window.location.host, kind: 'path', slot: 'suffix' }])
const selectedAuthorizationFormat = computed(() => authorizationDomainFormats.value.find(format => format.raw === shortAuthorizationForm.domainFormat))
const authorizationFormatChoices = computed(() => [
  ...(shortAuthorizationForm.domainFormat === '__existing__' ? [{ value: '__existing__', label: '保留现有短地址（不在当前允许格式中）' }] : []),
  ...authorizationDomainFormats.value.map(format => ({ value: format.raw, label: `${format.kind === 'wildcard' ? '子域名' : '路径'} · ${format.raw}` })),
])
const authorizationReservedError = computed(() => {
  if (shortAuthorizationMode.value !== 'short') return ''
  if (isBuiltinScriptHostReserved(authorizationAddress.value)) {
    return 'localhost、回环地址和未指定地址不能分配给脚本，但仍可用于访问 WebUI/API。请更换为可公开访问的域名。'
  }
  let path = ''
  if (selectedAuthorizationFormat.value?.kind === 'path') path = shortAuthorizationForm.slot
  else if (shortAuthorizationForm.domainFormat === '__existing__' && authorizationAddress.value) path = new URL(authorizationAddress.value).pathname
  const prefix = String(path || '').trim().replace(/^\/+/, '').split('/')[0].toLowerCase()
  return prefix && reservedShortPrefixes.value.includes(prefix) ? `路径首段“${prefix}”为系统保留路径，请更换后再保存。` : ''
})
let purchaseIntentKey = '', purchaseIntentQuote = ''
const canBuyActivationCode = computed(() => modalOpen.value && !editing.value && form.billingMode === 'activation_code'
  && !saving.value && !activationBusy.value && !purchasedCode.value && !form.entitlementId
  && (purchaseUncertain.value || activationPrice.value?.enabled === true))

const emptyForm = () => ({ id: undefined, name: '', versionId: undefined, enabled: true, presetText: '', billingMode: 'escrow', entitlementId: undefined, autoTopUp: false, runDurationMinutes: 150, notifyOnDenied: false, domainFormat: '' })
const form = reactive(emptyForm())
const draft = useUnsavedDraft(form, { focusTriggerAfterClose: false })
const editingAddress = ref('')
const scopeDescription = computed(() => isAdmin.value ? '当前显示全部用户的脚本；删除和编辑仍由服务端校验权限。' : '当前只显示自己的脚本。')
const columns = computed(() => [
  { title: '脚本', key: 'name', dataIndex: 'name', width: 220 },
  ...(isAdmin.value ? [{ title: '用户', key: 'owner', dataIndex: 'ownerName', width: 150 }] : []),
  { title: '命令与授权', key: 'command', dataIndex: 'command', width: 430 },
  { title: '预置参数', key: 'preset', dataIndex: 'preset', width: 100 },
  { title: '答题计费', key: 'billing', dataIndex: 'billing', width: 180 },
  { title: '更新时间', key: 'updatedAt', dataIndex: 'updatedAt', width: 150 },
  { title: '操作', key: 'operation', dataIndex: 'operation', width: 130 },
])
const tableScrollX = computed(() => sumTableColumnWidths(columns.value))

function resetForm(record) {
  const fields = emptyForm()
  editingAddress.value = record ? buildScriptDownloadURL(record) : ''
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
  }
  form[name] = value
}
function setUploadVersion(value) { if (isAdmin.value && !uploading.value) uploadVersion.value = value }
function setDomainText(value) { if (isAdmin.value && !domainSaving.value) { domainRevision++; domainText.value = value } }
function setDeniedDomainText(value) { if (canAct() && isAdmin.value && !domainSaving.value) { domainRevision++; deniedDomainText.value = value } }
function setExpiryPolicy(kind, value) {
  if (!canAct() || !isAdmin.value || domainSaving.value) return
  domainRevision++
  if (kind === 'full') allowFullNeverExpire.value = Boolean(value)
  else allowShortNeverExpire.value = Boolean(value)
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
function openPurchases() { if (canAct()) purchasesOpen.value = true }
function usePurchasedCode(code) {
  if (!canAct() || !isRedeemableScriptCode(code) || saving.value || activationBusy.value) return
  purchasesOpen.value = false
  if (!modalOpen.value) startEditor()
  if (editing.value) return
  form.billingMode = 'activation_code'; form.entitlementId = undefined
  activationCode.value = code; purchasedCode.value = true; activationFrozenUntil.value = null
}
function openEdit(record) { if (rows.value.includes(record)) startEditor(record) }
function retireEditor() {
  hidePrivate(modalSurface.value)
  activationCode.value = ''; purchasedCode.value = false; purchaseUncertain.value = false; purchaseIntentKey = ''; purchaseIntentQuote = ''
  activationFrozenUntil.value = null
  purchasingCode.value = false; redeemingCode.value = false
  // 清掉「跳过绑定校验」状态。放在这里而不是只放 openCreate：
  // retireEditor 是所有关闭路径的共同出口（保存成功、取消、路由离开），
  // 只在 openCreate 清会漏掉「保存失败后关弹窗再开」这类路径。
  resetSkipBinding()
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
  const scope = buildScriptListParams(isAdmin.value)
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
    clearScriptPurchaseIntent(currentUser.value?.id)
    purchaseUncertain.value = false; purchaseIntentKey = ''; purchaseIntentQuote = ''
    activationPrice.value = null
    message.warning('激活码售价已变化，请确认最新售价后再购买。')
    void loadBillingSettings()
    return
  }
  purchaseUncertain.value = true
  message.error(reason === 'SCRIPT_ACTIVATION_BALANCE_INSUFFICIENT' ? '钱包余额不足，请补足后再购买。' : '购买结果未确认，请先在购买记录核对。再次点击购买会核对原交易，避免重复扣款。')
}
async function buyActivationCode() {
  const owns = editorEntry.value.isCurrent
  if (!owns() || !canBuyActivationCode.value) return
  purchasingCode.value = true
  try {
    if (!purchaseIntentKey) {
      purchaseIntentQuote = activationPrice.value?.price || ''
      if (!purchaseIntentQuote) return
      const intent = scriptPurchaseIntent(currentUser.value?.id, purchaseIntentQuote)
      purchaseIntentKey = intent.key; purchaseIntentQuote = intent.quote
    }
    const response = await purchaseScriptActivationCode(purchaseIntentKey, purchaseIntentQuote)
    if (!owns()) return
    const code = response?.data?.code
    if (response?.status !== 200 || typeof code !== 'string' || !/^sac_[a-f0-9]{64}$/.test(code) ||
      decimalMicros(response.data?.price) !== decimalMicros(purchaseIntentQuote)) {
      showActivationPurchaseFailure(response)
      return
    }
    clearScriptPurchaseIntent(currentUser.value?.id)
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
  if (!isRedeemableScriptCode(code)) return message.warning('请输入有效激活码。')
  redeemingCode.value = true
  try {
    const response = await redeemScriptActivationCode({ code })
    if (!owns()) return
    const id = Number(response?.data?.id)
    if (response?.status !== 200 || !Number.isSafeInteger(id) || id <= 0 || response?.data?.scriptId) {
      message.error('未取得可用的创建资格，请确认激活码状态。'); return
    }
    form.entitlementId = id; activationCode.value = ''; purchasedCode.value = false
    activationFrozenUntil.value = response.data.frozenUntil || null
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

// 用户点 a-popconfirm 的「仍然跳过」后才真正打开开关（2026-11-17）。
//
// 为什么不在 switch 上直接双向绑定：a-switch 的 onUpdate:checked 会在用户
// 点击瞬间就翻转，确认弹窗还没出现状态就已经变了。放在 popconfirm 的
// onConfirm 里，语义是「确认后才生效」，与用户的心理模型一致。
function confirmSkipBinding() {
  if (!editorEntry.value.isCurrent()) return
  skipBindingCheck.value = true
}

// 关闭并重置编辑器时清掉跳过状态（2026-11-17）。
//
// ⚠️ 必须清：否则「上一个脚本勾了跳过，关掉弹窗后新建另一个」会带着
// 上次的 true 静默绕过门槛。纯函数 buildCreateScriptPayload 只能清 payload，
// 清不掉组件状态。
function resetSkipBinding() {
  skipBindingCheck.value = false
}

async function handleSubmit() {  const owns = editorEntry.value.isCurrent
  if (!owns() || saving.value || deleting.value || activationBusy.value) return
  // 跳过绑定校验（2026-11-17）：只在新建时生效。
  //
  // 二次确认由模板里的 a-popconfirm 承担（与页面其它危险操作同一交互范式），
  // 用户点「仍然跳过」后才会把 skipBindingCheck 置真再来调用本函数。
  // 这里不再弹窗 —— 嵌套 confirm 会让「取消」出现在两个层级，用户分不清。
  const requestSkipBinding = !editing.value && skipBindingCheck.value && showSkipBindingButton.value
  // buildCreateScriptPayload 负责「只有显式 true 才写字段」，
  // 并清掉可能残留的脏值（上一次勾过、这次取消）。
  const payload = buildCreateScriptPayload({
    name: form.name.trim(),
    enabled: form.enabled,
    parameters: parsePresetParams(form.presetText),
  }, requestSkipBinding)
  if (!payload.name) return message.warning('请输入脚本名称')
  const isEditing = editing.value, sentSnapshot = draft.currentConfigSnapshot.value
  if (isEditing) payload.id = form.id
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
    if (!form.versionId) return message.warning('请选择 EXE 版本')
    payload.versionId = form.versionId
    if (form.domainFormat) payload.domainFormat = form.domainFormat
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
      // 缺绑定的提示分流（2026-11-17）：管理员已经能看到「跳过绑定校验」开关，
      // 所以对他要提示「可以先勾跳过」，对普通用户只说去绑定 ——
      // 否则管理员会以为功能坏了。
      message.error(isBindingsRequiredError(response)
        ? (showSkipBindingButton.value
          ? '创建脚本前请绑定手机号和微信（管理员可在弹窗内「管理员选项」中跳过本次校验）。'
          : '创建脚本前请绑定手机号和微信。')
        : response?.data?.code === 'SCRIPT_LAUNCHER_HTTPS_REQUIRED'
          ? response.data.message
          : '保存脚本未完成，请刷新列表核对后重试。')
      return
    }
    if (!isEditing) retry = null
    draft.savedConfigSnapshot.value = sentSnapshot
    const sameDraft = draft.currentConfigSnapshot.value === sentSnapshot
    if (sameDraft) retireEditor()
    message.success(isEditing ? '脚本已更新' : '脚本已创建')
    await loadScripts()
  } catch (error) {
    if (owns()) message.error(isBindingsRequiredError(error)
      ? '创建脚本前请绑定手机号和微信。'
      : error?.response?.data?.code === 'SCRIPT_LAUNCHER_HTTPS_REQUIRED'
        ? error.response.data.message
        : '保存结果未确认，请刷新核对；如重试，请保留原表单。')
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
  return isOwnScript(record) && !isEscrowClosing(record) && record.scheme === 'https' && Boolean(buildScriptCommand(record))
    && (!record.launcherAuthorization || ['active', 'scheduled', 'paused'].includes(record.launcherAuthorization.status))
}
function isOwnScript(record) { return Boolean(currentUser.value?.id) && String(record?.ownerId ?? '') === String(currentUser.value.id) }
function copyCommand(record) {
  if (rows.value.includes(record) && mayCopyCommand(record)) return copyText(buildScriptCommand(record))
}
function copyShortRecordCommand(record) {
  if (!canAct() || !rows.value.includes(record) || !isOwnScript(record) || isEscrowClosing(record)) return
  const command = buildScriptShortCommand(record)
  if (command) return copyText(command)
}
function shortAuthorizationStatus(status) { return ({ active: '已启用', scheduled: '待生效', paused: '等待下一时段', consumed: '一次性资格已使用', used: '已使用', exhausted: '次数已用完', expired: '已过期', disabled: '已撤销' })[status] || '未启用' }
function authorizationUsesText(grant) { return Number(grant?.maxUses) > 0 ? `已使用 ${Number(grant.usedCount) || 0} / ${grant.maxUses} 次` : `已使用 ${Number(grant?.usedCount) || 0} 次 · 不限次数` }
function localDateTimeInput(value) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, -1).replace(/\.000$/, '').replace(/:00$/, '')
}
function normalizeShortAuthorizationAddress(raw) {
  const value = String(raw ?? '').trim()
  if (!value) return ''
  const address = value.includes('://') ? value : `https://${value}`
  if (!/^https:\/\/(?:[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?|\[[0-9a-f:.]+\])(?::[0-9]+)?(?:\/[A-Za-z0-9_~.-]+)*\/?$/i.test(address)) return ''
  if (/(?:^|\/)\.{1,2}(?:\/|$)/.test(address)) return ''
  try {
    const url = new URL(address)
    if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || /(?:^|\/)\.{1,2}(?:\/|$)/.test(url.pathname)) return ''
    return url.origin + (url.pathname === '/' ? '' : url.pathname.replace(/\/$/, ''))
  } catch { return '' }
}
function restoreShortAddress(address = '') {
  shortAuthorizationForm.address = address
  const normalized = normalizeShortAuthorizationAddress(address)
  const domain = normalized.replace(/^https:\/\//, '')
  for (const format of authorizationDomainFormats.value) {
    const slot = splitScriptDomainForFormat(format, domain)
    if (slot !== null) { shortAuthorizationForm.domainFormat = format.raw; shortAuthorizationForm.slot = slot; return }
  }
  shortAuthorizationForm.domainFormat = address ? '__existing__' : authorizationDomainFormats.value[0]?.raw || ''
  shortAuthorizationForm.slot = ''
}
function openShortAuthorization(record, mode = 'short') {
  if (!canAct() || shortAuthorizationBusy.value || !rows.value.includes(record) || !isOwnScript(record) || isScriptRunConsumed(record) || isEscrowClosing(record) || record.scheme !== 'https') return
  shortAuthorizationMode.value = mode === 'full' ? 'full' : 'short'
  const grant = record[shortAuthorizationMode.value === 'full' ? 'launcherAuthorization' : 'shortAuthorization']
  const permanent = !grant?.expiresAt && authorizationAllowsForever.value && (Boolean(grant) || shortAuthorizationMode.value === 'full')
  const durationMinutes = grant?.expiresAt ? Math.min(shortAuthorizationMode.value === 'full' ? 525600 : 1440, Math.max(1, Math.ceil((Date.parse(grant.expiresAt) - Math.max(Date.now(), Date.parse(grant.startsAt) || 0)) / 60000))) : (permanent ? 0 : 5)
  Object.assign(shortAuthorizationForm, { durationMode: grant?.windows?.length ? 'windows' : permanent ? 'permanent' : 'limited', durationMinutes, startMode: grant?.startsAt ? 'scheduled' : 'immediate', startsAt: localDateTimeInput(grant?.startsAt), maxUses: grant?.maxUses ?? (shortAuthorizationMode.value === 'full' ? 0 : 1), windows: grant?.windows?.length ? grant.windows.map(window => ({ ...emptyAuthorizationWindow(), startsAt: localDateTimeInput(window.startsAt), expiresAt: localDateTimeInput(window.expiresAt), startsAtSource: window.startsAt, expiresAtSource: window.expiresAt })) : [emptyAuthorizationWindow()] })
  restoreShortAddress(record.shortAuthorization?.address || '')
  authorizationError.value = ''
  shortAuthorizationRecord.value = record
}
function closeShortAuthorization(force = false) {
  if (shortAuthorizationBusy.value && force !== true) return
  hidePrivate(shortAuthorizationSurface.value)
  shortAuthorizationRecord.value = null; shortAuthorizationBusy.value = false
  Object.assign(shortAuthorizationForm, emptyAuthorizationForm())
  authorizationError.value = ''
}
function setShortAuthorizationField(field, value) {
  if (!canAct() || !shortAuthorizationRecord.value || isScriptRunConsumed(shortAuthorizationRecord.value) || shortAuthorizationBusy.value || !Object.hasOwn(shortAuthorizationForm, field)) return
  if (field === 'durationMode') {
    if (!['limited', 'permanent', 'windows'].includes(value) || (value === 'permanent' && !authorizationAllowsForever.value)) return
    if (value === 'limited' && !(Number(shortAuthorizationForm.durationMinutes) > 0)) shortAuthorizationForm.durationMinutes = 5
  }
  if (field === 'startMode' && !['immediate', 'scheduled'].includes(value)) return
  if (field === 'windows') return
  if (field === 'domainFormat' && !authorizationDomainFormats.value.some(format => format.raw === value)) return
  if (field === 'address') { restoreShortAddress(String(value || '')); authorizationError.value = ''; return }
  shortAuthorizationForm[field] = value
  if (field === 'durationMinutes') shortAuthorizationForm.durationMode = Number(value) === 0 && authorizationAllowsForever.value ? 'permanent' : 'limited'
  if (field === 'startsAt') shortAuthorizationForm.startMode = value ? 'scheduled' : 'immediate'
  authorizationError.value = ''
}
function addAuthorizationWindow() {
  if (!canAct() || !shortAuthorizationRecord.value || isScriptRunConsumed(shortAuthorizationRecord.value) || shortAuthorizationBusy.value || shortAuthorizationForm.windows.length >= 32) return
  shortAuthorizationForm.windows.push(emptyAuthorizationWindow()); authorizationError.value = ''
}
function removeAuthorizationWindow(index) {
  if (!canAct() || !shortAuthorizationRecord.value || isScriptRunConsumed(shortAuthorizationRecord.value) || shortAuthorizationBusy.value || shortAuthorizationForm.windows.length <= 1 || !Number.isInteger(index) || index < 0 || index >= shortAuthorizationForm.windows.length) return
  shortAuthorizationForm.windows.splice(index, 1); authorizationError.value = ''
}
function setAuthorizationWindowField(index, field, value) {
  if (!canAct() || !shortAuthorizationRecord.value || isScriptRunConsumed(shortAuthorizationRecord.value) || shortAuthorizationBusy.value || !Number.isInteger(index) || !shortAuthorizationForm.windows[index] || !['startsAt', 'expiresAt'].includes(field)) return
  shortAuthorizationForm.windows[index][field] = String(value || '')
  shortAuthorizationForm.windows[index][field + 'Source'] = ''
  authorizationError.value = ''
}
async function saveShortAuthorization(enabled) {
  const record = shortAuthorizationRecord.value
  if (!canAct() || shortAuthorizationBusy.value || !rows.value.includes(record) || !isOwnScript(record) || isScriptRunConsumed(record)) return
  const permanent = shortAuthorizationForm.durationMode === 'permanent'
  const multiple = shortAuthorizationForm.durationMode === 'windows'
  const durationMinutes = permanent ? 0 : Number(shortAuthorizationForm.durationMinutes)
  const maxUses = Number(shortAuthorizationForm.maxUses), full = shortAuthorizationMode.value === 'full'
  const scheduled = shortAuthorizationForm.startMode === 'scheduled'
  const startsAt = scheduled && shortAuthorizationForm.startsAt ? new Date(shortAuthorizationForm.startsAt) : null
  if (enabled && !full && !authorizationAddress.value) { authorizationError.value = '请填写有效的 HTTPS 短地址：选择格式后填写子域名或路径。'; return }
  if (enabled && !full && authorizationReservedError.value) return
  const checkedWindows = multiple ? validateScriptAuthorizationWindows(shortAuthorizationForm.windows.map(window => ({ startsAt: window.startsAtSource || window.startsAt, expiresAt: window.expiresAtSource || window.expiresAt })), { maxDurationMinutes: full ? 525600 : 1440 }) : null
  if (enabled && checkedWindows?.error) { authorizationError.value = checkedWindows.error; return }
  if (enabled && ((!multiple && ((!permanent && (!Number.isSafeInteger(durationMinutes) || durationMinutes < 1 || durationMinutes > (full ? 525600 : 1440))) || (permanent && !authorizationAllowsForever.value) || (scheduled && !startsAt) || (startsAt && (Number.isNaN(startsAt.getTime()) || startsAt.getTime() > Date.now() + 365 * 86400000)))) || !Number.isSafeInteger(maxUses) || maxUses < 0 || maxUses > 1000000)) { authorizationError.value = '请填写有效的授权时间与使用次数，生效时间不晚于未来 365 天。'; message.warning(authorizationError.value); return }
  const payload = enabled ? { enabled: true, ...(!full ? { address: authorizationAddress.value } : {}), ...(multiple ? { windows: checkedWindows.windows } : { durationMinutes, startsAt: startsAt?.toISOString() || null }), maxUses } : { enabled: false }
  const owns = captureOwner(), current = () => owns() && shortAuthorizationRecord.value === record
  shortAuthorizationBusy.value = true
  try {
    const response = await (full ? configureScriptLauncherAuthorization : configureScriptShortAuthorization)(record.id, payload)
    if (!current()) return
    if (response?.status !== 200 || !response.data?.status) { authorizationError.value = response?.data?.message || '命令授权结果未确认，请刷新核对。'; message.error(authorizationError.value); return }
    record[full ? 'launcherAuthorization' : 'shortAuthorization'] = response.data
    if (full) {
      record.command = response.data.command || ''; record.instruction = record.command
      if (response.data.address) record.downloadUrl = response.data.address
    } else restoreShortAddress(response.data.address || authorizationAddress.value)
    message.success(enabled ? '命令授权已更新，请使用新的指令。' : '命令授权已撤销')
  } catch (error) {
    if (current()) { authorizationError.value = error?.response?.data?.message || '命令授权结果未确认，请刷新核对。'; message.error(authorizationError.value) }
  } finally { if (current()) shortAuthorizationBusy.value = false }
}
function copyShortCommand() {
  const record = shortAuthorizationRecord.value
  if (!canAct() || !isOwnScript(record)) return
  const command = shortAuthorizationMode.value === 'full' ? buildScriptCommand(record) : buildScriptShortCommand(record)
  if (!command) return message.warning('当前命令已失效，请刷新或重新授权。')
  return copyText(command)
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
    deniedDomainText.value = Array.isArray(response.data?.deniedDomains) ? response.data.deniedDomains.join('\n') : ''
    deniedDomainSavedText.value = deniedDomainText.value
    allowFullNeverExpire.value = response.data?.allowFullNeverExpire !== false
    allowShortNeverExpire.value = response.data?.allowShortNeverExpire === true
    savedExpiryPolicy.value = `${allowFullNeverExpire.value}:${allowShortNeverExpire.value}`
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
    if (response?.status !== 200) { domainFormats.value = []; reservedShortPrefixes.value = [...DEFAULT_RESERVED_SHORT_PREFIXES]; return }
    allowFullNeverExpire.value = response.data?.allowFullNeverExpire !== false
    allowShortNeverExpire.value = response.data?.allowShortNeverExpire === true
    reservedShortPrefixes.value = [...new Set([...DEFAULT_RESERVED_SHORT_PREFIXES, ...(Array.isArray(response.data?.reservedPrefixes) ? response.data.reservedPrefixes : [])].map(prefix => String(prefix || '').trim().toLowerCase()).filter(Boolean))]
    const list = Array.isArray(response.data?.formats) ? response.data.formats : []
    // 只保留能解析的新语法条目：服务端已过滤，这里再挡一次是为了让
    // 「界面给了一个填不出域名的选项」这种不可用状态不可能出现。
    domainFormats.value = list
      .map((item) => parseScriptDomainFormat(item?.raw ?? item?.pattern ?? ''))
      .filter(Boolean)
  } catch {
    if (owns()) { domainFormats.value = []; reservedShortPrefixes.value = [...DEFAULT_RESERVED_SHORT_PREFIXES] }
  }
}

function splitDomainSettingsLines(text) {
  // 后端按数组下标报告错误行号；有内容时保留空行，全空白时仍用空数组清空配置。
  return text.trim() ? text.split(/\r?\n/).map(value => value.trim()) : []
}

async function saveDomainSettings() {
  if (!canAct() || !isAdmin.value || !domainReady.value || domainSaving.value) return
  const owns = captureOwner(), ticket = ++domainRevision
  domainSaving.value = true
  domainError.value = ''
  try {
    const domains = splitDomainSettingsLines(domainText.value)
    const deniedDomains = splitDomainSettingsLines(deniedDomainText.value)
    const response = await updateScriptDomainSettings({ domains, deniedDomains, allowFullNeverExpire: allowFullNeverExpire.value, allowShortNeverExpire: allowShortNeverExpire.value })
    if (!owns() || ticket !== domainRevision) return
    const policy = response?.data
    if (response?.status !== 200 || !Array.isArray(policy?.domains) || !Array.isArray(policy?.deniedDomains)) {
      // Only the normalized policy returned by the admin API acknowledges both lists.
      const detail = response?.status === 200 ? '域名设置保存结果未确认：未收到完整策略，请刷新核对。' : response?.data?.message || '保存域名设置未完成，请刷新核对。'
      domainError.value = detail
      message.error(detail)
      return
    }
    domainText.value = policy.domains.join('\n')
    domainSavedText.value = domainText.value
    deniedDomainText.value = policy.deniedDomains.join('\n')
    deniedDomainSavedText.value = deniedDomainText.value
    allowFullNeverExpire.value = response.data?.allowFullNeverExpire !== false
    allowShortNeverExpire.value = response.data?.allowShortNeverExpire === true
    savedExpiryPolicy.value = `${allowFullNeverExpire.value}:${allowShortNeverExpire.value}`
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
  hidePrivate(pageSurface.value); retireEditor(); cancelActiveScriptClose(); purchasesOpen.value = false
  closeShortAuthorization(true); rows.value = []; versions.value = []
  Object.assign(summary, { total: 0, enabled: 0, reserved: 0 })
  readError.value = ''; listReady.value = false; loading.value = false; deleting.value = false
  domainText.value = ''; deniedDomainText.value = ''; domainReady.value = false; domainSaving.value = false; domainError.value = ''
  domainFormats.value = []
  reservedShortPrefixes.value = [...DEFAULT_RESERVED_SHORT_PREFIXES]
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
.denied-domain-field { margin-top: 20px; }
.denied-domain-field > label { display: block; margin-bottom: 8px; }
.short-authorization-form, .short-authorization-actions { margin-top: 20px; }
.short-authorization-result { padding: 16px; border: 1px solid var(--console-border); border-radius: 12px; }
.consumed-authorization-alert { margin-top: 12px; }
.short-address-slot { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-width: 0; }
.short-address-slot :deep(.ant-input) { flex: 1 1 120px; min-width: 0; }
.short-address-affix { color: var(--console-muted); font-size: 13px; overflow-wrap: anywhere; }
.short-address-preview, .short-existing-address { margin: 0 0 24px; padding: 12px; border-radius: 10px; border: 1px solid var(--console-border); background: var(--console-surface-muted, #f8faf9); overflow-wrap: anywhere; }
.short-existing-address > code { display: block; margin-top: 6px; }
.authorization-mode-options { display: flex; flex-wrap: wrap; row-gap: 8px; }
.authorization-windows { display: grid; gap: 12px; margin-bottom: 24px; }
.authorization-window { padding: 14px; border: 1px solid var(--console-border); border-radius: 12px; }
.authorization-window-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.authorization-window-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.authorization-window-fields :deep(.ant-form-item) { margin-bottom: 0; min-width: 0; }
.authorization-window-fields :deep(.ant-input) { min-width: 0; width: 100%; }
.form-error { color: var(--console-danger, #cf1322); margin: 8px 0 16px; }
.short-command { display: block; margin: 12px 0; overflow-wrap: anywhere; white-space: normal; }
.script-name-cell, .command-cell { display: flex; align-items: center; gap: 8px; min-width: 0; }
.script-name-cell { flex-wrap: wrap; }
.command-cell { align-items: flex-start; flex-direction: column; }
.command-cell code, .command-preview code { display: block; width: 100%; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--console-text); }
.form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 16px; }
.switch-description { margin-left: 10px; color: var(--console-muted); }
.billing-panel, .preview-panel { margin-top: 16px; }
.command-preview { display: flex; align-items: flex-start; gap: 12px; padding: 12px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-surface-muted, #f8faf9); }
@media (max-width: 700px) { .form-grid, .authorization-window-fields { grid-template-columns: 1fr; } .command-preview { flex-direction: column; } }
</style>
