<template>
  <div class="console-page system-settings-page" :onInputCapture="settingsEntry.guardNative" :onChangeCapture="settingsEntry.guardNative" :onClickCapture="settingsEntry.guardNative">
    <ConsolePageHeading title="系统设置" description="集中管理认证、通知、支付和站点运行参数。">
      <template #actions><a-space wrap class="section-actions">
        <a-button :onClick="settingsEntry.bind(handleLoad)" :disabled="!canManageSettings() || settingsDraining || saving" :loading="tabLoading">刷新</a-button>
        <a-button type="primary" :onClick="settingsEntry.bind(handleSave)" :disabled="!canManageSettings() || settingsDraining || !settingsLoaded || tabLoading || saving" :loading="saving">保存</a-button>
      </a-space></template>
    </ConsolePageHeading>

      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="这里维护公开认证、微信登录、验证码、短信宝、支付回调和支付方式。修改后会立即影响后续注册、登录与充值链路。"
      />

      <SystemMetricsPanel />
      <a-alert v-if="!canManageSettings()" type="warning" show-icon message="当前身份尚未确认或无系统设置管理权限，请重新登录管理员账号。" />
      <a-alert v-if="settingsError" type="error" show-icon :message="settingsError" />
      <section v-if="settingsReadFailure" class="page-alert" data-system-settings-read-feedback>
        <RequestFailureAlert :key="settingsReadFailureContextKey" :failure="settingsReadFailure" :message="settingsError" :busy="tabLoading" :context-key="settingsReadFailureContextKey" :is-current="isCurrentSettingsReadFailure" />
      </section>
      <section v-if="settingsSaveFailure" class="page-alert" data-system-settings-save-feedback>
        <RequestFailureAlert :key="settingsSaveFailureContextKey" :failure="settingsSaveFailure" message="保存失败，请重试；未保存的更改仍保留。" :busy="saving" :context-key="settingsSaveFailureContextKey" :is-current="isCurrentSettingsSaveFailure" />
      </section>
      <a-alert v-if="hasUnsavedChanges" type="warning" show-icon message="有未保存的更改，离开或刷新前请先保存。" />

      <a-form v-if="settingsFormMounted" :key="settingsFormKey" ref="settingsForm" :disabled="!canManageSettings() || settingsDraining || !settingsLoaded || saving" :label-col="labelCol" :wrapper-col="wrapperCol" class="settings-form">
        <ConsolePanel title="站点与日志" description="配置站点名称和日志存储上限。" class="settings-section">
        <a-form-item label="网站名称" :html-for="fieldId('siteName')">
          <a-input :id="fieldId('siteName')" :value="formState.siteName" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'siteName') }" placeholder="例如 题库" />
          <div class="hint">用于浏览器标题、页头品牌和登录页名称，留空时恢复为“题库”。</div>
        </a-form-item>

        <a-form-item label="最大日志大小" :html-for="fieldId('maxLogSizeValue')">
          <div class="log-size-controls">
          <span class="log-size-control">
          <label :id="fieldId('maxLogSizeValueLabel')" :for="fieldId('maxLogSizeValue')" >数值</label>
          <a-input-number :id="fieldId('maxLogSizeValue')"
            :value="formState.maxLogSizeValue" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'maxLogSizeValue') }"
            :min="minimumLogSizeValue"
            :precision="0"
            :step="1"
            :parser="parseIntegerInput"
            style="width: 180px"
          />
          </span>
          <span class="log-size-control">
          <label :id="fieldId('maxLogSizeUnitLabel')" :for="fieldId('maxLogSizeUnit')" >最大日志大小 单位</label>
          <a-select :id="fieldId('maxLogSizeUnit')" :value="formState.maxLogSizeUnit" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'maxLogSizeUnit') }" style="width: 110px">
            <a-select-option v-for="unit in LOG_SIZE_UNITS" :key="unit.value" :value="unit.value">
              {{ unit.label }}
            </a-select-option>
          </a-select>
          </span>
          </div>
          <div class="hint">日志目录全部日志文件的总占用上限，默认 5GB。支持 KB、MB、GB、TB，保存时会转换为字节。超过上限时会优先删除最旧的归档日志。</div>
        </a-form-item>
        </ConsolePanel>

        <ConsolePanel title="认证与充值" description="管理注册、登录和账户充值规则。" class="settings-section">
        <a-form-item label="允许公开注册" :html-for="fieldId('registerEnabled')">
          <a-switch :id="fieldId('registerEnabled')" :checked="formState.registerEnabled" v-on="{ 'update:checked': settingsEntry.bind(updateSetting, 'registerEnabled') }" />
          <div class="hint">关闭后，注册页会直接阻止进入；管理员在后台创建用户不受影响。</div>
        </a-form-item>

        <a-form-item label="新用户赠送金额" :html-for="fieldId('newUserBonusAmount')">
          <a-input-number :id="fieldId('newUserBonusAmount')"
            :value="formState.newUserBonusAmount" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'newUserBonusAmount') }"
            :min="0.01"
            :precision="6"
            style="width: 220px"
          />
          <div class="hint">新用户注册或后台创建成功后自动计入余额；后台创建时可单独调整或关闭。金额最多支持 6 位小数。</div>
        </a-form-item>

        <a-form-item label="充值最低金额" :html-for="fieldId('minRechargeAmount')">
          <a-input-number :id="fieldId('minRechargeAmount')"
            :value="formState.minRechargeAmount" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'minRechargeAmount') }"
            :min="0.01"
            :precision="2"
            :step="0.01"
            style="width: 220px"
          />
          <div class="hint">限制用户每次充值的最低金额，默认 1 元，最多支持两位小数。</div>
        </a-form-item>

        <a-form-item label="开启短信登录" :html-for="fieldId('smsLoginEnabled')">
          <a-switch :id="fieldId('smsLoginEnabled')" :checked="formState.smsLoginEnabled" v-on="{ 'update:checked': settingsEntry.bind(updateSetting, 'smsLoginEnabled') }" />
        </a-form-item>

        <a-form-item label="开启微信登录" :html-for="fieldId('wechatLoginEnabled')">
          <a-switch :id="fieldId('wechatLoginEnabled')" :checked="formState.wechatLoginEnabled" v-on="{ 'update:checked': settingsEntry.bind(updateSetting, 'wechatLoginEnabled') }" />
          <div class="hint">总开关会随微信扫描或微信验证码方式自动生效；两个方式都关闭时会自动关闭。</div>
        </a-form-item>
        </ConsolePanel>

        <ConsolePanel title="商业化开关" description="两项默认关闭；关闭时保持现有行为，不产生赠金、不限制设备。" class="settings-section">
        <a-form-item label="启用充值阶梯赠送" :html-for="fieldId('rechargePromotionEnabled')">
          <a-switch :id="fieldId('rechargePromotionEnabled')" :checked="formState.rechargePromotionEnabled" v-on="{ 'update:checked': settingsEntry.bind(updateSetting, 'rechargePromotionEnabled') }" />
          <div class="hint">关闭（默认）：充值只按实付入账，不产生任何赠金与赠送流水。开启后才按六档门槛匹配赠比；方案本身仍未获批准上线。</div>
        </a-form-item>

        <a-form-item label="启用账户设备池上限" :html-for="fieldId('deviceLimitEnabled')">
          <a-switch :id="fieldId('deviceLimitEnabled')" :checked="formState.deviceLimitEnabled" v-on="{ 'update:checked': settingsEntry.bind(updateSetting, 'deviceLimitEnabled') }" />
          <div class="hint">关闭（默认）：不登记设备、不校验安装，所有有效 Key 按原逻辑使用。开启后才按等级上限（1/2/3/5/10/20）拒绝新登记。</div>
        </a-form-item>
        </ConsolePanel>

        <ConsolePanel title="黑名单" description="请求进站即拦截，先于登录鉴权。默认关闭。" class="settings-section">
        <a-form-item label="启用黑名单" :html-for="fieldId('blacklistEnabled')">
          <a-switch :id="fieldId('blacklistEnabled')" :checked="formState.blacklistEnabled" v-on="{ 'update:checked': settingsEntry.bind(updateSetting, 'blacklistEnabled') }" />
          <div class="hint">关闭（默认）：完全不做黑名单查询，不拦任何请求，也不写命中日志。开启后才按 IP / 手机号 / 微信 / 地区四类规则拦截。</div>
        </a-form-item>
        <div class="hint">黑名单页也能切换同一个开关，两处读写的是同一个配置（system_settings.blacklist_enabled），改任意一处另一处刷新后即同步。</div>
        </ConsolePanel>

        <ConsolePanel title="敏感词检测" description="只给日志打标记，不改变答题结果。默认关闭。" class="settings-section">
        <a-form-item label="启用敏感词检测" :html-for="fieldId('sensitiveWordCheckEnabled')">
          <a-switch :id="fieldId('sensitiveWordCheckEnabled')" :checked="formState.sensitiveWordCheckEnabled" v-on="{ 'update:checked': settingsEntry.bind(updateSetting, 'sensitiveWordCheckEnabled') }" />
          <div class="hint">关闭（默认）：完全不调用检测接口，不写任何敏感词字段。开启后，答题照常返回，题目与答案在后台异步检测并打标记；检测失败、超时或上游报错都不会影响答题。</div>
        </a-form-item>
        <a-alert
          v-if="formState.sensitiveWordCheckEnabled && !formState.sensitiveWordCheckConfigured"
          type="warning"
          show-icon
          message="已启用但未配置 UApiPro 密钥"
          description="检测能力复用上面的「UApiPro API 密钥」。未配置有效密钥（需以 uapi- 开头）时不会外呼，日志也不会出现敏感词标记。"
        />
        </ConsolePanel>

        <ConsolePanel title="微信登录与验证码" description="配置微信验证方式和验证码服务。" class="settings-section">
        <a-form-item label="微信扫描" :html-for="fieldId('wechatScanLoginEnabled')">
          <a-switch :id="fieldId('wechatScanLoginEnabled')" :checked="formState.wechatScanLoginEnabled" v-on="{ 'update:checked': settingsEntry.bind(updateSetting, 'wechatScanLoginEnabled') }" />
        </a-form-item>

        <a-form-item label="微信验证码" :html-for="fieldId('wechatCodeLoginEnabled')">
          <a-switch :id="fieldId('wechatCodeLoginEnabled')" :checked="formState.wechatCodeLoginEnabled" v-on="{ 'update:checked': settingsEntry.bind(updateSetting, 'wechatCodeLoginEnabled') }" />
        </a-form-item>

        <a-form-item label="默认微信方式" :html-for="fieldId('wechatDefaultLoginMode')">
          <a-select :id="fieldId('wechatDefaultLoginMode')" :value="formState.wechatDefaultLoginMode" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'wechatDefaultLoginMode') }">
            <a-select-option value="scan" :disabled="!formState.wechatScanLoginEnabled">微信扫描</a-select-option>
            <a-select-option value="code" :disabled="!formState.wechatCodeLoginEnabled">微信验证码</a-select-option>
          </a-select>
        </a-form-item>

        <a-form-item label="微信 API 地址" :html-for="fieldId('wechatApiBaseUrl')">
          <a-input :id="fieldId('wechatApiBaseUrl')" :value="formState.wechatApiBaseUrl" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'wechatApiBaseUrl') }" placeholder="例如 https://wechat.example.com" />
        </a-form-item>

        <a-form-item label="Bearer Token" :html-for="fieldId('wechatApiBearerToken')">
          <a-input-password :id="fieldId('wechatApiBearerToken')" :value="formState.wechatApiBearerToken" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'wechatApiBearerToken') }" placeholder="请输入微信登录服务 Bearer Token" />
        </a-form-item>

        <a-form-item label="验证码二维码" :html-for="fieldId('wechatCodeStaticQrcodeUrl')">
          <a-input :id="fieldId('wechatCodeStaticQrcodeUrl')" :value="formState.wechatCodeStaticQrcodeUrl" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'wechatCodeStaticQrcodeUrl') }" placeholder="请输入微信验证码模式下展示的静态二维码图片地址" />
          <!-- <div class="hint">微信验证码模式会在登录页展示该二维码，用户扫码关注后发送“验证码”获取登录码。</div> -->
        </a-form-item>

        <a-form-item label="验证码提供方" :html-for="fieldId('captchaProvider')">
          <a-select :id="fieldId('captchaProvider')" :value="formState.captchaProvider" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'captchaProvider') }">
            <a-select-option value="remote">远程 TAC 验证码</a-select-option>
            <a-select-option value="local">站内图片验证码</a-select-option>
          </a-select>
          <div class="hint">远程验证码不可用时，登录和注册页面会自动回退到站内图片验证码。</div>
        </a-form-item>

        <a-form-item label="远程服务地址" :html-for="fieldId('captchaApiBaseUrl')" v-if="useRemoteCaptcha">
          <a-input :id="fieldId('captchaApiBaseUrl')" :value="formState.captchaApiBaseUrl" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'captchaApiBaseUrl') }" placeholder="https://captcha.api.zelly.cn" />
        </a-form-item>

        <a-form-item label="远程服务 Key" :html-for="fieldId('captchaApiKey')" v-if="useRemoteCaptcha">
          <a-input-password :id="fieldId('captchaApiKey')" :value="formState.captchaApiKey" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'captchaApiKey') }" placeholder="请输入 X-Captcha-Key" />
        </a-form-item>

        <a-form-item label="验证码类型" :html-for="fieldId('captchaType')" v-if="useRemoteCaptcha">
          <a-select :id="fieldId('captchaType')" :value="formState.captchaType" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'captchaType') }">
            <a-select-option value="RANDOM">随机</a-select-option>
            <a-select-option value="SLIDER">滑块</a-select-option>
            <a-select-option value="ROTATE">旋转</a-select-option>
            <a-select-option value="CONCAT">滑动还原</a-select-option>
            <a-select-option value="WORD_IMAGE_CLICK">点选</a-select-option>
          </a-select>
        </a-form-item>
        </ConsolePanel>

        <ConsolePanel title="归属地查询" description="管理普通和商业增强归属地服务的访问密钥。" class="settings-section">
        <a-form-item label="UApiPro API 密钥" :html-for="fieldId('uapiApiKey')">
          <a-input-password :id="fieldId('uapiApiKey')" :value="formState.uapiApiKey" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'uapiApiKey') }" placeholder="请输入 UApiPro API 密钥（以 uapi- 开头）" />
          <div class="hint">
            用于「用户管理」「日志列表」展示手机号 / IP 归属地。留空时仍可使用普通 IP 和手机号查询，商业增强 IP 查询需要配置密钥。
          </div>
        </a-form-item>
        </ConsolePanel>

        <ConsolePanel title="支付回调" description="配置支付渠道、商户密钥和结算回调地址。" class="settings-section">
        <a-form-item label="接口版本" :html-for="fieldId('epayVersion')">
          <a-input :id="fieldId('epayVersion')" value="v2" disabled />
          <div class="hint">当前仅兼容易支付 v2；支付方式请在本页下方维护。</div>
        </a-form-item>

        <a-form-item label="网关地址" :html-for="fieldId('epayGatewayUrl')">
          <a-input :id="fieldId('epayGatewayUrl')" :value="formState.epayGatewayUrl" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'epayGatewayUrl') }" placeholder="例如 https://pay.example.com/submit.php" />
        </a-form-item>

        <a-form-item label="商户编号" :html-for="fieldId('epayPartnerId')">
          <a-input :id="fieldId('epayPartnerId')" :value="formState.epayPartnerId" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'epayPartnerId') }" placeholder="请输入易支付商户编号" />
        </a-form-item>

        <a-form-item label="平台公钥" :html-for="fieldId('epayPlatformPublicKey')">
          <a-input-password :id="fieldId('epayPlatformPublicKey')" :value="formState.epayPlatformPublicKey" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'epayPlatformPublicKey') }" placeholder="请输入易支付平台公钥" />
        </a-form-item>

        <a-form-item label="商户私钥" :html-for="fieldId('epayMerchantPrivateKey')">
          <a-input-password :id="fieldId('epayMerchantPrivateKey')" :value="formState.epayMerchantPrivateKey" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'epayMerchantPrivateKey') }" placeholder="请输入易支付商户私钥" />
        </a-form-item>

        <a-form-item label="网站 Base 地址" :html-for="fieldId('siteBaseUrl')">
          <a-space direction="vertical" style="width: 100%" :size="8">
            <a-space class="base-url-controls" style="width: 100%" wrap>
              <a-input :id="fieldId('siteBaseUrl')" :value="formState.siteBaseUrl" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'siteBaseUrl') }" placeholder="例如 https://app.example.com" class="base-url-input" />
              <a-button :onClick="settingsEntry.bind(fillBaseUrlFromBrowser)">从浏览器地址栏获取</a-button>
            </a-space>
            <div class="hint">只需要维护当前站点的 base 地址，系统会按易支付 SDK 所需参数自动补全异步通知与同步跳转地址。</div>
          </a-space>
        </a-form-item>

        <a-form-item label="异步通知地址" :html-for="fieldId('derivedNotifyUrl')">
          <a-input :id="fieldId('derivedNotifyUrl')" :value="derivedNotifyUrl" readonly placeholder="将根据网站 Base 地址自动补全" />
          <div class="hint">固定补全为 /adapter-service/payment/notify，并作为 notify_url 参与易支付下单。</div>
        </a-form-item>

        <a-form-item label="同步跳转地址" :html-for="fieldId('derivedReturnUrl')">
          <a-input :id="fieldId('derivedReturnUrl')" :value="derivedReturnUrl" readonly placeholder="将根据网站 Base 地址自动补全" />
          <div class="hint">固定补全为 /adapter/recharge/result，并作为 return_url 参与易支付下单。</div>
        </a-form-item>
        </ConsolePanel>

        <ConsolePanel title="短信验证码配置" description="管理短信宝账号、发送模式和验证码模板。" class="settings-section">
        <a-form-item label="短信提供方" :html-for="fieldId('smsProvider')">
          <a-input :id="fieldId('smsProvider')" :value="formState.smsProvider" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'smsProvider') }" disabled />
          <!-- <div class="hint">首期固定使用短信宝，后续如有需要再扩展多供应商。</div> -->
        </a-form-item>

        <a-form-item label="短信宝用户名" :html-for="fieldId('smsbaoUsername')">
          <a-input :id="fieldId('smsbaoUsername')" :value="formState.smsbaoUsername" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'smsbaoUsername') }" placeholder="请输入短信宝用户名" />
        </a-form-item>

        <a-form-item label="短信宝密钥" :html-for="fieldId('smsbaoSecret')">
          <a-input-password :id="fieldId('smsbaoSecret')" :value="formState.smsbaoSecret" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'smsbaoSecret') }" placeholder="请输入密码或 ApiKey" />
        </a-form-item>

        <a-form-item label="凭证类型" :html-for="fieldId('smsbaoCredentialType')">
          <a-select :id="fieldId('smsbaoCredentialType')" :value="formState.smsbaoCredentialType" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'smsbaoCredentialType') }">
            <a-select-option value="apikey">ApiKey，原样发送</a-select-option>
            <a-select-option value="password">登录密码，发送前 MD5</a-select-option>
          </a-select>
        </a-form-item>

        <a-form-item label="发送区域" :html-for="fieldId('smsbaoRegionMode')">
          <a-select :id="fieldId('smsbaoRegionMode')" :value="formState.smsbaoRegionMode" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'smsbaoRegionMode') }">
            <a-select-option value="auto">自动识别</a-select-option>
            <a-select-option value="domestic">仅国内</a-select-option>
            <a-select-option value="international">仅国际</a-select-option>
          </a-select>
        </a-form-item>

        <a-form-item label="短信签名" :html-for="fieldId('smsCodeSignName')">
          <a-input :id="fieldId('smsCodeSignName')" :value="formState.smsCodeSignName" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'smsCodeSignName') }" placeholder="例如 题库助手" />
        </a-form-item>

        <a-form-item label="验证码模板" :html-for="fieldId('smsCodeTemplate')">
          <a-textarea :id="fieldId('smsCodeTemplate')" :value="formState.smsCodeTemplate" v-on="{ 'update:value': settingsEntry.bind(updateSetting, 'smsCodeTemplate') }" :rows="3" placeholder="例如 【{sign}】您的验证码是{code}，5分钟内有效。" />
          <div class="hint">请保留 {sign} 和 {code} 占位符，系统会在发送前替换。</div>
        </a-form-item>
        </ConsolePanel>

      </a-form>

    <section v-if="paymentListFailure" class="page-alert" data-payment-method-list-feedback>
      <RequestFailureAlert :key="paymentListFailureContextKey" :failure="paymentListFailure" :message="paymentMethodsError" :busy="paymentMethodLoading" :context-key="paymentListFailureContextKey" :is-current="isCurrentPaymentListFailure" />
    </section>
    <a-alert v-else-if="paymentMethodsError" type="error" show-icon :message="paymentMethodsError" />
    <section v-if="paymentDeleteFailure" class="page-alert" data-payment-method-delete-feedback>
      <RequestFailureAlert :key="paymentDeleteFailureContextKey" :failure="paymentDeleteFailure" message="删除支付方式失败，请重试。" :busy="paymentMethodDeleting !== null" :context-key="paymentDeleteFailureContextKey" :is-current="isCurrentPaymentDeleteFailure" />
    </section>
    <PaymentMethodPanel
      :key="settingsFormKey"
      :entry="paymentPanelEntry"
      :disabled="!canManageSettings() || settingsDraining || !paymentMethodsLoaded || paymentMethodLoading || paymentMethodDeleting !== null"
      :rows="paymentMethods"
      :columns="paymentMethodColumns"
      :loading="paymentMethodLoading"
      :loaded="paymentMethodsLoaded"
      :error="paymentMethodsError"
      :onRefresh="paymentPanelEntry.bind(handleLoadPaymentMethods)"
      :onCreate="paymentPanelEntry.bind(openCreatePaymentMethodModal)"
      :onEdit="paymentPanelEntry.bind(openEditPaymentMethodModal)"
      :onDelete="paymentPanelEntry.bind(handleDeletePaymentMethod)"
    />

    <a-modal
      v-if="paymentMethodMounted" :key="paymentMethodKey"
      :open="paymentMethodVisible"
      :wrap-props="{ 'aria-modal': true }"
      :focusTriggerAfterClose="false"
      :onCancel="paymentDialogEntry.bind(closePaymentMethodModal)"
      :title="paymentMethodModalTitle"
      :onOk="paymentDialogEntry.bind(handleSubmitPaymentMethod)"
      :afterClose="paymentMethodAfterClose"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="paymentMethodSubmitting"
      width="760px"
    >
      <section v-if="paymentSaveFailure" class="page-alert" data-payment-method-save-feedback>
        <RequestFailureAlert :key="paymentSaveFailureContextKey" :failure="paymentSaveFailure" :message="paymentSaveFailureText" :busy="paymentMethodSubmitting" :context-key="paymentSaveFailureContextKey" :is-current="isCurrentPaymentSaveFailure" />
      </section>
      <a-form ref="paymentMethodForm" :onInputCapture="paymentDialogEntry.guardNative" :onChangeCapture="paymentDialogEntry.guardNative" :onClickCapture="paymentDialogEntry.guardNative" :disabled="paymentMethodSubmitting" :model="paymentMethodFormState" :label-col="paymentMethodLabelCol" :wrapper-col="paymentMethodWrapperCol" layout="horizontal">
        <a-form-item label="支付方式名称" required data-payment-method-field="name" :validate-status="paymentMethodFieldErrors.name ? 'error' : undefined" :help="paymentMethodFieldErrors.name || undefined">
          <a-input :value="paymentMethodFormState.name" v-on="{ 'update:value': paymentDialogEntry.bind(updatePaymentField, 'name') }" placeholder="例如 支付宝" />
        </a-form-item>
        <a-form-item label="SVG 图标">
          <PaymentMethodIconPicker :selected-id="paymentMethodIconCatalogId" :value="paymentMethodFormState.iconSVG"
            :disabled="paymentMethodSubmitting" :onSelect="paymentDialogEntry.bind(handlePaymentIconSelect)" />
          <a-textarea :value="paymentMethodFormState.iconSVG" v-on="{ 'update:value': paymentDialogEntry.bind(updatePaymentField, 'iconSVG') }" :onChange="paymentDialogEntry.bind(handlePaymentIconTextChange)" :rows="4" placeholder="可选，直接粘贴 SVG 源码" />
          <div class="icon-preview" aria-label="支付图标安全预览"><SafePaymentIcon :value="paymentMethodFormState.iconSVG" placeholder="¥" /></div>
          <div v-if="!isSafePaymentIcon(paymentMethodFormState.iconSVG)" class="hint" role="status">图标不受支持，请重新选择内置图标或使用静态 SVG；原值未改写。</div>
          <div class="hint">支持直接粘贴内联 SVG，用于充值选择弹窗与管理页预览。</div>
        </a-form-item>
        <a-form-item label="易支付通道值" required data-payment-method-field="ePayType" :validate-status="paymentMethodFieldErrors.ePayType ? 'error' : undefined" :help="paymentMethodFieldErrors.ePayType || undefined">
          <a-input :value="paymentMethodFormState.ePayType" v-on="{ 'update:value': paymentDialogEntry.bind(updatePaymentField, 'ePayType') }" placeholder="例如 alipay / wxpay" />
        </a-form-item>
        <a-form-item label="启用状态">
          <a-switch :checked="paymentMethodFormState.enabled" v-on="{ 'update:checked': paymentDialogEntry.bind(updatePaymentField, 'enabled') }" />
        </a-form-item>
        <a-form-item label="排序">
          <a-input-number :value="paymentMethodFormState.sort" v-on="{ 'update:value': paymentDialogEntry.bind(updatePaymentField, 'sort') }" :min="0" :step="1" style="width: 100%" />
        </a-form-item>
        <a-form-item label="备注">
          <a-textarea :value="paymentMethodFormState.remark" v-on="{ 'update:value': paymentDialogEntry.bind(updatePaymentField, 'remark') }" :rows="3" placeholder="可选，例如推荐场景或渠道说明" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script>
