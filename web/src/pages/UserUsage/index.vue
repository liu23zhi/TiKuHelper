<template>
  <div class="console-page user-usage-page">
    <!-- 标题必须与侧边栏入口同名（T-COPY）。
         两侧菜单里这个 key（user-usage）都叫「个人使用」，页面标题原为「账户使用」，
         于是用户从「个人使用」点进来看到的却是「账户使用」—— 会以为自己点错了。
         选「改标题」而不是「改菜单」的理由：
           ① 菜单文案在两个角色下**本来就相同**（都叫「个人使用」），改菜单要动两处，
              改标题只动一处，且菜单是用户记住的入口名，不宜反向迁就页面；
           ② 用户侧菜单已有「个人中心 / 个人使用」这一族命名，改成「账户使用」会与它断开，
              而另一族是「我的账单 / 我的脚本」的「我的」前缀 —— 「账户使用」两头都不靠；
           ③ 本页不只是「账户」：第二个 Tab 是「等级提升」，等级是身份属性不是账户属性，
              叫「账户使用」反而比「个人使用」更窄。
         description 保留原话，它已经把「余额 + 消费/充值流水」讲清楚了。
         由 roleCopyConsistency.test.mjs 钉住：共用 key 的静态标题必须等于两侧共同文案。 -->
    <ConsolePageHeading title="个人使用" description="管理账户余额，并查看个人消费与充值流水。">
      <template #actions><a-button @click="handleRefreshAll" :loading="pageRefreshing">刷新</a-button></template>
    </ConsolePageHeading>

    <ConsoleSummaryGrid>
      <div class="summary-card"><a-statistic title="账户余额合计" prefix="¥" :value="currentUserTotalBalance" :precision="6" /><p>充值本金与赠送余额合计</p></div>
      <!-- 本金与赠金是两个独立金额，合计仅用于展示可用余额。 -->
      <div class="summary-card"><a-statistic title="充值本金（可退款）" prefix="¥" :value="currentUserBalance" :precision="6" /><p>来自充值的本金，按退款规则计算可退金额。</p></div>
      <div class="summary-card"><a-statistic title="赠送余额（不可退款）" prefix="¥" :value="currentUserGiftBalance" :precision="6" /><p>赠送金额可用于消费，但不能申请退款。</p></div>
      <div class="summary-card"><a-statistic title="账单总数" :value="summary.total" /></div>
      <div class="summary-card"><a-statistic title="当前页条数" :value="summary.currentPageCount" /></div>
      <div class="summary-card"><a-statistic title="当前页消费金额" prefix="¥" :value="summary.totalAmount" :precision="6" /></div>
    </ConsoleSummaryGrid>

    <!-- 账户使用页改成 Tab 结构（123 号 T-26）。
         Tab 顺序是契约：**充值在上，等级提升在下**（与 USER_USAGE_TAB_ORDER 一致）。

         ⚠️ 面板必须**显式手写**两个 a-tab-pane、不要用 v-for 生成：
         v-for 会把顺序藏进数组，模板顺序被对调时测试看不出差别。手写之后
         userUsageTabSource.test.mjs 直接按出现顺序抽取 pane，顺序错位必红。 -->
    <a-tabs v-model:activeKey="activeUsageTab" :destroy-inactive-tab-pane="false" class="usage-tabs">
      <!-- ① 充值（第一个 pane，顺序不可动） -->
      <a-tab-pane key="recharge" tab="充值">
    <ConsolePanel title="账户余额与充值" description="选择金额和支付方式，创建充值订单。" class="recharge-card">
      <a-row :gutter="16" class="recharge-summary-row">
        <a-col :xs="24" :lg="10">
          <div class="balance-panel">
            <div class="balance-label">当前账户余额</div>
            <div class="balance-value">¥ {{ formatMoneyValue(currentUserTotalBalance) }}</div>
            <div class="balance-meta">账户状态以最新余额为准</div>
            <a-alert v-if="balanceRefreshError" class="balance-refresh-error" type="warning" show-icon :message="balanceRefreshError">
              <template #action><a-button size="small" :loading="currentUserLoading" @click="handleRefreshCurrentUser">重试余额刷新</a-button></template>
            </a-alert>
            <a-alert v-else-if="!currentUserLoading && currentUserTotalBalance <= 0" class="balance-warning" type="warning" show-icon
              message="余额不足，新的付费调用将被阻止；已付结果取回仍须通过身份和归属校验。" />
            <section v-if="balanceFailure" data-user-usage-balance-feedback>
              <RequestFailureAlert :key="balanceFailureContextKey" :failure="balanceFailure" message="余额刷新失败，当前显示的是上次读取的余额。" :busy="currentUserLoading" :context-key="balanceFailureContextKey" :is-current="isCurrentBalanceFailure" />
            </section>
          </div>
        </a-col>
        <a-col :xs="24" :lg="14">
          <a-form layout="vertical" class="recharge-form">
            <a-form-item label="充值金额" :html-for="rechargeAmountId">
              <a-input-number
                :id="rechargeAmountId"
                :aria-describedby="rechargeAmountId + '-hint'"
                v-model:value="rechargeAmount"
                :min="Math.max(0.01, minRechargeAmount)"
                :precision="2"
                :step="1"
                :parser="parseRechargeAmountInput"
                :formatter="formatRechargeAmountInput"
                placeholder="请输入充值金额，例如 10 或 25.50"
                size="large"
                style="width: 100%"
                @keydown="handleRechargeAmountKeydown"
              />
              <div :id="rechargeAmountId + '-hint'" class="hint">最低充值金额：¥ {{ formatMoneyValue(minRechargeAmount) }}；金额最多支持两位小数。</div>
            </a-form-item>

            <a-form-item label="快捷金额">
              <div class="recharge-quick-amounts">
                <a-button
                  v-for="amount in rechargeQuickAmounts"
                  :key="amount"
                  class="recharge-quick-amount"
                  @click="applyRechargeQuickAmount(amount)"
                >
                  {{ amount }}
                </a-button>
              </div>
            </a-form-item>

            <a-form-item label="支付方式">
              <a-alert v-if="paymentMethodsError" class="payment-method-load-error" type="error" show-icon :message="paymentMethodsError">
                <template #action><a-button size="small" :loading="paymentMethodLoading" @click="handleLoadPaymentMethods">重试支付方式</a-button></template>
              </a-alert>
              <div v-if="paymentMethods.length" class="payment-method-grid">
                <button
                  v-for="method in paymentMethods"
                  :key="method.id"
                  type="button"
                  class="payment-method-card"
                  :class="{ 'payment-method-card--selected': selectedPaymentMethodId === method.id }"
                  :aria-pressed="selectedPaymentMethodId === method.id"
                  @click="selectPaymentMethod(method.id)"
                >
                  <SafePaymentIcon class="payment-method-icon" :value="method.iconSvg || ''" />
                  <span class="payment-method-main">
                    <span class="payment-method-name">{{ method.name }}</span>
                    <span class="payment-method-remark">{{ method.remark || method.epayType || '可用支付方式' }}</span>
                  </span>
                </button>
              </div>
              <a-empty v-else-if="!paymentMethodLoading && !paymentMethodsError" description="暂无可用支付方式，请联系管理员。" />
              <a-spin v-else-if="paymentMethodLoading && !paymentMethods.length" tip="正在加载支付方式" />
            </a-form-item>

            <div class="recharge-actions">
              <a-button type="primary" @click="handleCreateRechargeOrder" :loading="rechargeSubmitting" :disabled="paymentMethodLoading || !!paymentMethodsError || !paymentMethods.length">
                创建充值订单
              </a-button>
            </div>
          </a-form>
        </a-col>
      </a-row>

      <section v-if="orderFailure" class="page-alert" data-user-usage-order-feedback>
        <RequestFailureAlert :key="orderFailureContextKey" :failure="orderFailure" message="创建充值订单请求未完成，请核对充值记录后再操作。" :busy="rechargeSubmitting" :retryable="false" :context-key="orderFailureContextKey" :is-current="isCurrentOrderFailure" />
      </section>

      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="这里只展示当前账号自己的账单记录。充值创建订单后会跳转至收银台，支付结果以异步通知为准；如余额已变化，可点击刷新同步最新余额。"
        style="display: none"
      />
    </ConsolePanel>

    <ConsolePanel title="账单流水" description="查看题目消费与充值入账的金额变动、来源渠道和请求 ID。" class="page-card">

      <!-- 筛选栏（138 §3.2.3 / B1-5）。
           ⚠️ 维度与取值**全部**来自 utils/billingFilters.mjs，与「账单明细」页共用同一份：
             关键词 / 类型 / 时间范围三项。
           为什么只做这三项：其余维度（用户 ID / 请求 ID / 渠道 / 搜索模式 / 来源 IP）
           是管理员排障用的，个人使用页不需要；但「类型」的 9 个取值必须与
           BillingRecordList 逐字一致 —— 两页各写一份会让同一笔账单在两边筛出不同结果，
           那是比「少一个筛选」严重得多的缺陷（138 §3.2.3）。
           方向（收入/支出）不是独立维度：它由 type 隐含决定，后端也没有该字段。 -->
      <a-form layout="inline" class="billing-filter-form" data-user-usage-billing-filters>
        <a-form-item label="关键词" :html-for="billingFilterId('keyword')">
          <a-input-search
            :id="billingFilterId('keyword')"
            v-model:value="billingKeywordInput"
            placeholder="题目 / 请求 ID"
            allow-clear
            style="width: 220px"
            @search="handleBillingKeywordApply"
            @press-enter="handleBillingKeywordApply"
          />
        </a-form-item>
        <a-form-item label="类型" :html-for="billingFilterId('type')">
          <a-select
            :id="billingFilterId('type')"
            v-model:value="billingFilters.type"
            allow-clear
            placeholder="全部类型"
            style="width: 170px"
            :options="billingTypeOptions"
            @change="handleBillingFilterChange"
          />
        </a-form-item>
        <a-form-item label="创建日期" :html-for="billingFilterId('createdFrom')">
          <a-input
            :id="billingFilterId('createdFrom')"
            v-model:value="billingFilters.createdFrom"
            type="date"
            style="width: 150px"
            aria-label="创建日期起始"
            @change="handleBillingFilterChange"
          />
          <span class="filter-range-separator">至</span>
          <a-input
            v-model:value="billingFilters.createdTo"
            type="date"
            style="width: 150px"
            aria-label="创建日期截止"
            @change="handleBillingFilterChange"
          />
        </a-form-item>
        <a-form-item>
          <a-space>
            <a-button @click="handleBillingFilterReset">重置筛选</a-button>
            <a-tag v-if="billingActiveFilterCount > 0" color="blue" data-user-usage-billing-filter-count>
              已启用 {{ billingActiveFilterCount }} 个筛选条件
            </a-tag>
          </a-space>
        </a-form-item>
      </a-form>

      <a-alert v-if="billingLoadError" class="billing-load-error" type="error" show-icon :message="billingLoadError">
        <template #action><a-button size="small" :loading="tableLoading" @click="handleLoadBillingRecords">重试账单加载</a-button></template>
      </a-alert>
      <ConsoleTableShell label="个人账单流水">
        <a-table
          class="personal-billing-table"
          bordered
          :data-source="dataSource"
          :columns="columns"
          :loading="tableLoading"
          :pagination="pagination"
          @change="handleTableChange"
          row-key="id"
          table-layout="fixed"
          :scroll="{ x: tableScrollX }"
        >
          <template #bodyCell="{ column, record, text }">
            <template v-if="column.key === 'typeLabel'">
              <a-tag :color="record.direction === 'income' ? 'green' : 'blue'">{{ buildBillingRecordTypeText(record) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'subject'">
              <OverflowTableText
                :text="buildBillingRecordListSubjectText(record)"
                :expanded="isSubjectExpanded(record.id)"
                @toggle="toggleSubject(record.id)"
              />
            </template>
            <template v-else-if="column.key === 'amount'">
              <span :class="record.direction === 'income' ? 'amount-income' : 'amount-expense'">{{ buildBillingRecordAmountText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'source'">
              <span class="multiline-cell">{{ buildBillingRecordSourceText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'searchMode'">
              <span>{{ buildBillingRecordModeText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'referenceNo'">
              <span class="reference-cell">{{ buildBillingRecordReferenceText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'accessTokenName'">
              <span>{{ record.accessTokenName || '-' }}</span>
            </template>
            <template v-else-if="column.key === 'createdAt'">
              <span class="billing-time-cell">{{ formatDateTime(record.createdAt) }}</span>
            </template>
            <template v-else-if="column.key === 'feeDetail'">
              <a-button type="link" style="padding: 0" @click="handleShowFeeDetail(record)">查看详情</a-button>
            </template>
            <template v-else>{{ text }}</template>
          </template>
          <template #emptyText>
            <a-empty v-if="!tableLoading && !billingLoadError" description="当前还没有消费记录。" />
            <span v-else>{{ billingLoadError ? '账单尚未加载成功' : '正在加载账单…' }}</span>
          </template>
        </a-table>
      </ConsoleTableShell>
    </ConsolePanel>
      </a-tab-pane>

      <!-- ② 等级提升（第二个 pane）。放在充值下面（123 号原话）。 -->
      <a-tab-pane key="level" tab="等级提升">
        <ConsolePanel title="等级提升" description="查看当前等级、两个口径的累计值，以及下一档的门槛与差额。" class="level-card">
          <a-alert v-if="levelLoadError" type="warning" show-icon class="page-alert" :message="levelLoadError">
            <template #action>
              <a-button size="small" :loading="levelLoading" @click="handleLoadUserLevel">重试等级读取</a-button>
            </template>
          </a-alert>
          <a-spin v-if="levelLoading && !levelLoaded" tip="正在读取等级信息" />

          <template v-else>
            <a-row :gutter="[16, 16]" class="level-summary-row">
              <a-col :xs="24" :md="12" :lg="6">
                <div class="level-metric">
                  <div class="level-metric-label">当前等级</div>
                  <!-- 无等级时给明确文案（helper 保证），不留空白。 -->
                  <div data-user-usage-current-level class="level-metric-value">{{ currentLevelText }}</div>
                  <div class="level-metric-hint">等级由累计充值与累计消费共同决定。</div>
                </div>
              </a-col>
              <a-col :xs="24" :md="12" :lg="9">
                <div class="level-metric">
                  <div class="level-metric-label">{{ levelRechargeMetric.label }}</div>
                  <div data-user-usage-recharge-total class="level-metric-value">
                    ¥ {{ formatLevelMetric(levelRechargeMetric.value) }}
                  </div>
                  <div class="level-metric-hint">已成功支付的充值总额，扣除已成功退款金额。</div>
                </div>
              </a-col>
              <a-col :xs="24" :md="12" :lg="9">
                <div class="level-metric">
                  <div class="level-metric-label">{{ levelConsumptionMetric.label }}</div>
                  <div data-user-usage-consumption-total class="level-metric-value">
                    ¥ {{ formatLevelMetric(levelConsumptionMetric.value) }}
                  </div>
                  <div class="level-metric-hint">实际消费支出合计，不含余额调整、赠金撤回等。</div>
                </div>
              </a-col>
            </a-row>

            <!-- 下一档门槛与差额。
                 没有档位数据时 nextLevelRequirement.available === false，
                 这里如实显示原因，**不编造门槛数字**（63 号 U-03 红线）。 -->
            <div class="level-next">
              <template v-if="nextLevelRequirement.available">
                <div class="level-next-title">
                  下一档：<span data-user-usage-next-level>{{ nextLevelRequirement.levelName }}</span>
                </div>
                <a-row :gutter="[16, 16]" class="level-next-row">
                  <a-col :xs="24" :md="12">
                    <div class="level-next-metric">
                      <span class="level-next-label">累计充值门槛</span>
                      <span data-user-usage-next-recharge class="level-next-value">
                        ¥ {{ formatLevelMetric(nextLevelRequirement.minRechargeTotal) }}
                      </span>
                      <span data-user-usage-recharge-gap class="level-next-gap">
                        还差 ¥ {{ formatLevelMetric(nextLevelRequirement.rechargeGap) }}
                      </span>
                    </div>
                  </a-col>
                  <a-col :xs="24" :md="12">
                    <div class="level-next-metric">
                      <span class="level-next-label">累计消费门槛</span>
                      <span data-user-usage-next-consumption class="level-next-value">
                        ¥ {{ formatLevelMetric(nextLevelRequirement.minConsumptionTotal) }}
                      </span>
                      <span data-user-usage-consumption-gap class="level-next-gap">
                        还差 ¥ {{ formatLevelMetric(nextLevelRequirement.consumptionGap) }}
                      </span>
                    </div>
                  </a-col>
                </a-row>
                <div v-if="nextLevelRequirement.satisfied" class="level-next-satisfied">
                  两个门槛均已达标，请刷新查看最新等级。
                </div>
                <div v-else class="level-next-hint">
                  两个门槛**同时**满足才会升级（与后端判定口径一致）。
                </div>
              </template>
              <div v-else data-user-usage-level-requirement-empty class="level-next-hint">
                {{ nextLevelRequirement.reason }}
              </div>
            </div>

            <!-- ── 等级一览（138 §1.2 / B1-1）───────────────────────────
                 列出**全部启用**等级（enabled 过滤，rank 升序），按 id 标出当前等级。

                 「状态」列对未达成的档位**同时**显示两个差额
                 （`充值差 ¥300 / 消费差 ¥150`），绝不取最小值 —— 后端判定是 AND
                 （service/user_level.go:96-101 两条 continue），取 min 会给出
                 错误行动指引：用户补上较小的那一项后仍然升不了级。
                 已达标的那一维明确写「充值已达标」，让用户知道该维度不用再管。

                 设备数走 buildDeviceLimitText：0 渲染成「不限」，**不是**「0 台」。 -->
            <div class="level-overview" data-user-usage-level-overview>
              <div class="level-overview-title">等级一览</div>
              <template v-if="levelOverviewHasLevels">
                <a-table
                  class="level-overview-table"
                  :columns="levelOverviewColumns"
                  :data-source="levelOverviewRows"
                  :pagination="false"
                  row-key="id"
                  size="middle"
                  :scroll="{ x: 810 }"
                >
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'name'">
                      <span class="level-overview-name">{{ record.name || '—' }}</span>
                      <span v-if="record.isCurrent" class="level-overview-current-marker">← 当前</span>
                    </template>
                    <template v-else-if="column.key === 'minRechargeTotal'">
                      {{ formatLevelOverviewThreshold(record.minRechargeTotal) }}
                    </template>
                    <template v-else-if="column.key === 'minConsumptionTotal'">
                      {{ formatLevelOverviewThreshold(record.minConsumptionTotal) }}
                    </template>
                    <template v-else-if="column.key === 'deviceLimitText'">
                      {{ record.deviceLimitText }}
                    </template>
                    <template v-else-if="column.key === 'status'">
                      <span
                        :class="record.isCurrent ? 'level-overview-status-current' : record.satisfied ? 'level-overview-status-achieved' : 'level-overview-status-pending'"
                        :data-current="record.isCurrent ? 'true' : 'false'"
                      >{{ formatLevelOverviewStatus(record) }}</span>
                    </template>
                  </template>
                  <template #emptyText>
                    <a-empty description="暂无启用中的等级档位。" />
                  </template>
                </a-table>
                <div class="level-overview-hint">
                  两个门槛**同时**满足才会升级；「状态」列分列两个差额，补齐任意一项不等于升级。
                </div>
              </template>
              <div v-else class="level-overview-hint" data-user-usage-level-overview-empty>
                服务器未下发等级档位，无法列出等级一览。
              </div>
            </div>
          </template>
        </ConsolePanel>
      </a-tab-pane>
    </a-tabs>

    <BillingDetailDialog v-if="selectedBillingRecord" v-model:open="billingDetailOpen" :record="selectedBillingRecord" viewer-scope="self" />

    <a-modal
      v-model:open="paymentResultVisible"
      title="扫码支付"
      :footer="null"
      width="420px"
      centered
      @cancel="closePaymentResult"
    >
      <div class="payment-result">
        <img
          v-if="paymentQRCodeDataUrl"
          :src="paymentQRCodeDataUrl"
          alt="支付二维码"
          class="payment-qrcode-image"
        />
        <div v-else class="payment-qrcode-empty">正在生成支付二维码</div>
        <div class="payment-result-title">请使用对应支付 App 扫码完成充值</div>
        <div class="payment-result-meta">
          支付完成后余额会通过异步通知更新，可稍后刷新查看。
        </div>
        <div class="payment-result-actions">
          <a-button v-if="paymentJumpUrl" type="primary" block @click="openPaymentJumpFallback">
            打开支付链接
          </a-button>
          <a-button block @click="handleRefreshAll">刷新余额与记录</a-button>
        </div>
      </div>
    </a-modal>
  </div>
</template>

<script>
import { computed, defineComponent, getCurrentInstance, onBeforeUnmount, reactive, ref, shallowRef, useId, watch } from 'vue'
import { message } from 'ant-design-vue'
import QRCode from 'qrcode'
import OverflowTableText from '@/components/OverflowTableText.vue'
import BillingDetailDialog from '@/components/BillingDetailDialog.vue'
import SafePaymentIcon from '@/components/SafePaymentIcon.js'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { ensureRequestFailure, buildRequestFailureView } from '@/utils/requestFailureView.mjs'
import { useTableExpansion } from '@/composables/useTableExpansion.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import {
  createRechargeOrder,
  getBillingRecords,
  getCurrentUser,
  getPaymentMethods,
} from '@/api/TikuComponentApi'
import {
  buildRechargeOrderPayload,
  sanitizeRechargeAmountInput,
  shouldBlockRechargeAmountKey,
  sortPaymentMethodsForDisplay,
} from '@/utils/paymentMethodHelpers.mjs'
import {
  // `scope: 'self'` 的分页/作用域参数不再在这里拼：它已归入
  // billingFilters.buildPersonalBillingParams（那里在既有 helper 之上叠加筛选条件）。
  buildBillingRecordAmountText,
  buildBillingRecordListSubjectText,
  buildBillingRecordModeText,
  buildBillingRecordReferenceText,
  buildBillingRecordSourceText,
  buildBillingRecordSubjectText,
  buildBillingRecordTypeText,
  billingTableColumnWidths,
  formatBillingRecordTime,
} from '@/utils/userPortalHelpers.mjs'
import { useCurrentUserDisplay, setStoredCurrentUser } from '@/utils/currentUser'
import { useCurrentUserRequestScope } from '@/utils/currentUserScope'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import {
  USER_USAGE_DEFAULT_TAB,
  buildCurrentLevelText,
  buildLevelMetricText,
  buildLevelOverviewRows,
  buildLevelOverviewStatusText,
  buildNextLevelRequirement,
  buildUserUsageTabs,
} from '@/utils/userLevelHelpers.mjs'
// 账单筛选的**唯一**定义来源（138 §3.2.3 / B1-4 / B1-5）。
// 与「账单明细」页共用同一份 typeOptions / 参数组装 / 已启用条件数：
// 两页各写一套会让同一笔账单在两个页面筛出不同结果 —— 那是严重缺陷，
// 所以这里**禁止**另写一份字面量（有单测钉住「两页同一份」）。
import {
  BILLING_TYPE_OPTIONS,
  buildPersonalBillingParams,
  countBillingActiveFilters,
  createBillingFilters,
  normalizeBillingKeyword,
  resetBillingFilters,
} from '@/utils/billingFilters.mjs'
import { getMyUserLevel, getUserLevels } from '@/api/UserLevelApi'


function normalizeMinRechargeAmount(value) {
  const amount = Number(value)
  return Number.isFinite(amount) && amount > 0
    ? Math.round(amount * 100) / 100
    : 1
}

export default defineComponent({
  name: 'UserUsagePage',
  components: { OverflowTableText, BillingDetailDialog, SafePaymentIcon, RequestFailureAlert },
  setup() {
    const rechargeAmountId = 'user-usage-' + useId() + '-recharge-amount'
    // ── 等级提升 Tab（123 号 T-26）───────────────────────────────
    //
    // Tab 顺序来自 USER_USAGE_TAB_ORDER（充值在前），默认选中充值 ——
    // 保持重构前的首屏行为，不让用户一进页面就看不到充值表单。
    const usageTabs = buildUserUsageTabs()
    const activeUsageTab = ref(USER_USAGE_DEFAULT_TAB)
    const userLevelSummary = shallowRef(null)
    const userLevelRows = ref([])
    const levelLoading = ref(false)
    const levelLoaded = ref(false)
    const levelLoadError = ref('')
    const currentLevelText = computed(() => buildCurrentLevelText(userLevelSummary.value))
    const levelRechargeMetric = computed(() => buildLevelMetricText(userLevelSummary.value, 'recharge'))
    const levelConsumptionMetric = computed(() => buildLevelMetricText(userLevelSummary.value, 'consumption'))
    // 没有档位数据时 buildNextLevelRequirement 返回 available:false + 原因，
    // 界面上如实说明，**不编造门槛数字**（63 号 U-03 红线）。
    const nextLevelRequirement = computed(() => buildNextLevelRequirement(userLevelRows.value, userLevelSummary.value))
    // 金额展示：'—' 原样透出（未读到），数字走统一的 6 位小数格式。
    const formatLevelMetric = (value) => value === '—' || value === null || value === undefined
      ? '—'
      : formatMoneyValue(value)

    // ── 等级一览表（138 §1.2 / B1-1）─────────────────────────────
    //
    // 列出**全部启用**等级（enabled 过滤，rank 升序），每行都给两个差额。
    // ⚠️ 差额**绝不取 min**：后端判定是 AND（service/user_level.go:96-101 两条 continue），
    //    取 min 会给出错误行动指引 —— 用户补上较小的那一项后仍然升不了级。
    //    现有「下一档」区块（data-user-usage-recharge-gap / -consumption-gap）
    //    已经是两口径分列，本表沿用同一口径。
    const levelOverview = computed(() => buildLevelOverviewRows(userLevelRows.value, userLevelSummary.value))
    const levelOverviewRows = computed(() => levelOverview.value.rows)
    const levelOverviewColumns = [
      { title: '等级', dataIndex: 'name', key: 'name', width: 140 },
      { title: '累计充值门槛', dataIndex: 'minRechargeTotal', key: 'minRechargeTotal', width: 150 },
      { title: '累计消费门槛', dataIndex: 'minConsumptionTotal', key: 'minConsumptionTotal', width: 150 },
      { title: '设备数', dataIndex: 'deviceLimitText', key: 'deviceLimitText', width: 110 },
      { title: '状态', dataIndex: 'status', key: 'status', width: 260 },
    ]
    const formatLevelOverviewThreshold = (value) => value === null || value === undefined
      ? '—'
      : '¥ ' + formatMoneyValue(value)
    const formatLevelOverviewStatus = (row) => buildLevelOverviewStatusText(row)

    // ── 账单流水筛选（138 §3.2.3 / B1-5）────────────────────────
    //
    // 维度只做三项（关键词 / 类型 / 时间范围），取值与参数组装全部来自
    // utils/billingFilters.mjs —— 与「账单明细」页共用同一份定义。
    // 这里只负责把共享定义包成响应式对象并接上按钮事件。
    const billingFilterId = (field) => 'user-usage-billing-' + field + '-' + useId()
    const billingFilters = reactive({})
    Object.assign(billingFilters, createBillingFilters())
    const billingKeywordInput = ref('')
    const billingTypeOptions = BILLING_TYPE_OPTIONS
    const billingActiveFilterCount = computed(() => countBillingActiveFilters(billingFilters))
    // loadUserLevel 读当前等级（GET /user/level）与档位表（GET /admin/user-levels）。
    //
    // 两条数据作用不同，失败处理也不同：
    //   ① /user/level 人人可读 → 拿当前等级 + 两个口径值；
    //   ② /admin/user-levels 是**管理员专属** → 普通用户必然 403。
    //      拿不到档位就算不出下一档门槛，此时 nextLevelRequirement 返回
    //      available:false + 原因，界面如实说明「未下发档位」，
    //      **绝不编造门槛数字**（63 号 U-03 红线）。
    //
    // 放在 setup 闭包里（而不是 methods）有两个理由：
    //   ① 能直接用 `active` 这个卸载标记判断存活；
    //   ② 充值相关的既有用例把 handleRefreshAll 的方法集合钉死了，
    //      等级读取不该出现在那条链路里。
    let levelLoadEpoch = 0
    const loadUserLevel = () => {
      const sequence = ++levelLoadEpoch
      const current = () => active && sequence === levelLoadEpoch
      levelLoadError.value = ''
      levelLoading.value = true
      const readLevel = getMyUserLevel().then(res => {
        if (!current()) return
        if (res?.status !== 200) throw new Error(res?.message || '读取等级信息失败')
        const summary = res?.data?.data
        if (!summary || typeof summary !== 'object' || Array.isArray(summary)) {
          throw new Error('等级信息格式不正确')
        }
        userLevelSummary.value = summary
        levelLoaded.value = true
      })
      // 档位表是可选用数据：403（普通用户）与其它失败都不报错，只是拿不到门槛。
      const readRows = getUserLevels().then(res => {
        if (!current()) return
        const rows = Array.isArray(res?.data?.data) ? res.data.data : Array.isArray(res?.data) ? res.data : []
        userLevelRows.value = Array.isArray(rows) ? rows : []
      }).catch(() => { /* 无权限 / 不可用：保持空档位，界面显示「无法计算」。 */ })
      return Promise.all([readLevel, readRows]).catch(error => {
        if (!current()) return
        levelLoadError.value = error?.message || '读取等级信息失败，请重试。'
      }).finally(() => {
        if (current()) levelLoading.value = false
      })
    }
    const dataSource = ref([])
    const paymentMethods = ref([])
    const rechargeSubmitting = ref(false)
    const rechargeAmount = ref(10)
    const selectedPaymentMethodId = ref(null)
    const paymentResultVisible = ref(false)
    const paymentQRCodeDataUrl = ref('')
    const paymentJumpUrl = ref('')
    const minRechargeAmount = ref(1)
    const rechargeQuickAmounts = [10, 20, 50, 100, 200, 500]
    const pagination = reactive({
      current: 1,
      pageSize: 10,
      total: 0,
      showSizeChanger: true,
      showQuickJumper: true,
      showTotal: (total) => `共 ${total} 条`,
      pageSizeOptions: ['10', '20', '50', '100'],
    })
    const subjectExpansion = useTableExpansion(dataSource)
    const subjectColumnWidth = computed(() => subjectExpansion.columnWidth('subject', billingTableColumnWidths.subject, billingTableColumnWidths.subjectExpanded))
    const selectedBillingRecord = ref(null)
    const billingDetailOpen = ref(false)
    const { currentUser, captureUpdateGuard } = useCurrentUserDisplay()
    const instance = getCurrentInstance()
    const tableLoading = ref(false)
    const paymentMethodLoading = ref(false)
    const currentUserLoading = ref(false)
    const balanceRefreshError = ref('')
    const billingLoadError = ref('')
    const billingHasLoaded = ref(false)
    const paymentMethodsError = ref('')
    const billingLoadSequence = ref(0)
    const paymentLoadSequence = ref(0)
    const balanceFailure = shallowRef(null)
    const balanceFailureContextKey = ref(0)
    let balanceFailureOwner = () => false
    const clearBalanceFailure = () => {
      balanceFailure.value = null
      balanceFailureOwner = () => false
      balanceFailureContextKey.value += 1
    }
    const showBalanceFailure = (failure, owns) => {
      if (!owns() || failure?.name === 'AbortError' || failure?.code === 'ERR_CANCELED') return
      balanceFailureOwner = owns
      balanceFailure.value = ensureRequestFailure(failure)
    }
    const isCurrentBalanceFailure = (failure, context) => balanceFailureOwner() && Boolean(failure)
      && failure === balanceFailure.value && context === balanceFailureContextKey.value
    const orderFailure = shallowRef(null)
    const orderFailureContextKey = ref(0)
    let orderFailureOwner = () => false
    let orderFailureDraftLease = {}
    const readOrderFailureDraft = ([amount, method]) => {
      try { return buildRechargeOrderPayload(amount, method) } catch { return null }
    }
    const captureOrderFailureOwner = (owns, submitted) => {
      const lease = orderFailureDraftLease
      const amount = submitted.amount, paymentMethodId = submitted.paymentMethodId
      return () => {
        if (!owns() || lease !== orderFailureDraftLease) return false
        const current = readOrderFailureDraft([rechargeAmount.value, selectedPaymentMethodId.value])
        return current?.amount === amount && current?.paymentMethodId === paymentMethodId
      }
    }
    const clearOrderFailure = () => {
      orderFailure.value = null
      orderFailureContextKey.value += 1
      orderFailureOwner = () => false
    }
    const isCurrentOrderFailure = (failure, contextKey) => Boolean(failure)
      && failure === orderFailure.value && contextKey === orderFailureContextKey.value
      && orderFailureOwner()
    const showOrderFailure = (value, owns) => {
      if (!owns()) return
      const failure = ensureRequestFailure(value)
      orderFailureContextKey.value += 1
      orderFailureOwner = owns
      orderFailure.value = failure
      message.error(buildRequestFailureView(failure).title)
    }
    watch([rechargeAmount, selectedPaymentMethodId], (next, previous) => {
      const current = readOrderFailureDraft(next), prior = readOrderFailureDraft(previous)
      if (current?.amount === prior?.amount && current?.paymentMethodId === prior?.paymentMethodId) return
      orderFailureDraftLease = {}
      clearOrderFailure()
    }, { flush: 'sync' })
    let active = true
    let refreshSequence = 0
    const { captureDataOwner, canLoadForCurrentUser } = useCurrentUserRequestScope(currentUser, {
      clear() {
        clearBalanceFailure()
        clearOrderFailure()
        billingDetailOpen.value = false
        selectedBillingRecord.value = null
        refreshSequence += 1
        billingLoadSequence.value += 1
        paymentLoadSequence.value += 1
        balanceRefreshError.value = ''
        billingLoadError.value = ''
        billingHasLoaded.value = false
        paymentMethodsError.value = ''
        currentUserLoading.value = false
        paymentMethodLoading.value = false
        dataSource.value = []; pagination.total = 0; tableLoading.value = false
        subjectExpansion.clear()
        paymentMethods.value = []; selectedPaymentMethodId.value = null
        rechargeSubmitting.value = false
        paymentResultVisible.value = false; paymentQRCodeDataUrl.value = ''; paymentJumpUrl.value = ''
      },
      reset() { pagination.current = 1 },
      reload() { instance.proxy.handleLoadBillingRecords(); instance.proxy.handleLoadPaymentMethods() },
    })
    onBeforeUnmount(() => { active = false; billingDetailOpen.value = false; selectedBillingRecord.value = null; clearBalanceFailure(); clearOrderFailure() })
    // 等级信息改为「切到等级 Tab 时按需加载」：
    //   ① 充值仍是默认 Tab，首屏不额外发请求（重构前行为不变）；
    //   ② 每次切到等级 Tab 都重读两个口径值，避免充值后回来看到旧累计值。
    watch(activeUsageTab, (tab) => {
      if (tab === 'level') void loadUserLevel()
    }, { flush: 'post' })
    const handleRefreshCurrentUser = () => {
      if (!canLoadForCurrentUser()) return Promise.resolve()
      const isCurrentDisplay = captureUpdateGuard()
      const owns = captureDataOwner()
      const sequence = ++refreshSequence
      currentUserLoading.value = true
      balanceRefreshError.value = ''
      clearBalanceFailure()
      const context = balanceFailureContextKey.value
      // Feedback owns the view/data scope, not an ended read's raw display guard.
      const ownsFailure = () => active && sequence === refreshSequence && owns()
        && context === balanceFailureContextKey.value
      return getCurrentUser({ errorFeedback: 'local' }).catch((error) => {
        // Only the actual me rejection can persist, never local cache/amount errors.
        if (active && sequence === refreshSequence && isCurrentDisplay()) showBalanceFailure(error, ownsFailure)
        throw error
      }).then((res) => {
        if (!active || sequence !== refreshSequence || !isCurrentDisplay()) return
        if (res?.status === 200 && res?.data) {
          if (Object.prototype.hasOwnProperty.call(res.data, 'minRechargeAmount')) {
            minRechargeAmount.value = normalizeMinRechargeAmount(res.data.minRechargeAmount)
            if (Number(rechargeAmount.value) < minRechargeAmount.value) rechargeAmount.value = minRechargeAmount.value
          }
          setStoredCurrentUser(res.data)
        } else {
          balanceRefreshError.value = '余额刷新失败，当前显示的是上次读取的余额。'
          showBalanceFailure(res, ownsFailure)
        }
      }).catch(() => {
        if (active && sequence === refreshSequence && isCurrentDisplay()) {
          balanceRefreshError.value = '余额刷新失败，当前显示的是上次读取的余额。'
        }
      }).finally(() => {
        // A successful setStoredCurrentUser changes the display-version guard.
        // Release only this live request's busy state, not the captured raw value.
        if (active && sequence === refreshSequence && owns()) currentUserLoading.value = false
      })
    }
    const summary = computed(() => {
      const rows = Array.isArray(dataSource.value) ? dataSource.value : []
      return rows.reduce((acc, item) => {
        const amount = Number(item?.amount || 0)
        acc.currentPageCount += 1
        if (Number.isFinite(amount) && item?.direction !== 'income' && item?.type !== 'balance_adjustment') {
          acc.totalAmount += amount
        }
        return acc
      }, {
        total: billingHasLoaded.value ? Number(pagination.total || 0) : '--',
        currentPageCount: 0,
        totalAmount: 0,
      })
    })

    const currentUserBalance = computed(() => {
      const balance = Number(currentUser.value?.balance || 0)
      return Number.isFinite(balance) ? balance : 0
    })

    // gift_balance 与充值本金分开保存，按同一六位小数口径展示。
    const currentUserGiftBalance = computed(() => {
      const gift = Number(currentUser.value?.giftBalance || 0)
      return Number.isFinite(gift) ? gift : 0
    })
    const currentUserTotalBalance = computed(() => Number((currentUserBalance.value + currentUserGiftBalance.value).toFixed(6)))

    const pageRefreshing = computed(() => currentUserLoading.value || tableLoading.value || paymentMethodLoading.value)

    // 充值金额输入约束：输入阶段就挡住超出 2 位的金额。
    const parseRechargeAmountInput = (value) => sanitizeRechargeAmountInput(value)
    const formatRechargeAmountInput = (value) => sanitizeRechargeAmountInput(value)
    const handleRechargeAmountKeydown = (event) => {
      if (shouldBlockRechargeAmountKey(event?.target?.value, event?.key)) {
        event.preventDefault()
      }
    }

    const columns = computed(() => [
        {
          title: '账单 ID',
          dataIndex: 'id',
          key: 'id',
          width: billingTableColumnWidths.id,
        },
        {
          title: '类型',
          dataIndex: 'typeLabel',
          key: 'typeLabel',
          width: billingTableColumnWidths.typeLabel,
        },
        {
          title: '项目',
          dataIndex: 'subject',
          key: 'subject',
          width: subjectColumnWidth.value,
        },
        {
          title: '金额',
          dataIndex: 'amount',
          key: 'amount',
          width: billingTableColumnWidths.amount,
        },
        {
          title: '来源/渠道',
          dataIndex: 'source',
          key: 'source',
          width: billingTableColumnWidths.source,
        },
        {
          title: '模式',
          dataIndex: 'searchMode',
          key: 'searchMode',
          width: billingTableColumnWidths.searchMode,
        },
        {
          title: '请求 ID',
          dataIndex: 'referenceNo',
          key: 'referenceNo',
          width: billingTableColumnWidths.referenceNo,
        },
        {
          title: '访问令牌名称',
          dataIndex: 'accessTokenName',
          key: 'accessTokenName',
          width: billingTableColumnWidths.accessTokenName,
        },
        {
          title: '时间',
          dataIndex: 'createdAt',
          key: 'createdAt',
          width: billingTableColumnWidths.createdAt,
        },
        {
          title: '操作',
          dataIndex: 'feeDetail',
          key: 'feeDetail',
          width: billingTableColumnWidths.feeDetail,
        },
      ])
    const tableScrollX = computed(() => sumTableColumnWidths(columns.value))

    return {
      rechargeAmountId,
      balanceFailure, balanceFailureContextKey, isCurrentBalanceFailure,
      orderFailure, orderFailureContextKey, isCurrentOrderFailure, clearOrderFailure, showOrderFailure, captureOrderFailureOwner,
      captureDataOwner, canLoadForCurrentUser, handleRefreshCurrentUser, currentUserLoading,
      balanceRefreshError, billingLoadError, billingHasLoaded, paymentMethodsError, billingLoadSequence, paymentLoadSequence,
      currentUser,
      dataSource,
      paymentMethods,
      tableLoading,
      paymentMethodLoading,
      rechargeSubmitting,
      rechargeAmount,
      minRechargeAmount,
      parseRechargeAmountInput,
      formatRechargeAmountInput,
      handleRechargeAmountKeydown,
      selectedPaymentMethodId,
      paymentResultVisible,
      paymentQRCodeDataUrl,
      paymentJumpUrl,
      rechargeQuickAmounts,
      pagination,
      tableScrollX,
      selectedBillingRecord,
      billingDetailOpen,
      isSubjectExpanded: (id) => subjectExpansion.isExpanded(id, 'subject'),
      toggleSubject: (id) => subjectExpansion.toggle(id, 'subject'),
      summary,
      currentUserBalance,
      currentUserTotalBalance,
      currentUserGiftBalance,
      pageRefreshing,
      // 等级提升 Tab（T-26）
      usageTabs, activeUsageTab, levelLoading, levelLoaded, levelLoadError,
      currentLevelText, levelRechargeMetric, levelConsumptionMetric, nextLevelRequirement, formatLevelMetric,
      // 等级一览表（138 §1.2 / B1-1）：全部启用等级 + 每行两个差额。
      levelOverviewRows, levelOverviewColumns, formatLevelOverviewThreshold, formatLevelOverviewStatus,
      levelOverviewHasLevels: computed(() => levelOverview.value.hasLevels),
      // 供模板"重试等级读取"与 methods.handleLoadUserLevel 调用。
      loadUserLevel,
      // 账单流水筛选（138 §3.2.3 / B1-5）：全部来自共享 helper。
      billingFilterId, billingFilters, billingKeywordInput, billingTypeOptions, billingActiveFilterCount,
      formatMoneyValue,
      buildBillingRecordAmountText,
      buildBillingRecordListSubjectText,
      buildBillingRecordModeText,
      buildBillingRecordReferenceText,
      buildBillingRecordSourceText,
      buildBillingRecordSubjectText,
      buildBillingRecordTypeText,
      columns,

    }
  },
  mounted() {
    this.handleRefreshAll()
  },
  methods: {
    handleShowFeeDetail(record) {
      if (!this.canLoadForCurrentUser() || !this.dataSource.includes(record)) return
      this.selectedBillingRecord = record
      this.billingDetailOpen = true
    },
    // ── 账单流水筛选事件（138 §3.2.3 / B1-5）───────────────────
    //
    // 三个事件都回到同一个入口 handleLoadBillingRecords，保证「筛选即重新取数」，
    // 而不是在前端过滤当前页 —— 后者会让总数（pagination.total）与实际结果不符（B4 判据）。
    handleBillingKeywordApply() {
      // 关键词与「账单明细」页同一归一化口径（去首尾空白 + 截断）。
      this.billingFilters.keyword = normalizeBillingKeyword(this.billingKeywordInput)
      this.billingKeywordInput = this.billingFilters.keyword
      this.pagination.current = 1
      this.handleLoadBillingRecords()
    },
    handleBillingFilterChange() {
      this.pagination.current = 1
      this.handleLoadBillingRecords()
    },
    handleBillingFilterReset() {
      // 字段集来自共享 helper：将来加维度时这里不用改，也不会漏重置某一项。
      resetBillingFilters(this.billingFilters)
      this.billingKeywordInput = ''
      this.pagination.current = 1
      this.handleLoadBillingRecords()
    },
    handleRefreshAll() {
      // ⚠️ 这里**只**保留重构前的三个调用，不要加等级读取。
      // 既有用例 loginAndRechargePageSource.test.mjs:244-257 用 deepEqual 钉死了
      // 这个调用序列（充值回归钉子）。等级改由「切到等级 Tab 时按需加载」触发
      // （见 setup 里的 watch(activeUsageTab)），这样充值链路的可观察行为
      // 与重构前逐项一致。
      return Promise.all([
        this.handleRefreshCurrentUser(),
        this.handleLoadBillingRecords(),
        this.handleLoadPaymentMethods(),
      ])
    },
    // handleLoadUserLevel 读当前等级（GET /user/level）与档位表（GET /admin/user-levels）。
    //
    // 两条数据的作用不同，失败处理也不同：
    //   ① /user/level 人人可读 → 拿当前等级 + 两个口径值；
    //   ② /admin/user-levels 是**管理员专属** → 普通用户一定 403。
    //      拿不到档位就**算不出**下一档门槛，此时 nextLevelRequirement 返回
    //      available:false 并给出原因，界面上如实说明「未下发档位」，
    //      绝不编造门槛数字（63 号 U-03 红线）。
    //
    // 返回的 Promise 永远 resolve：等级读取失败不该阻塞充值/账单的刷新。
    handleLoadUserLevel() {
      return this.loadUserLevel()
    },
    handleLoadBillingRecords() {
      if (!this.canLoadForCurrentUser()) return Promise.resolve()
      const owner = this.captureDataOwner()
      const sequence = ++this.billingLoadSequence
      const owns = () => owner() && sequence === this.billingLoadSequence
      this.billingLoadError = ''
      this.tableLoading = true
      return getBillingRecords(buildPersonalBillingParams(this.billingFilters, this.pagination.current, this.pagination.pageSize)).then((res) => {
        if (!owns()) return
        if (res?.status === 200) {
          this.dataSource = Array.isArray(res?.data?.data) ? res.data.data : []
          this.pagination.total = Number(res?.data?.total || 0)
          this.pagination.current = Number(res?.data?.page || 1)
          this.pagination.pageSize = Number(res?.data?.pageSize || 10)
          this.billingHasLoaded = true
        } else {
          this.billingLoadError = this.billingHasLoaded
            ? '获取账单列表失败，已保留上次成功结果。'
            : '账单尚未加载成功，请重试读取。'
          message.error(res?.message || '获取账单列表失败')
        }
      }).catch(() => {
        if (!owns()) return
        this.billingLoadError = this.billingHasLoaded
            ? '获取账单列表失败，已保留上次成功结果。'
            : '账单尚未加载成功，请重试读取。'
        message.error('获取账单列表失败')
      }).finally(() => {
        if (!owns()) return
        this.tableLoading = false
      })
    },
    handleLoadPaymentMethods() {
      if (!this.canLoadForCurrentUser()) return Promise.resolve()
      const owner = this.captureDataOwner()
      const sequence = ++this.paymentLoadSequence
      const owns = () => owner() && sequence === this.paymentLoadSequence
      this.paymentMethodsError = ''
      this.paymentMethodLoading = true
      return getPaymentMethods().then((res) => {
        if (!owns()) return
        if (res?.status === 200) {
          const rows = Array.isArray(res?.data) ? res.data : []
          this.paymentMethods = sortPaymentMethodsForDisplay(rows).filter((item) => !!item.enabled)
          if (!this.selectedPaymentMethodId && this.paymentMethods.length) {
            this.selectedPaymentMethodId = this.paymentMethods[0].id
          }
          if (this.selectedPaymentMethodId && !this.paymentMethods.some((item) => item.id === this.selectedPaymentMethodId)) {
            this.selectedPaymentMethodId = this.paymentMethods[0]?.id || null
          }
        } else {
          this.paymentMethodsError = '获取支付方式失败，请重试后再创建订单。'
          message.error(res?.message || '获取支付方式失败')
        }
      }).catch(() => {
        if (!owns()) return
        this.paymentMethodsError = '获取支付方式失败，请重试后再创建订单。'
        message.error('获取支付方式失败')
      }).finally(() => {
        if (!owns()) return
        this.paymentMethodLoading = false
      })
    },
    handleTableChange(paginationData) {
      this.pagination.current = paginationData.current
      this.pagination.pageSize = paginationData.pageSize
      this.handleLoadBillingRecords()
    },
    formatDateTime(value) {
      return formatBillingRecordTime(value)
    },
    applyRechargeQuickAmount(amount) {
      // 快捷金额是数字，保持与 a-input-number 的取值类型一致。
      this.rechargeAmount = Number(amount)
    },
    selectPaymentMethod(id) {
      this.selectedPaymentMethodId = Number(id || 0)
    },
    closePaymentResult() {
      this.paymentResultVisible = false
    },
    openPaymentJumpFallback() {
      if (!this.paymentJumpUrl) {
        return
      }
      window.open(this.paymentJumpUrl, '_blank', 'noopener,noreferrer')
    },
    async showPaymentResult(data, owns = this.captureDataOwner()) {
      if (!owns()) return
      this.paymentQRCodeDataUrl = ''
      this.paymentJumpUrl = data?.payUrl || ''
      if (data?.payType === 'qrcode' && data?.qrcodeContent) {
        const qrCodeDataUrl = await QRCode.toDataURL(data.qrcodeContent, {
          margin: 1,
          width: 256,
          errorCorrectionLevel: 'M',
        })
        if (!owns()) return
        this.paymentQRCodeDataUrl = qrCodeDataUrl
        this.paymentResultVisible = true
        return
      }
      if (data?.payUrl || data?.payInfo) {
        this.paymentJumpUrl = data.payUrl || data.payInfo
        this.paymentResultVisible = true
        return
      }
      throw new Error('支付平台未返回可用支付参数')
    },
    async handleCreateRechargeOrder() {
      if (!this.canLoadForCurrentUser() || this.rechargeSubmitting) return
      if (this.paymentMethodLoading || this.paymentMethodsError || !this.paymentMethods.some(method => method.enabled && Number(method.id) === Number(this.selectedPaymentMethodId))) {
        message.warning('请选择支付方式')
        return
      }
      let payload
      try {
        if (Number(this.rechargeAmount) < this.minRechargeAmount) {
          throw new Error(`充值金额不能低于 ¥ ${formatMoneyValue(this.minRechargeAmount)}`)
        }
        payload = buildRechargeOrderPayload(this.rechargeAmount, this.selectedPaymentMethodId)
      } catch (error) {
        message.warning(error?.message || '充值参数有误')
        return
      }

      const owns = this.captureDataOwner()
      const ownsFailure = this.captureOrderFailureOwner(owns, payload)
      this.clearOrderFailure()
      this.rechargeSubmitting = true
      createRechargeOrder(payload).then((res) => {
        if (!owns()) return
        if (res?.status === 200) {
          return this.showPaymentResult(res.data, owns).then(() => {
            if (!owns()) return
            message.success('充值订单创建成功')
            this.handleRefreshCurrentUser()
          })
        }
        if (res?.status === 202) {
          const orderNo = res?.data?.orderNo
          message.warning(`支付平台结果待核对${orderNo ? `（订单号 ${orderNo}）` : ''}，请查看充值记录或联系管理员，勿重复下单`)
          return
        }
        this.showOrderFailure(res, ownsFailure)
      }).catch((error) => {
        if (!owns()) return
        this.showOrderFailure(error, ownsFailure)
      }).finally(() => {
        if (owns()) this.rechargeSubmitting = false
      })
    },
  },
})
</script>

<style scoped>
.summary-card { padding: 20px; border: 1px solid var(--console-border); border-radius: 10px; background: var(--console-surface); min-width: 0; }
.recharge-summary-row { row-gap: 20px; }
.balance-panel { display: flex; flex-direction: column; justify-content: center; height: 100%; min-height: 190px; padding: 24px; border-radius: 10px; background: var(--console-selected, #e7f3ef); border: 1px solid var(--console-border); }
.balance-label { color: var(--console-muted); }
.balance-value { margin: 12px 0; color: var(--console-primary); font-size: clamp(24px, 3vw, 36px); font-weight: 600; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
.balance-meta, .hint { color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.hint { margin-top: 8px; }
.recharge-quick-amounts { display: flex; flex-wrap: wrap; gap: 8px; }
.recharge-quick-amount { min-width: 60px; }
.recharge-actions { display: flex; justify-content: flex-end; }
.payment-method-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; }
.payment-method-card { display: flex; align-items: center; gap: 12px; min-width: 0; padding: 14px; background: var(--console-surface); border: 1px solid var(--console-border); border-radius: 8px; color: var(--console-text); text-align: left; cursor: pointer; transition: border-color .15s, background .15s; }
.payment-method-card--selected { border-color: var(--console-primary); background: var(--console-selected, #e7f3ef); }
.payment-method-card:focus-visible { outline: 2px solid var(--console-primary); outline-offset: 2px; }
.payment-method-icon { background: var(--console-subtle); display: inline-flex; align-items: center; justify-content: center; flex: 0 0 36px; width: 36px; height: 36px; }
.payment-method-icon :deep(svg), .payment-method-icon svg { width: 28px; height: 28px; }
.payment-method-main { display: grid; gap: 4px; min-width: 0; }
.payment-method-name { font-weight: 600; overflow-wrap: anywhere; }
.payment-method-remark { color: var(--console-muted); font-size: 12px; overflow-wrap: anywhere; }
.amount-income { color: var(--console-success, #15803d); }
.amount-expense { color: var(--console-danger, #c13e41); }
.reference-cell, .multiline-cell, .billing-time-cell { white-space: pre-wrap; overflow-wrap: anywhere; }
.billing-time-cell { font-variant-numeric: tabular-nums; }
.payment-result { display: grid; gap: 16px; text-align: center; }
.payment-qrcode-image { width: 256px; max-width: 100%; height: auto; margin: 0 auto; border: 1px solid var(--console-border); border-radius: 8px; }
.payment-qrcode-empty { display: grid; place-items: center; width: 256px; max-width: 100%; min-height: 220px; margin: 0 auto; color: var(--console-muted); background: var(--console-surface-muted, #f8faf9); border-radius: 8px; }
.payment-result-title { font-size: 16px; font-weight: 600; }
.payment-result-meta { color: var(--console-muted); line-height: 1.6; }
.payment-result-actions { display: grid; gap: 12px; }
/* ── 等级提升 Tab（T-26）───────────────────────────────── */
.usage-tabs :deep(.ant-tabs-nav) { margin-bottom: 16px; }
.level-summary-row { row-gap: 16px; }
.level-metric {
  display: flex; flex-direction: column; gap: 8px; height: 100%;
  padding: 20px; border-radius: 10px;
  background: var(--console-surface); border: 1px solid var(--console-border);
}
.level-metric-label { color: var(--console-muted); font-size: 13px; }
.level-metric-value { color: var(--console-text); font-size: clamp(18px, 2.2vw, 24px); font-weight: 600; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
.level-metric-hint { color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.level-next {
  display: flex; flex-direction: column; gap: 12px;
  margin-top: 20px; padding: 20px; border-radius: 10px;
  background: var(--console-surface-secondary, #f8faf9); border: 1px solid var(--console-border);
}
.level-next-title { color: var(--console-text); font-size: 15px; font-weight: 600; }
.level-next-row { row-gap: 12px; }
.level-next-metric { display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; min-width: 0; }
.level-next-label { color: var(--console-muted); font-size: 13px; }
.level-next-value { color: var(--console-text); font-weight: 600; font-variant-numeric: tabular-nums; }
.level-next-gap { color: var(--console-muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.level-next-hint { color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.level-next-satisfied { color: var(--console-success, #15803d); font-size: 13px; }
/* ── 等级一览表（138 §1.2）───────────────────────────────── */
.level-overview {
  display: flex; flex-direction: column; gap: 12px;
  margin-top: 20px; padding: 20px; border-radius: 10px;
  background: var(--console-surface); border: 1px solid var(--console-border);
}
.level-overview-title { color: var(--console-text); font-size: 15px; font-weight: 600; }
.level-overview-name { font-weight: 600; }
/* 「← 当前」必须与之一致地可见：颜色 + 字重，不只靠颜色（色盲可辨）。 */
.level-overview-current-marker { margin-left: 8px; color: var(--console-primary); font-weight: 600; }
.level-overview-status-current { color: var(--console-primary); font-weight: 600; }
.level-overview-status-achieved { color: var(--console-success, #15803d); }
.level-overview-status-pending { color: var(--console-muted); font-variant-numeric: tabular-nums; }
.level-overview-hint { color: var(--console-muted); font-size: 12px; line-height: 1.6; }
/* ── 账单流水筛选（138 §3.2.3）────────────────────────────── */
.billing-filter-form { display: flex; flex-wrap: wrap; gap: 12px 0; margin-bottom: 16px; }
.billing-filter-form :deep(.ant-form-item) { margin-bottom: 0; }
.filter-range-separator { margin: 0 8px; color: var(--console-muted); }
@media (max-width: 640px) {
  .summary-card { padding: 16px; }
  .balance-panel { min-height: 150px; padding: 20px; }
  .recharge-actions :deep(.ant-btn) { width: 100%; }
  .payment-method-grid { grid-template-columns: 1fr; }
  /* 筛选栏在窄屏改成块级堆叠：inline 表单横向溢出会把「重置筛选」按钮挤出视口。 */
  .billing-filter-form { display: block; }
  .billing-filter-form :deep(.ant-form-item) { margin: 0 0 12px; }
  .billing-filter-form :deep(.ant-form-item-control), .billing-filter-form :deep(.ant-input-search), .billing-filter-form :deep(.ant-select) { width: 100% !important; }
}
</style>
