<template>
  <!--
    注销引导 / 自助注销（B2-4 + B2-5）。

    # 为什么这个组件有两种 mode

    用户对注销的诉求是「两个入口 + 一个兜底」（138 号 §2.4 用户原话）：

      · 主入口：自助注销（个人中心）—— 立即生效、等待期内可撤销；
      · 兜底入口：注销工单（工单列表里选「注销申请」）——
        但**必须先引导用户走自助**，用户仍然继续才建单。

    所以同一套 UI 复用在两处，只是起点不同：

      mode="guide"        工单页选「注销申请」时 → 先出推荐卡片（主按钮=去自助注销）
      mode="self-service" 个人中心的「自助注销」入口 → 直接进自助流程

    # 三条硬约束（改动本组件前必读）

    1. **主按钮必须是「去自助注销」，次按钮才是「仍然提交工单」。**
       反过来做等于把兜底路径做成默认路径，用户会在不知情的情况下多等管理员几天。
       data-closure-action 属性是稳定标记，测试按它定位按钮（不按位置）。
    2. **申请必须带 password + acknowledge。** 后端 closure.go:144-152 缺任一项
       直接 400。acknowledge 是「已知晓注销后果」的知情确认，不允许前端代填。
    3. **状态必须从后端查，不能靠本地推断。** closure_pending 期间账号仍可登录
       （isUserSessionAllowed 认它），所以「我现在是不是在等待期」只有
       GET /api/user/closure-status 知道。
  -->
  <div class="closure-guide" :data-closure-mode="screen">
    <!-- ─────────── 引导页：推荐自助注销 ─────────── -->
    <section v-if="screen === 'guide'" class="closure-recommend" data-closure-guide>
      <a-alert
        type="warning"
        show-icon
        message="推荐使用自助注销"
        description="注销工单是兜底路径，需要等管理员处理。自助注销更快，而且可以随时撤销。"
      />
      <ul class="closure-benefits">
        <li>立即生效，无需等待管理员处理</li>
        <li>等待期内可一键撤销</li>
        <li>数据到期自动归档</li>
      </ul>
      <div class="closure-actions">
        <!-- 主按钮（primary）：自助注销。视觉与语义都必须在前面。 -->
        <a-button
          type="primary"
          data-closure-action="self-service"
          :onClick="openSelfService"
          >去自助注销</a-button
        >
        <!-- 次按钮（default）：仍然提交工单（兜底）。 -->
        <a-button data-closure-action="fallback" :onClick="onFallback"
          >仍然提交工单</a-button
        >
      </div>
    </section>

    <!-- ─────────── 自助注销流程（申请 / 撤销）─────────── -->
    <section v-else class="closure-self" data-closure-self>
      <div v-if="loading" class="closure-loading"><a-spin size="small" /> 正在读取注销状态…</div>

      <a-alert
        v-else-if="loadError"
        type="error"
        show-icon
        :message="loadError"
        description="注销状态未能确认，请刷新后重试。未确认前不会提交任何注销请求。"
      />

      <template v-else-if="status">
        <!-- 等待期：只显示倒计时与撤销 -->
        <template v-if="status.pending">
          <a-alert
            type="info"
            show-icon
            message="已提交注销申请"
            :description="pendingDescription"
          />
          <dl class="closure-facts">
            <div>
              <dt>等待期</dt>
              <dd>{{ status.waitDays }} 天</dd>
            </div>
            <div>
              <dt>剩余</dt>
              <dd>{{ status.remainingDays }} 天</dd>
            </div>
            <div>
              <dt>账户余额合计</dt>
              <dd>¥ {{ formatMoney(status.balanceTotal) }}</dd>
            </div>
          </dl>
          <a-alert
            v-if="status.requiresTicket"
            type="warning"
            show-icon
            message="账户仍有余额，已转人工处理"
            description="余额超过自动处理阈值，管理员会先核对退款或结转方案，再归档账号。"
          />
          <a-button
            danger
            data-closure-action="cancel"
            :loading="busy"
            :disabled="busy"
            :onClick="cancelClosure"
            >撤销注销申请</a-button
          >
        </template>

        <!-- 未申请：填密码 + 勾选知情确认 -->
        <template v-else>
          <a-alert
            v-if="!status.eligible"
            type="warning"
            show-icon
            message="当前账号不支持自助注销"
            :description="status.ineligibleReason || '请提交工单由管理员处理。'"
          />
          <template v-else>
            <a-alert
              type="warning"
              show-icon
              message="注销后果"
              description="提交后账号进入注销等待期：期间不可消费、可随时撤销；到期后账号会被归档，无法自行恢复。"
            />
            <div class="closure-field">
              <label for="closure-password">当前密码</label>
              <a-input
                id="closure-password"
                type="password"
                :value="password"
                :maxlength="128"
                autocomplete="current-password"
                :disabled="busy"
                placeholder="请输入当前密码以确认身份"
                @update:value="setPassword"
              />
            </div>
            <div class="closure-field closure-field--check">
              <!-- 知情确认：后端 acknowledge 字段，缺了直接 400。 -->
              <a-checkbox
                :checked="acknowledge"
                :disabled="busy"
                @change="setAcknowledge"
                >我已了解：注销后账号将移入归档，无法自行恢复</a-checkbox
              >
            </div>
            <a-button
              danger
              type="primary"
              data-closure-action="request"
              :loading="busy"
              :disabled="!canSubmit"
              :onClick="requestClosure"
              >确认申请注销</a-button
            >
          </template>
        </template>
      </template>
    </section>
  </div>