import { computed, defineComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue';
import { message, Modal } from 'ant-design-vue';
import RequestFailureAlert from '@/components/RequestFailureAlert.vue';
import { inheritTransportRequestId } from '@/utils/requestHelpers.mjs';
import { inheritRequestFailureView } from '@/utils/requestFailureView.mjs';
import { useCurrentUserDisplay, isAdminUser, getCurrentUserLogoutEpoch } from '@/utils/currentUser'
import { useUnsavedDraft } from '@/utils/useUnsavedDraft'
import PaymentMethodPanel from './PaymentMethodPanel.vue';
import SystemMetricsPanel from './SystemMetricsPanel.vue';
import {
  createPaymentMethod,
  deletePaymentMethod,
  getPaymentMethods,
  getSystemSettings,
  updatePaymentMethod,
  updateSystemSettings,
} from '@/api/TikuComponentApi';
import {
  normalizePaymentMethodForm,
  sortPaymentMethodsForDisplay,
} from '@/utils/paymentMethodHelpers.mjs';

import SafePaymentIcon from '@/components/SafePaymentIcon.js'
import PaymentMethodIconPicker from './components/PaymentMethodIconPicker.vue'
import { isSafePaymentIcon, PAYMENT_ICON_ERROR } from '@/utils/safePaymentIcon.mjs'
import { serializePaymentIcon } from '@/utils/paymentIconCatalog'

const notifyPath = '/adapter-service/payment/notify'
const returnPath = '/adapter/recharge/result'
const DEFAULT_MAX_LOG_SIZE_BYTES = 5 * 1024 * 1024 * 1024
const DEFAULT_MIN_RECHARGE_AMOUNT = 1
const MIN_LOG_SIZE_BYTES = 1 * 1024 * 1024
const LOG_SIZE_UNITS = [
  { value: 'B', label: '字节', bytes: 1 },
  { value: 'KB', label: 'KB', bytes: 1024 },
  { value: 'MB', label: 'MB', bytes: 1024 * 1024 },
  { value: 'GB', label: 'GB', bytes: 1024 * 1024 * 1024 },
  { value: 'TB', label: 'TB', bytes: 1024 * 1024 * 1024 * 1024 },
]
const LOG_SIZE_UNIT_MAP = Object.fromEntries(LOG_SIZE_UNITS.map(unit => [unit.value, unit]))

function normalizeString(value) {
  return String(value || '').trim()
}

function normalizeBonusAmount(value) {
  const amount = Number(value || 0)
  // 与后端保持一致：金额最多 6 位小数。
  return Number.isFinite(amount) && amount >= 0 ? Math.round(amount * 1e6) / 1e6 : 0
}

function normalizeMinRechargeAmount(value) {
  const raw = String(value ?? '').trim()
  if (!raw) {
    return DEFAULT_MIN_RECHARGE_AMOUNT
  }
  const amount = Number(raw)
  return Number.isFinite(amount) && amount > 0 && amount <= Number.MAX_SAFE_INTEGER
    ? Math.round(amount * 100) / 100
    : DEFAULT_MIN_RECHARGE_AMOUNT
}

function parseIntegerInput(value) {
  if (value === null || value === undefined || value === '') {
    return ''
  }
  return String(value).replace(/[^0-9]/g, '')
}

function normalizeMaxLogSizeBytes(value) {
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed >= MIN_LOG_SIZE_BYTES
    ? parsed
    : DEFAULT_MAX_LOG_SIZE_BYTES
}

function splitMaxLogSizeBytes(value) {
  const bytes = normalizeMaxLogSizeBytes(value)
  for (let index = LOG_SIZE_UNITS.length - 1; index >= 0; index -= 1) {
    const unit = LOG_SIZE_UNITS[index]
    if (bytes >= unit.bytes && bytes % unit.bytes === 0) {
      return { value: bytes / unit.bytes, unit: unit.value }
    }
  }
  return { value: bytes, unit: 'B' }
}

function composeMaxLogSizeBytes(value, unitValue) {
  const amount = Number(value)
  const unit = LOG_SIZE_UNIT_MAP[unitValue] || LOG_SIZE_UNIT_MAP.GB
  if (!Number.isSafeInteger(amount) || amount < 0 || !Number.isSafeInteger(amount * unit.bytes)) {
    return 0
  }
  return amount * unit.bytes
}

function trimTrailingSlash(value) {
  return normalizeString(value).replace(/\/+$/, '')
}

function buildAbsoluteURL(baseUrl, path) {
  const normalizedBase = trimTrailingSlash(baseUrl)
  if (!normalizedBase) {
    return ''
  }
  return `${normalizedBase}${path}`
}

function inferSiteBaseURL(detail = {}) {
  const candidates = [detail.epayNotifyUrl, detail.epayReturnUrl]
  for (const candidate of candidates) {
    const normalized = normalizeString(candidate)
    if (!normalized) {
      continue
    }
    if (normalized.endsWith(notifyPath)) {
      return normalized.slice(0, -notifyPath.length)
    }
    if (normalized.endsWith(returnPath)) {
      return normalized.slice(0, -returnPath.length)
    }
    try {
      const parsed = new URL(normalized)
      return parsed.origin
    } catch (_) {
      continue
    }
  }
  return ''
}

// A diagnostic must not retain the PUT config, secret form, or submitted snapshot.
const projectSettingsSaveFailure = source => {
  const carrier = Object.freeze({})
  inheritTransportRequestId(carrier, source)
  return inheritRequestFailureView(carrier, source)
}

export default defineComponent({
  name: 'SystemSettings',
  components: { PaymentMethodPanel, SafePaymentIcon, PaymentMethodIconPicker, SystemMetricsPanel, RequestFailureAlert },
  setup() {
    const settingsFormId = `system-settings-${useId()}`
    const fieldId = field => `${settingsFormId}-${field}`

    const defaults = () => ({
      siteName: '题库',
      maxLogSizeValue: DEFAULT_MAX_LOG_SIZE_BYTES / LOG_SIZE_UNIT_MAP.GB.bytes,
      maxLogSizeUnit: 'GB',
      registerEnabled: false,
      newUserBonusAmount: 0,
      minRechargeAmount: DEFAULT_MIN_RECHARGE_AMOUNT,
      // 商业化开关默认关闭，与后端缺省一致。
      rechargePromotionEnabled: false,
      deviceLimitEnabled: false,
      // 黑名单总开关默认关闭，与后端缺省一致（46-黑名单设计.md 红线：
      // blacklist_enabled 读不到一律按 false，不能让配置读错变成开始拦人）。
      blacklistEnabled: false,
      // 敏感词检测默认关闭，与后端缺省一致。
      sensitiveWordCheckEnabled: false,
      // 只读派生值：开关开着但没有可用 UAPI 密钥时为 false，页面据此提示。
      sensitiveWordCheckConfigured: false,
      epayVersion: 'v2',
      epayGatewayUrl: '',
      epayPartnerId: '',
      epayPlatformPublicKey: '',
      epayMerchantPrivateKey: '',
      siteBaseUrl: '',
      epayNotifyUrl: '',
      epayReturnUrl: '',
      smsProvider: 'smsbao',
      smsbaoUsername: '',
      smsbaoSecret: '',
      smsCodeSignName: '',
      smsCodeTemplate: '',
      smsLoginEnabled: false,
      wechatLoginEnabled: false,
      wechatScanLoginEnabled: false,
      wechatCodeLoginEnabled: false,
      wechatDefaultLoginMode: 'scan',
      wechatCodeStaticQrcodeUrl: '',
      wechatApiBaseUrl: '',
      wechatApiBearerToken: '',
      captchaProvider: 'remote',
      captchaApiBaseUrl: 'https://captcha.api.zelly.cn',
      captchaApiKey: '',
      captchaType: 'RANDOM',
      smsbaoCredentialType: 'apikey',
      smsbaoRegionMode: 'auto',
      uapiApiKey: '',
    })
    const emptyPaymentMethod = () => ({ id: undefined, name: '', iconSVG: '', ePayType: '', enabled: true, sort: 0, remark: '' })
    const tabLoading = ref(false)
    const saving = ref(false)
    const settingsLoaded = ref(false)
    const settingsError = ref('')
    const paymentMethodLoading = ref(false)
    const paymentMethodsLoaded = ref(false)
    const paymentMethodsError = ref('')
    const paymentMethodVisible = ref(false)
    const paymentMethodMounted = ref(false)
    const paymentMethodKey = ref(0)
    const paymentMethodSubmitting = ref(false)
    const paymentMethodEditing = ref(false)
    const paymentMethods = ref([])
    const paymentMethodDeleting = ref(null)
    const formState = reactive(defaults())
    const paymentMethodIconCatalogId = ref(null)
    const paymentMethodFormState = ref(emptyPaymentMethod())
    const settingsForm = ref(null)
    const paymentMethodForm = ref(null)
    const settingsFormMounted = ref(true)
    const settingsFormKey = ref(0)
    const settingsDraining = ref(false)
    const { currentUser } = useCurrentUserDisplay()
    let authorizedLogoutEpoch = getCurrentUserLogoutEpoch()
    // Private settings need a confirmed, stable server user id. This display
    // freshness gate never replaces the existing server authorization policy.
    const principal = () => {
      const id = currentUser.value?.id
      if (typeof id !== 'number' && (typeof id !== 'string' || !/^[0-9]+$/.test(id.trim()))) return ''
      const value = Number(id)
      return Number.isSafeInteger(value) && value > 0 && value <= 2147483647 ? `id:${value}` : ''
    }
    const role = () => String(currentUser.value?.role || '').trim().toLowerCase()
    let alive = true
    let mounted = false
    let epoch = 0
    let settingsRead = 0
    let paymentRead = 0
    let settingsWrite = 0
    let paymentWrite = 0
    let paymentDataVersion = 0
    let settingsRenderVersion = 0
    let dialogOwner = null
    let dialogDrain = Promise.resolve()
    let refreshPrompt = null
    let dialogPrompt = null
    const canManageSettings = () => alive && getCurrentUserLogoutEpoch() === authorizedLogoutEpoch && Boolean(principal()) && isAdminUser(currentUser.value)
    const captureOwner = () => {
      const ticket = epoch, identity = principal(), privilege = role()
      return () => alive && ticket === epoch && identity === principal() && privilege === role() && canManageSettings()
    }
    const settingsSaveFailure = shallowRef(null)
    const settingsSaveFailureContextKey = ref(0)
    let settingsSaveFailureOwner = () => false
    const clearSettingsSaveFailure = () => {
      settingsSaveFailure.value = null
      settingsSaveFailureOwner = () => false
      settingsSaveFailureContextKey.value += 1
    }
    // This factory captures authority and scalar revisions, never a save activation.
    const captureSettingsSaveFailureOwner = (owns, ticket) => {
      const context = settingsSaveFailureContextKey.value
      return () => owns() && ticket === settingsWrite && context === settingsSaveFailureContextKey.value
    }
    const showSettingsSaveFailure = (carrier, owns) => {
      if (!carrier || !owns()) return
      settingsSaveFailureOwner = owns
      settingsSaveFailure.value = carrier
    }
    const isCurrentSettingsSaveFailure = (failure, context) => settingsSaveFailureOwner() && Boolean(failure)
      && failure === settingsSaveFailure.value && context === settingsSaveFailureContextKey.value
    const settingsReadFailure = shallowRef(null)
    const settingsReadFailureContextKey = ref(0)
    let settingsReadFailureOwner = () => false
    const clearSettingsReadFailure = () => {
      settingsReadFailure.value = null
      settingsReadFailureOwner = () => false
      settingsReadFailureContextKey.value += 1
    }
    const captureSettingsReadFailureOwner = owns => {
      const context = settingsReadFailureContextKey.value
      // A completed read's trace is not an inflight ticket also superseded by Save.
      return () => owns() && context === settingsReadFailureContextKey.value
    }
    const showSettingsReadFailure = (carrier, owns) => {
      if (!carrier || !owns()) return
      settingsReadFailureOwner = owns
      settingsReadFailure.value = carrier
    }
    const isCurrentSettingsReadFailure = (failure, context) => settingsReadFailureOwner() && Boolean(failure)
      && failure === settingsReadFailure.value && context === settingsReadFailureContextKey.value
    const paymentSaveFailure = shallowRef(null)
    const paymentSaveFailureContextKey = ref(0)
    const paymentSaveFailureText = ref('')
    let paymentSaveFailureOwner = () => false
    const clearPaymentSaveFailure = () => {
      paymentSaveFailure.value = null
      paymentSaveFailureText.value = ''
      paymentSaveFailureOwner = () => false
      paymentSaveFailureContextKey.value += 1
    }
    const capturePaymentSaveFailureOwner = ticket => {
      // Never retain dialogOwner/isCurrent: that operation holds snapshot and opener.
      const owns = captureOwner(), key = paymentMethodKey.value
      const context = paymentSaveFailureContextKey.value
      return () => owns() && paymentMethodVisible.value && key === paymentMethodKey.value
        && ticket === paymentWrite && context === paymentSaveFailureContextKey.value
    }
    const showPaymentSaveFailure = (carrier, owns, editing) => {
      if (!carrier || !owns()) return
      paymentSaveFailureOwner = owns
      paymentSaveFailureText.value = editing
        ? '更新支付方式失败，未保存的更改仍保留。'
        : '创建支付方式失败，未保存的更改仍保留。'
      paymentSaveFailure.value = carrier
    }
    const isCurrentPaymentSaveFailure = (failure, context) => paymentSaveFailureOwner() && Boolean(failure)
      && failure === paymentSaveFailure.value && context === paymentSaveFailureContextKey.value
    const paymentListFailure = shallowRef(null)
    const paymentListFailureContextKey = ref(0)
    let paymentListFailureOwner = () => false
    const clearPaymentListFailure = () => {
      paymentListFailure.value = null
      paymentListFailureOwner = () => false
      paymentListFailureContextKey.value += 1
    }
    const capturePaymentListFailureOwner = (owns, ticket) => {
      const context = paymentListFailureContextKey.value
      // Completed-read feedback does not retain a loading/panel/row/operation lease.
      return () => owns() && ticket === paymentRead && context === paymentListFailureContextKey.value
    }
    const showPaymentListFailure = (carrier, owns) => {
      if (!carrier || !owns()) return
      paymentListFailureOwner = owns
      paymentListFailure.value = carrier
    }
    const isCurrentPaymentListFailure = (failure, context) => paymentListFailureOwner() && Boolean(failure)
      && failure === paymentListFailure.value && context === paymentListFailureContextKey.value
    const paymentDeleteFailure = shallowRef(null)
    const paymentDeleteFailureContextKey = ref(0)
    let paymentDeleteFailureOwner = () => false
    const clearPaymentDeleteFailure = () => {
      paymentDeleteFailure.value = null
      paymentDeleteFailureOwner = () => false
      paymentDeleteFailureContextKey.value += 1
    }
    const capturePaymentDeleteFailureOwner = (owns, version) => {
      const context = paymentDeleteFailureContextKey.value
      // A completed failure's copy outlives busy and never retains a Popup/row/operation.
      return () => owns() && version === paymentDataVersion
        && context === paymentDeleteFailureContextKey.value
    }
    const showPaymentDeleteFailure = (carrier, owns) => {
      if (!carrier || !owns()) return
      paymentDeleteFailureOwner = owns
      paymentDeleteFailure.value = carrier
    }
    const isCurrentPaymentDeleteFailure = (failure, context) => paymentDeleteFailureOwner() && Boolean(failure)
      && failure === paymentDeleteFailure.value && context === paymentDeleteFailureContextKey.value
    const paymentMethodFieldErrors = reactive({ name: '', ePayType: '' })
    let paymentFieldFocusVersion = 0
    let paymentCloseFocusVersion = 0
    const clearPaymentMethodFieldErrors = () => {
      paymentMethodFieldErrors.name = ''; paymentMethodFieldErrors.ePayType = ''
      paymentFieldFocusVersion += 1
    }
    const showPaymentFieldError = (field, text) => {
      paymentMethodFieldErrors.name = field === 'name' ? text : ''
      paymentMethodFieldErrors.ePayType = field === 'ePayType' ? text : ''
      const owns = captureOwner(), key = paymentMethodKey.value, ticket = ++paymentFieldFocusVersion
      // Focus owns only safe principal/scalar context, never a dialog snapshot/opener.
      nextTick().then(() => nextTick()).then(() => {
        if (!owns() || !paymentMethodVisible.value || key !== paymentMethodKey.value
          || ticket !== paymentFieldFocusVersion || settingsDraining.value || paymentMethodSubmitting.value) return
        const element = paymentMethodForm.value?.$el || paymentMethodForm.value
        if (!(element instanceof HTMLElement) || !element.isConnected) return
        const input = element.querySelector(`[data-payment-method-field="${field}"] input`)
        if (input instanceof HTMLElement && !input.disabled) input.focus()
      })
    }
    const makeEntry = (extra = () => true) => {
      const owns = captureOwner()
      const isCurrent = () => owns() && !settingsDraining.value && extra()
      return {
        isCurrent,
        guardNative(event) {
          if (isCurrent()) return
          event.preventDefault(); event.stopImmediatePropagation()
        },
        bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined,
      }
    }
    const settingsEntry = shallowRef(makeEntry(() => settingsRenderVersion === 0))
    const paymentPanelEntry = shallowRef(makeEntry())
    const paymentDialogEntry = shallowRef(makeEntry(() => false))
    const paymentMethodAfterClose = shallowRef(null)
    const renewSettingsEntry = () => {
      const version = ++settingsRenderVersion
      settingsEntry.value = makeEntry(() => version === settingsRenderVersion)
    }
    const renewPaymentPanelEntry = () => {
      const version = paymentDataVersion
      paymentPanelEntry.value = makeEntry(() => version === paymentDataVersion && !paymentMethodLoading.value)
    }
    const draftState = reactive({ settings: formState, payment: computed(() => paymentMethodVisible.value ? paymentMethodFormState.value : null) })
    const { currentConfigSnapshot, savedConfigSnapshot, hasUnsavedChanges, requestRouteLeave } = useUnsavedDraft(draftState, { focusTriggerAfterClose: false })
    const markSettingsDraftSaved = (snapshot = JSON.stringify(formState)) => {
      const saved = JSON.parse(savedConfigSnapshot.value || '{}')
      saved.settings = JSON.parse(snapshot)
      savedConfigSnapshot.value = JSON.stringify(saved)
    }
    const markPaymentDraftSaved = () => {
      const saved = JSON.parse(savedConfigSnapshot.value || '{}')
      saved.payment = paymentMethodVisible.value ? { ...paymentMethodFormState.value } : null
      savedConfigSnapshot.value = JSON.stringify(saved)
    }
    const dialogDirty = () => paymentMethodVisible.value && JSON.stringify(paymentMethodFormState.value) !== dialogOwner?.snapshot
    const closeOwnedPrompt = (name) => {
      const prompt = name === 'refresh' ? refreshPrompt : dialogPrompt
      if (!prompt) return
      if (name === 'refresh') refreshPrompt = null
      else dialogPrompt = null
      prompt.resolve(false); prompt.modal?.destroy()
    }
    const confirmOwned = (name, title, owns) => {
      closeOwnedPrompt(name)
      if (!owns()) return Promise.resolve(false)
      return new Promise(resolve => {
        const prompt = { resolve, modal: null }
        if (name === 'refresh') refreshPrompt = prompt
        else dialogPrompt = prompt
        const settle = allow => {
          if ((name === 'refresh' ? refreshPrompt : dialogPrompt) !== prompt) return
          if (name === 'refresh') refreshPrompt = null
          else dialogPrompt = null
          resolve(Boolean(allow && owns())); prompt.modal?.destroy()
        }
        prompt.modal = Modal.confirm({ title, content: '放弃将清除当前未保存的编辑。', okText: '放弃更改', cancelText: '保留编辑',
          autoFocusButton: 'cancel', focusTriggerAfterClose: false, onOk: () => settle(true), onCancel: () => settle(false) })
      })
    }
    // Retire authority and private pixels now. Keep accepted Antd native-input refs
    // alive for two ticks before removing their owned subtree (pending setSelection).
    const hidePrivateForm = (form) => {
      const element = form?.$el || form
      if (!(element instanceof HTMLElement)) return
      const surface = element.closest('[role="dialog"]') || element
      surface.hidden = true; surface.style.display = 'none'
      for (const input of surface.querySelectorAll('input, textarea')) {
        input.value = ''
        if (input.tagName === 'INPUT' && input.type !== 'checkbox' && input.type !== 'radio') input.type = 'password'
      }
      if (surface.contains(document.activeElement)) document.activeElement.blur?.()
    }
    const retirePaymentDialog = () => {
      paymentCloseFocusVersion += 1
      clearPaymentMethodFieldErrors()
      clearPaymentSaveFailure()
      const owner = dialogOwner
      dialogOwner = null
      paymentWrite += 1
      paymentMethodSubmitting.value = false
      paymentDialogEntry.value = makeEntry(() => false)
      paymentMethodAfterClose.value = null
      closeOwnedPrompt('dialog')
      hidePrivateForm(paymentMethodForm.value)
      paymentMethodVisible.value = false
      paymentMethodIconCatalogId.value = null
      paymentMethodFormState.value = emptyPaymentMethod()
      paymentMethodEditing.value = false
      markPaymentDraftSaved()
      if (paymentMethodMounted.value) {
        dialogDrain = nextTick().then(() => nextTick()).then(() => {
          if (dialogOwner || (owner && paymentMethodKey.value !== owner.key)) return
          paymentMethodMounted.value = false
        }).then(() => nextTick())
      }
      return dialogDrain
    }
    // Only short-lived focus metadata: never a dialog snapshot, form or record.
    const makePaymentCloseFocus = (owns, opener, key, read, dataVersion, ticket) => () => {
      if (!owns() || settingsDraining.value || dialogOwner || paymentMethodMounted.value
        || paymentMethodVisible.value || key !== paymentMethodKey.value || read !== paymentRead
        || dataVersion !== paymentDataVersion || ticket !== paymentCloseFocusVersion) return
      if (!(opener instanceof HTMLElement) || !opener.isConnected || opener.tabIndex < 0
        || opener.matches(':disabled, [aria-disabled="true"]')) return
      const active = document.activeElement
      if (active !== document.body && active !== opener) return
      const isVisible = element => {
        for (let node = element; node; node = node.parentElement) {
          const style = window.getComputedStyle(node)
          if (node.hidden || node.inert || node.getAttribute('aria-hidden') === 'true'
            || style.display === 'none' || style.visibility === 'hidden') return false
        }
        return true
      }
      if (!isVisible(opener) || [...document.querySelectorAll('[role="dialog"]')].some(isVisible)) return
      if (active !== opener) opener.focus({ preventScroll: true })
    }
    const derivedNotifyUrl = computed(() => buildAbsoluteURL(formState.siteBaseUrl, notifyPath))
    const derivedReturnUrl = computed(() => buildAbsoluteURL(formState.siteBaseUrl, returnPath))
    const paymentMethodModalTitle = computed(() => (paymentMethodEditing.value ? '编辑支付方式' : '新增支付方式'))
    const minimumLogSizeValue = computed(() => {
      const unit = LOG_SIZE_UNIT_MAP[formState.maxLogSizeUnit] || LOG_SIZE_UNIT_MAP.GB
      return Math.ceil(MIN_LOG_SIZE_BYTES / unit.bytes)
    })
    const applyDetail = (data = {}) => {
      formState.siteName = data.siteName || '题库'
      const maxLogSize = splitMaxLogSizeBytes(data.maxLogSizeBytes)
      formState.maxLogSizeValue = maxLogSize.value
      formState.maxLogSizeUnit = maxLogSize.unit
      formState.registerEnabled = !!data.registerEnabled
      formState.newUserBonusAmount = Number(data.newUserBonusAmount || 0)
      formState.minRechargeAmount = normalizeMinRechargeAmount(data.minRechargeAmount)
      // 布尔开关只认显式 true；缺字段或异常值一律视为关闭。
      formState.rechargePromotionEnabled = data.rechargePromotionEnabled === true
      formState.deviceLimitEnabled = data.deviceLimitEnabled === true
      // 黑名单总开关：与其它布尔开关同规则 —— 只认显式 true。
      // 后端 resolveBoolSettingInput 对 nil 保持现值，这里读回的是落库后的真实值。
      formState.blacklistEnabled = data.blacklistEnabled === true
      formState.sensitiveWordCheckEnabled = data.sensitiveWordCheckEnabled === true
      formState.sensitiveWordCheckConfigured = data.sensitiveWordCheckConfigured === true
      formState.epayVersion = 'v2'
      formState.epayGatewayUrl = data.epayGatewayUrl || ''
      formState.epayPartnerId = data.epayPartnerId || ''
      formState.epayPlatformPublicKey = data.epayPlatformPublicKey || ''
      formState.epayMerchantPrivateKey = data.epayMerchantPrivateKey || ''
      formState.siteBaseUrl = inferSiteBaseURL(data)
      formState.epayNotifyUrl = data.epayNotifyUrl || ''
      formState.epayReturnUrl = data.epayReturnUrl || ''
      formState.smsProvider = data.smsProvider || 'smsbao'
      formState.smsbaoUsername = data.smsbaoUsername || ''
      formState.smsbaoSecret = data.smsbaoSecret || ''
      formState.smsCodeSignName = data.smsCodeSignName || ''
      formState.smsCodeTemplate = data.smsCodeTemplate || ''
      formState.smsLoginEnabled = !!data.smsLoginEnabled
      formState.wechatLoginEnabled = !!data.wechatLoginEnabled
      formState.wechatScanLoginEnabled = !!data.wechatScanLoginEnabled
      formState.wechatCodeLoginEnabled = !!data.wechatCodeLoginEnabled
      formState.wechatDefaultLoginMode = data.wechatDefaultLoginMode || (formState.wechatScanLoginEnabled ? 'scan' : 'code')
      formState.wechatCodeStaticQrcodeUrl = data.wechatCodeStaticQrcodeUrl || ''
      formState.wechatApiBaseUrl = data.wechatApiBaseUrl || ''
      formState.wechatApiBearerToken = data.wechatApiBearerToken || ''
      formState.captchaProvider = data.captchaProvider || 'remote'
      formState.captchaApiBaseUrl = data.captchaApiBaseUrl || 'https://captcha.api.zelly.cn'
      formState.captchaApiKey = data.captchaApiKey || ''
      formState.captchaType = data.captchaType || 'RANDOM'
      formState.smsbaoCredentialType = data.smsbaoCredentialType || 'apikey'
      formState.smsbaoRegionMode = data.smsbaoRegionMode || 'auto'
      // 明文回填（系统设置页仅管理员可见），密码框负责隐藏显示。
      formState.uapiApiKey = data.uapiApiKey || ''
    }


    const loadSettings = () => {
      if (!canManageSettings() || settingsDraining.value || saving.value) return
      const owns = captureOwner(), ticket = ++settingsRead
      const before = JSON.stringify(formState)
      clearSettingsSaveFailure()
      clearSettingsReadFailure()
      const ownsFailure = captureSettingsReadFailureOwner(owns)
      let failure = null
      tabLoading.value = true; settingsError.value = ''
      return Promise.resolve().then(() => {
        if (!owns()) return null
        return getSystemSettings({ errorFeedback: 'local' }).catch(error => {
          // Only this GET rejection projects metadata, not applyDetail/cache errors.
          if (owns() && ticket === settingsRead) failure = projectSettingsSaveFailure(error)
          throw error
        })
      }).then(res => {
        if (!owns() || ticket !== settingsRead) return
        if (res?.status !== 200 || !res.data || typeof res.data !== 'object' || Array.isArray(res.data)) {
          settingsError.value = '获取系统设置失败，请重试；未加载成功前不能保存。'
          showSettingsReadFailure(projectSettingsSaveFailure(res), ownsFailure)
          return
        }
        // A refresh may finish after this same admin has resumed typing. Keep the
        // newer draft instead of replacing it with the older server snapshot.
        if (JSON.stringify(formState) !== before) return
        applyDetail(res.data)
        settingsLoaded.value = true
        markSettingsDraftSaved()
        renewSettingsEntry()
      }).catch(() => {
        if (owns() && ticket === settingsRead) {
          settingsError.value = '获取系统设置失败，请重试；未加载成功前不能保存。'
          showSettingsReadFailure(failure, ownsFailure)
        }
      }).finally(() => { if (owns() && ticket === settingsRead) tabLoading.value = false })
    }
    const loadPaymentMethods = () => {
      if (!canManageSettings() || settingsDraining.value) return
      const owns = captureOwner(), ticket = ++paymentRead
      clearPaymentListFailure()
      const ownsFailure = capturePaymentListFailureOwner(owns, ticket)
      let failure = null
      clearPaymentDeleteFailure()
      paymentDataVersion += 1
      renewPaymentPanelEntry()
      paymentMethodLoading.value = true; paymentMethodsError.value = ''
      return Promise.resolve().then(() => {
        if (!owns()) return null
        return getPaymentMethods({ errorFeedback: 'local' }).catch(error => {
          // Only this GET rejection projects metadata, not sorting/local exceptions.
          if (owns() && ticket === paymentRead) failure = projectSettingsSaveFailure(error)
          throw error
        })
      }).then(res => {
        if (!owns() || ticket !== paymentRead) return
        if (res?.status !== 200 || !Array.isArray(res.data)) {
          paymentMethodsError.value = '获取支付方式失败，请重试。'
          showPaymentListFailure(projectSettingsSaveFailure(res), ownsFailure)
          return
        }
        paymentMethods.value = sortPaymentMethodsForDisplay(res.data)
        paymentMethodsLoaded.value = true
      }).catch(() => {
        if (owns() && ticket === paymentRead) {
          paymentMethodsError.value = '获取支付方式失败，请重试。'
          showPaymentListFailure(failure, ownsFailure)
        }
      }).finally(() => {
        if (!owns() || ticket !== paymentRead) return
        paymentMethodLoading.value = false; renewPaymentPanelEntry()
      })
    }
    const fillBaseUrlFromBrowser = () => {
      if (!canManageSettings() || settingsDraining.value || !settingsLoaded.value) return
      if (typeof window === 'undefined' || !window.location) { message.error('当前无法获取浏览器地址栏'); return }
      const siteBaseUrl = window.location.origin || ''
      if (formState.siteBaseUrl !== siteBaseUrl) clearSettingsSaveFailure()
      formState.siteBaseUrl = siteBaseUrl
      message.success('已从浏览器地址栏获取站点地址')
    }
    const updateSetting = (field, value) => {
      if (canManageSettings() && !settingsDraining.value && settingsLoaded.value && !saving.value) {
        if (formState[field] !== value) clearSettingsSaveFailure()
        formState[field] = value
      }
    }
    const updatePaymentField = (field, value) => {
      if (dialogOwner?.isCurrent() && !paymentMethodSubmitting.value) {
        if (paymentMethodFormState.value[field] !== value) {
          clearPaymentSaveFailure()
          paymentFieldFocusVersion += 1
          if (field === 'name' || field === 'ePayType') paymentMethodFieldErrors[field] = ''
        }
        paymentMethodFormState.value[field] = value
      }
    }
    const handleLoad = async () => {
      if (!canManageSettings() || settingsDraining.value || saving.value) return
      const owns = captureOwner()
      if (hasUnsavedChanges.value && !await confirmOwned('refresh', '有未保存的更改，确认刷新？', owns)) return
      if (!owns() || settingsDraining.value) return
      await retirePaymentDialog()
      if (!owns()) return
      loadSettings(); loadPaymentMethods()
    }
    const handleSave = () => {
      if (!canManageSettings() || settingsDraining.value || !settingsLoaded.value || tabLoading.value || saving.value) return

      const maxLogSizeBytes = composeMaxLogSizeBytes(formState.maxLogSizeValue, formState.maxLogSizeUnit)
      if (!Number.isSafeInteger(maxLogSizeBytes) || maxLogSizeBytes < MIN_LOG_SIZE_BYTES) {
        message.error('最大日志大小必须是不小于 1MB 的整数')
        return
      }
      const siteBaseUrl = trimTrailingSlash(formState.siteBaseUrl)
      const notifyUrl = buildAbsoluteURL(siteBaseUrl, notifyPath)
      const returnUrl = buildAbsoluteURL(siteBaseUrl, returnPath)
      const wechatScanLoginEnabled = !!formState.wechatScanLoginEnabled
      const wechatCodeLoginEnabled = !!formState.wechatCodeLoginEnabled
      const wechatLoginEnabled = !!formState.wechatLoginEnabled && (wechatScanLoginEnabled || wechatCodeLoginEnabled)
      let wechatDefaultLoginMode = normalizeString(formState.wechatDefaultLoginMode || 'scan')
      if (wechatDefaultLoginMode === 'code' && !wechatCodeLoginEnabled) {
        wechatDefaultLoginMode = wechatScanLoginEnabled ? 'scan' : 'code'
      }
      if (wechatDefaultLoginMode !== 'code' && !wechatScanLoginEnabled && wechatCodeLoginEnabled) {
        wechatDefaultLoginMode = 'code'
      }
      const minRechargeAmount = normalizeMinRechargeAmount(formState.minRechargeAmount)
      if (!Number.isFinite(minRechargeAmount) || minRechargeAmount <= 0) {
        message.error('充值最低金额必须是大于 0 的有效金额')
        return
      }
      const payload = {
        siteName: normalizeString(formState.siteName || '题库'),
        maxLogSizeBytes,
        registerEnabled: !!formState.registerEnabled,
        newUserBonusAmount: normalizeBonusAmount(formState.newUserBonusAmount),
        minRechargeAmount,
        epayVersion: 'v2',
        epayGatewayUrl: normalizeString(formState.epayGatewayUrl),
        epayPartnerId: normalizeString(formState.epayPartnerId),
        epayPlatformPublicKey: normalizeString(formState.epayPlatformPublicKey),
        epayMerchantPrivateKey: normalizeString(formState.epayMerchantPrivateKey),
        epayNotifyUrl: notifyUrl,
        epayReturnUrl: returnUrl,
        smsProvider: normalizeString(formState.smsProvider || 'smsbao'),
        smsbaoUsername: normalizeString(formState.smsbaoUsername),
        smsbaoSecret: normalizeString(formState.smsbaoSecret),
        smsCodeSignName: normalizeString(formState.smsCodeSignName),
        smsCodeTemplate: normalizeString(formState.smsCodeTemplate),
        smsLoginEnabled: !!formState.smsLoginEnabled,
        wechatLoginEnabled,
        wechatScanLoginEnabled,
        wechatCodeLoginEnabled,
        wechatDefaultLoginMode,
        wechatCodeStaticQrcodeUrl: normalizeString(formState.wechatCodeStaticQrcodeUrl),
        wechatApiBaseUrl: normalizeString(formState.wechatApiBaseUrl),
        wechatApiBearerToken: normalizeString(formState.wechatApiBearerToken),
        captchaProvider: normalizeString(formState.captchaProvider || 'remote'),
        captchaApiBaseUrl: normalizeString(formState.captchaApiBaseUrl || 'https://captcha.api.zelly.cn'),
        captchaApiKey: normalizeString(formState.captchaApiKey),
        captchaType: normalizeString(formState.captchaType || 'RANDOM'),
        smsbaoCredentialType: normalizeString(formState.smsbaoCredentialType || 'apikey'),
        smsbaoRegionMode: normalizeString(formState.smsbaoRegionMode || 'auto'),
        // 明文提交；留空即为清空密钥（不做脱敏占位符的特殊处理）。
        uapiApiKey: normalizeString(formState.uapiApiKey),
        // 显式提交布尔值：后端用指针区分「未提交」与「提交 false」。
        rechargePromotionEnabled: !!formState.rechargePromotionEnabled,
        deviceLimitEnabled: !!formState.deviceLimitEnabled,
        // 显式提交布尔值：后端用 *bool 区分「未提交」与「提交 false」。
        blacklistEnabled: !!formState.blacklistEnabled,
        sensitiveWordCheckEnabled: !!formState.sensitiveWordCheckEnabled,
      }

      const owns = captureOwner(), ticket = ++settingsWrite
      const snapshot = JSON.stringify(formState)
      clearSettingsSaveFailure()
      const ownsFailure = captureSettingsSaveFailureOwner(owns, ticket)
      let failure = null
      // Supersede older reads before writing a fully captured payload. An executed
      // request is not cancelled; only this page's stale display authority retires.
      settingsRead += 1; tabLoading.value = false; saving.value = true
      return Promise.resolve().then(() => {
        if (!owns()) return null
        return updateSystemSettings(payload, { errorFeedback: 'local' }).catch(error => {
          // Only this PUT rejection may project metadata; local processing errors may not.
          if (owns() && ticket === settingsWrite) failure = projectSettingsSaveFailure(error)
          throw error
        })
      }).then(res => {
        if (!owns() || ticket !== settingsWrite) return
        if (res?.status === 200) {
          message.success('保存成功')
          // Do not GET over a newer draft or read back secrets into a retired form.
          markSettingsDraftSaved(snapshot)
        } else {
          showSettingsSaveFailure(projectSettingsSaveFailure(res), ownsFailure)
          message.error('保存失败，请重试；未保存的更改仍保留。')
        }
      }).catch(() => {
        if (owns() && ticket === settingsWrite) {
          showSettingsSaveFailure(failure, ownsFailure)
          message.error('保存失败，请重试；未保存的更改仍保留。')
        }
      }).finally(() => { if (owns() && ticket === settingsWrite) saving.value = false })
    }
    const handleLoadPaymentMethods = () => loadPaymentMethods()
    const openPaymentMethodModal = async (record, event) => {
      if (!canManageSettings() || settingsDraining.value || !paymentMethodsLoaded.value) return
      const owns = captureOwner()
      if (dialogDirty() && !await confirmOwned('dialog', '有未保存的支付方式，确认放弃？', () => owns() && Boolean(dialogOwner?.isCurrent()))) return
      if (!owns()) return
      await retirePaymentDialog()
      if (!owns() || settingsDraining.value) return
      paymentMethodEditing.value = Boolean(record)
      paymentMethodFormState.value = record ? { id: record.id, name: record.name || '', iconSVG: record.iconSvg || '',
        ePayType: record.epayType || '', enabled: !!record.enabled, sort: Number(record.sort || 0), remark: record.remark || '' } : emptyPaymentMethod()
      const owner = { key: ++paymentMethodKey.value, owns, snapshot: JSON.stringify(paymentMethodFormState.value), opener: event?.currentTarget || document.activeElement }
      owner.isCurrent = () => owns() && dialogOwner === owner && paymentMethodVisible.value && owner.key === paymentMethodKey.value
      dialogOwner = owner
      paymentMethodMounted.value = true; paymentMethodVisible.value = true
      paymentDialogEntry.value = makeEntry(owner.isCurrent)
      paymentMethodAfterClose.value = () => {
        if (owns() && dialogOwner === owner && !paymentMethodVisible.value) retirePaymentDialog()
      }
      markPaymentDraftSaved()
    }
    const openCreatePaymentMethodModal = event => openPaymentMethodModal(null, event)
    const openEditPaymentMethodModal = (record, event) => openPaymentMethodModal(record, event)
    const closePaymentMethodModal = async () => {
      const owner = dialogOwner
      if (!owner?.isCurrent()) return
      paymentFieldFocusVersion += 1 // An owned dirty-close prompt must not lose focus to an older rejection.
      const dirty = dialogDirty(), restoreFocus = !dirty && !paymentMethodSubmitting.value
      if (dirty && !await confirmOwned('dialog', '有未保存的支付方式，确认关闭？', owner.isCurrent)) return
      if (!owner.isCurrent()) return
      if (!restoreFocus) { retirePaymentDialog(); return }
      const owns = owner.owns, opener = owner.opener, key = owner.key
      const read = paymentRead, dataVersion = paymentDataVersion
      const drain = retirePaymentDialog(), ticket = paymentCloseFocusVersion
      return drain.then(makePaymentCloseFocus(owns, opener, key, read, dataVersion, ticket))
    }
    const handlePaymentIconSelect = id => {
      if (!dialogOwner?.isCurrent() || paymentMethodSubmitting.value) return
      try {
        const svg = serializePaymentIcon(id)
        if (!isSafePaymentIcon(svg)) throw new Error(PAYMENT_ICON_ERROR)
        if (paymentMethodFormState.value.iconSVG !== svg) { clearPaymentSaveFailure(); paymentFieldFocusVersion += 1 }
        paymentMethodFormState.value.iconSVG = svg; paymentMethodIconCatalogId.value = id
      } catch { message.error(PAYMENT_ICON_ERROR) }
    }
    const handlePaymentIconTextChange = () => { if (dialogOwner?.isCurrent() && !paymentMethodSubmitting.value) paymentMethodIconCatalogId.value = null }
    const handleSubmitPaymentMethod = () => {
      const owner = dialogOwner
      if (!owner?.isCurrent() || paymentMethodSubmitting.value) return
      paymentFieldFocusVersion += 1
      if (!isSafePaymentIcon(paymentMethodFormState.value.iconSVG)) { message.warning(PAYMENT_ICON_ERROR); return }
      const payload = normalizePaymentMethodForm(paymentMethodFormState.value)
      if (!payload.name) { showPaymentFieldError('name', '请输入支付方式名称'); message.warning('请输入支付方式名称'); return }
      if (!payload.epayType) { showPaymentFieldError('ePayType', '请输入易支付通道值'); message.warning('请输入易支付通道值'); return }
      clearPaymentMethodFieldErrors()
      const editing = paymentMethodEditing.value, ticket = ++paymentWrite
      clearPaymentSaveFailure()
      const ownsFailure = capturePaymentSaveFailureOwner(ticket)
      let failure = null
      paymentMethodSubmitting.value = true
      return Promise.resolve().then(() => {
        if (!owner.isCurrent()) return null
        const request = editing
          ? updatePaymentMethod(payload, { errorFeedback: 'local' })
          : createPaymentMethod(payload, { errorFeedback: 'local' })
        return request.catch(error => {
          // Only an actual create/update API failure may project safe metadata.
          if (owner.isCurrent() && ticket === paymentWrite) failure = projectSettingsSaveFailure(error)
          throw error
        })
      }).then(res => {
        if (!owner.isCurrent() || ticket !== paymentWrite) return
        if (res?.status === 200) {
          message.success(editing ? '更新支付方式成功' : '创建支付方式成功')
          retirePaymentDialog(); loadPaymentMethods()
        } else {
          showPaymentSaveFailure(projectSettingsSaveFailure(res), ownsFailure, editing)
          message.error(editing ? '更新支付方式失败，未保存的更改仍保留。' : '创建支付方式失败，未保存的更改仍保留。')
        }
      }).catch(() => {
        if (owner.isCurrent() && ticket === paymentWrite) {
          showPaymentSaveFailure(failure, ownsFailure, editing)
          message.error(editing ? '更新支付方式失败，未保存的更改仍保留。' : '创建支付方式失败，未保存的更改仍保留。')
        }
      }).finally(() => { if (owner.isCurrent() && ticket === paymentWrite) paymentMethodSubmitting.value = false })
    }
    const handleDeletePaymentMethod = id => {
      if (!canManageSettings() || settingsDraining.value || !paymentMethodsLoaded.value || paymentMethodLoading.value || paymentMethodDeleting.value !== null) return
      if (!paymentMethods.value.some(row => row.id === id)) return
      const owns = captureOwner(), version = paymentDataVersion
      const isCurrent = () => owns() && version === paymentDataVersion
      clearPaymentDeleteFailure()
      const ownsFailure = capturePaymentDeleteFailureOwner(owns, version)
      let failure = null
      paymentMethodDeleting.value = id
      return Promise.resolve().then(() => {
        if (!isCurrent()) return null
        return deletePaymentMethod({ id }, { errorFeedback: 'local' }).catch(error => {
          // Only this DELETE rejection projects metadata, not list/reload exceptions.
          if (isCurrent()) failure = projectSettingsSaveFailure(error)
          throw error
        })
      }).then(res => {
        if (!isCurrent()) return
        if (res?.status === 200) { message.success('删除支付方式成功'); loadPaymentMethods() }
        else {
          showPaymentDeleteFailure(projectSettingsSaveFailure(res), ownsFailure)
          message.error('删除支付方式失败，请重试。')
        }
      }).catch(() => {
        if (isCurrent()) {
          showPaymentDeleteFailure(failure, ownsFailure)
          message.error('删除支付方式失败，请重试。')
        }
      })
        .finally(() => { if (owns() && paymentMethodDeleting.value === id) paymentMethodDeleting.value = null })
    }
    const retireScope = () => {
      clearPaymentListFailure()
      clearPaymentDeleteFailure()
      clearSettingsReadFailure()
      clearSettingsSaveFailure()
      epoch += 1; settingsRead += 1; paymentRead += 1; settingsWrite += 1; paymentDataVersion += 1
      settingsDraining.value = true
      hidePrivateForm(settingsForm.value)
      retirePaymentDialog()
      closeOwnedPrompt('refresh')
      requestRouteLeave({ path: '/login' }) // settles only useUnsavedDraft's owned prompt
      Object.assign(formState, defaults())
      paymentMethods.value = []; settingsLoaded.value = false; paymentMethodsLoaded.value = false
      tabLoading.value = false; saving.value = false; paymentMethodLoading.value = false; paymentMethodDeleting.value = null
      settingsError.value = ''; paymentMethodsError.value = ''
      savedConfigSnapshot.value = currentConfigSnapshot.value
      renewSettingsEntry(); renewPaymentPanelEntry()
      const ticket = epoch
      nextTick().then(() => nextTick()).then(async () => {
        if (!alive || ticket !== epoch) return
        settingsFormMounted.value = false
        await nextTick()
        if (!alive || ticket !== epoch) return
        settingsFormKey.value += 1; settingsFormMounted.value = true; settingsDraining.value = false
        renewSettingsEntry(); renewPaymentPanelEntry()
        if (mounted && canManageSettings()) { loadSettings(); loadPaymentMethods() }
      })
    }
    watch(() => [principal(), role(), getCurrentUserLogoutEpoch()], (next, previous) => {
      const identityChanged = next[0] !== previous[0] || next[1] !== previous[1]
      const logoutChanged = next[2] !== previous[2]
      if (!identityChanged && !logoutChanged) return
      // Residual cache cannot re-authorize a logged-out entry. Preserve the
      // existing same-view recovery only for a later stable administrator.
      if (identityChanged && !logoutChanged && next[0] && isAdminUser(currentUser.value)) authorizedLogoutEpoch = next[2]
      retireScope()
    }, { flush: 'sync' })
    watch(() => [formState.wechatScanLoginEnabled, formState.wechatCodeLoginEnabled], ([scanEnabled, codeEnabled]) => {
      if (!scanEnabled && !codeEnabled) { formState.wechatLoginEnabled = false; return }
      if (!formState.wechatLoginEnabled) formState.wechatLoginEnabled = true
      if (formState.wechatDefaultLoginMode === 'code' && !codeEnabled) formState.wechatDefaultLoginMode = scanEnabled ? 'scan' : 'code'
      else if (formState.wechatDefaultLoginMode !== 'code' && !scanEnabled && codeEnabled) formState.wechatDefaultLoginMode = 'code'
    }, { flush: 'sync' })
    onMounted(() => { mounted = true; if (canManageSettings()) { loadSettings(); loadPaymentMethods() } })
    onBeforeUnmount(() => { alive = false; mounted = false; retireScope() })
    return {
      paymentMethodFieldErrors,
      paymentListFailure, paymentListFailureContextKey, isCurrentPaymentListFailure,
      paymentDeleteFailure, paymentDeleteFailureContextKey, isCurrentPaymentDeleteFailure,
      paymentSaveFailure, paymentSaveFailureContextKey, paymentSaveFailureText, isCurrentPaymentSaveFailure,
      settingsReadFailure, settingsReadFailureContextKey, isCurrentSettingsReadFailure,
      settingsSaveFailure, settingsSaveFailureContextKey, isCurrentSettingsSaveFailure,
      fieldId,
      paymentMethodIconCatalogId, settingsEntry, paymentPanelEntry, paymentDialogEntry, paymentMethodAfterClose,
      settingsForm, settingsFormMounted, settingsFormKey, settingsDraining,
      paymentMethodForm, paymentMethodMounted, paymentMethodKey, paymentMethodDeleting,
      settingsLoaded, settingsError, paymentMethodsLoaded, paymentMethodsError, canManageSettings,
      hasUnsavedChanges, requestRouteLeave,
      labelCol: { style: { width: '140px' } }, wrapperCol: { span: 14 },
      paymentMethodLabelCol: { style: { width: '110px' } }, paymentMethodWrapperCol: { span: 17 },
      useRemoteCaptcha: computed(() => formState.captchaProvider !== 'local'),
      tabLoading, saving, paymentMethodLoading, paymentMethodVisible, paymentMethodSubmitting, paymentMethodEditing,
      paymentMethods, formState, MIN_LOG_SIZE_BYTES, LOG_SIZE_UNITS, minimumLogSizeValue, parseIntegerInput,
      paymentMethodFormState, derivedNotifyUrl, derivedReturnUrl, paymentMethodModalTitle,
      loadSettings, loadPaymentMethods, fillBaseUrlFromBrowser, updateSetting, updatePaymentField,
      handleLoad, handleSave, handleLoadPaymentMethods, openCreatePaymentMethodModal, openEditPaymentMethodModal,
      handlePaymentIconSelect, handlePaymentIconTextChange, handleSubmitPaymentMethod, handleDeletePaymentMethod,
      closePaymentMethodModal, resetPaymentMethodFormState: retirePaymentDialog, isSafePaymentIcon,
      paymentMethodColumns: [
        {
          title: '名称',
          dataIndex: 'name',
          key: 'name',
          width: 160,
        },
        {
          title: '图标',
          dataIndex: 'iconSvg',
          key: 'iconSvg',
          width: 120,
        },
        {
          title: '易支付通道',
          dataIndex: 'epayType',
          key: 'epayType',
          width: 150,
        },
        {
          title: '状态',
          dataIndex: 'enabled',
          key: 'enabled',
          width: 120,
        },
        {
          title: '排序',
          dataIndex: 'sort',
          key: 'sort',
          width: 90,
        },
        {
          title: '备注',
          dataIndex: 'remark',
          key: 'remark',
          width: 240,
        },
        {
          title: '操作',
          dataIndex: 'operation',
          key: 'operation',
          width: 120,
        },
      ],
    }
  },
  beforeRouteLeave(to) { return this.requestRouteLeave(to) },
})
</script>

<style scoped>
.log-size-controls { display: flex; flex-wrap: wrap; gap: 8px; min-width: 0; }
.log-size-control { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; }
.log-size-control > label { flex-shrink: 0; }
.log-size-control :deep(.ant-input-number),
.log-size-control :deep(.ant-select) { min-width: 0; max-width: 100%; flex-shrink: 1; }

.settings-form { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
.settings-section :deep(.ant-form-item:last-child) { margin-bottom: 0; }
.settings-section :deep(.ant-form-item-control) { max-width: 820px; }
.page-alert { margin-bottom: 0; }
.hint { margin-top: 8px; color: var(--console-muted); font-size: 12px; line-height: 1.8; overflow-wrap: anywhere; }
.base-url-controls { width: 100%; }
.base-url-controls :deep(.ant-space-item) { min-width: 0; max-width: 100%; }
.base-url-input { width: 320px; min-width: 0; max-width: 100%; }
@media (max-width: 768px) { .settings-section :deep(.ant-form-item) { display: block; } .settings-section :deep(.ant-form-item-label) { width: 100% !important; text-align: left; padding-bottom: 8px; } .settings-section :deep(.ant-form-item-control) { max-width: 100%; } .base-url-input { width: 100%; } }
</style>