</template>

<script>
import { defineComponent } from "vue";
import { message } from "ant-design-vue";
import {
  cancelAccountClosure,
  getAccountClosureStatus,
  requestAccountClosure,
} from "@/api/TikuComponentApi";

// 空串 / 非有限数一律显示 0.00：余额是钱，不能出现 NaN 或空。
function formatMoney(value) {
  const amount = Number(value);
  return Number.isFinite(amount) ? amount.toFixed(2) : "0.00";
}

export default defineComponent({
  name: "ClosureGuide",
  props: {
    // 'guide' = 先出推荐卡片；'self-service' = 直接进自助流程（个人中心用）。
    mode: { type: String, default: "guide" },
  },
  emits: ["fallback", "requested", "cancelled"],
  data() {
    return {
      // screen 是当前显示的屏幕，与 props.mode 的区别：mode 是**起点**，
      // screen 会随「去自助注销」点击而前进。用 data 而不是 computed，
      // 否则点了主按钮又会被 props 拉回引导页。
      screen: this.mode === "self-service" ? "self-service" : "guide",
      loading: false,
      busy: false,
      loadError: "",
      status: null,
      password: "",
      acknowledge: false,
    };
  },
  computed: {
    canSubmit() {
      return (
        Boolean(this.status && this.status.eligible) &&
        String(this.password || "").length > 0 &&
        this.acknowledge === true &&
        !this.busy
      );
    },
    pendingDescription() {
      const total = this.status?.waitDays;
      const remaining = this.status?.remainingDays;
      if (!Number.isFinite(Number(remaining))) return "等待期内可随时撤销。";
      return `等待期 ${total} 天，剩余 ${remaining} 天。期间账号不可消费，但可以登录并撤销申请。`;
    },
  },
  methods: {
    formatMoney,
    // 去自助注销：主按钮。切到自助流程并立刻查一次真实状态
    // （不做乐观假设 —— 用户可能已经申请过，或账号没有手机号而不可自助）。
    openSelfService() {
      this.screen = "self-service";
      this.loadStatus();
    },
    // 仍然提交工单：次按钮。交给父组件决定进哪个表单。
    onFallback() {
      this.$emit("fallback");
    },
    setPassword(value) {
      this.password = String(value ?? "");
    },
    setAcknowledge(event) {
      // a-checkbox 的 change 事件给的是 Event，取 target.checked；
      // 直接透传布尔值也支持（测试里会这么调）。
      if (typeof event === "boolean") {
        this.acknowledge = event;
        return;
      }
      this.acknowledge = Boolean(event?.target?.checked);
    },
    async loadStatus() {
      this.loading = true;
      this.loadError = "";
      try {
        const response = await getAccountClosureStatus();
        this.status = normalizeClosureStatus(response);
      } catch (error) {
        this.status = null;
        this.loadError = responseMessage(error, "注销状态读取失败");
      } finally {
        this.loading = false;
      }
    },
    async requestClosure() {
      if (!this.canSubmit) return;
      this.busy = true;
      try {
        const response = await requestAccountClosure({
          password: String(this.password || "").trim(),
          // 后端要求布尔 true（closure.go:149）；这里显式写真值而不是透传
          // 组件状态，避免用户取消勾选后又因为状态竞态把 true 传上去。
          acknowledge: true,
        });
        const view = normalizeClosureStatus(response);
        if (view) this.status = view;
        this.password = "";
        this.acknowledge = false;
        message.success("已提交注销申请，等待期内可随时撤销");
        this.$emit("requested", this.status);
      } catch (error) {
        message.error(responseMessage(error, "注销申请提交失败"));
      } finally {
        this.busy = false;
      }
    },
    async cancelClosure() {
      if (this.busy) return;
      this.busy = true;
      try {
        const response = await cancelAccountClosure();
        const view = normalizeClosureStatus(response);
        if (view) this.status = view;
        else await this.loadStatus();
        message.success("已撤销注销申请");
        this.$emit("cancelled", this.status);
      } catch (error) {
        message.error(responseMessage(error, "撤销注销申请失败"));
      } finally {
        this.busy = false;
      }
    },
  },
  mounted() {
    // self-service 起点（个人中心入口）需要在挂载时就知道当前状态，
    // 否则用户看到的是一张空表单，而他其实已在等待期内。
    if (this.screen === "self-service") this.loadStatus();
  },
});

// normalizeClosureStatus 从响应里取状态视图。
//
// 三条接口的响应形状：
//   closure-status  → { data: {...} }
//   closure-request → { message, data: {...} }
//   closure-cancel  → { message, data: {...} }
// request.js 已经把 axios 响应解密成 payload，所以这里兼容
// 「payload.data.data」与「payload.data」两种层级。
function normalizeClosureStatus(response) {
  const payload = response?.data?.data ?? response?.data ?? null;
  if (!payload || typeof payload !== "object") return null;
  return {
    status: String(payload.status ?? ""),
    pending: payload.pending === true,
    waitDays: Number.isFinite(Number(payload.waitDays)) ? Number(payload.waitDays) : 0,
    remainingDays: Number.isFinite(Number(payload.remainingDays))
      ? Number(payload.remainingDays)
      : 0,
    balanceTotal: Number(payload.balanceTotal) || 0,
    requiresTicket: payload.requiresTicket === true,
    phoneBound: payload.phoneBound === true,
    eligible: payload.eligible === true,
    ineligibleReason: String(payload.ineligibleReason ?? ""),
  };
}

// responseMessage 取后端给的中文提示。
//
// 后端的注销错误全是人话（「密码错误」「账号没有绑定手机号，不支持自助注销…」），
// 直接用它们比自己拼一套文案更准确 —— 自己拼容易与后端规则漂移。
function responseMessage(error, fallback) {
  return (
    error?.message ||
    error?.response?.data?.message ||
    error?.data?.message ||
    fallback
  );
}
</script>

<style scoped>
.closure-guide { display: block; }
.closure-recommend { display: flex; flex-direction: column; gap: 12px; }
.closure-benefits { margin: 0; padding-left: 20px; color: var(--console-muted); font-size: 13px; line-height: 1.9; }
.closure-actions { display: flex; gap: 12px; flex-wrap: wrap; }
.closure-self { display: flex; flex-direction: column; gap: 12px; }
.closure-loading { display: flex; align-items: center; gap: 8px; color: var(--console-muted); font-size: 13px; }
.closure-facts { display: flex; gap: 24px; flex-wrap: wrap; margin: 0; }
.closure-facts dt { color: var(--console-muted); font-size: 12px; }
.closure-facts dd { margin: 2px 0 0; font-size: 16px; color: var(--console-text); }
.closure-field { display: flex; flex-direction: column; gap: 6px; }
.closure-field label { font-size: 13px; color: var(--console-text); }
.closure-field--check { flex-direction: row; align-items: center; }
</style>
