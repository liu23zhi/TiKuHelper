<template>
  <svg
    class="sensitive-image-icon"
    viewBox="0 0 1271 1024"
    role="img"
    :aria-label="label"
    aria-hidden="true"
    focusable="false"
    xmlns="http://www.w3.org/2000/svg"
  >
    <!--
      用户指定的「图片损坏 / 敏感图片」图标（task_doc/tupiansunhuai.svg）。

      只取原文件的 <path>：原 svg 带 `<!DOCTYPE svg PUBLIC ...>` 与 XML 声明，
      塞进 Vue 模板会变成合法 SFC 解析不了的东西（编译期就报错）。

      **viewBox 必须保持 0 0 1271 1024**：原图是 317.75×256 的长方形，
      改成 64×64 会把它压扁变形。显示尺寸由 CSS 控制（见下方 style）。

      用 fill="currentColor" 而不是硬编码颜色：这张图出现在工单占位块里，
      必须跟随主题（含深色模式）与 .ticket-attachment-icon 的 --console-muted。

      注意风格差异：原手写图标是 stroke 描边，这个是**实心填充**。
      这是用户指定的资源，不要为了「和原来一致」再优化回描边。
    -->
    <path
      fill="currentColor"
      p-id="1839"
      d="M710.09113 896a323.266 323.266 0 0 0 49.082 64.088H127.11713A128 128 0 0 1 0.00013 831.912V128A127.823 127.823 0 0 1 127.11713 0h953.38a127.823 127.823 0 0 1 127.117 128v379.586a316.734 316.734 0 0 0-95.338-64.794V160.309a63.912 63.912 0 0 0-63.559-63.912h-889.82a63.912 63.912 0 0 0-63.559 63.559v497.876l195.266-296.607a35.31 35.31 0 0 1 27.19-15.537 34.251 34.251 0 0 1 28.777 13.948l140.36 187.674a35.31 35.31 0 0 0 49.433 6.356l112.11-90.218a35.31 35.31 0 0 1 40.078-3.001l38.841 23.481a36.193 36.193 0 0 1 9.181 52.966 320.441 320.441 0 0 0-25.777 358.4z m275.07-447.912a284.954 284.954 0 0 1 202.327 84.215 286.014 286.014 0 1 1-404.656 0 284.954 284.954 0 0 1 202.328-84.215z m31.78 335.449v-191.56a31.78 31.78 0 1 0-63.56 0v192.09a31.78 31.78 0 1 0 63.56 0z m-31.78 128a32.132 32.132 0 1 0-31.78-31.603 32.132 32.132 0 0 0 31.78 32.132z"
    />
  </svg>
</template>

<script>
/**
 * 敏感 / 损坏图片的占位图标（用户指定资源）。
 *
 * 抽成组件而不是内联进 TicketAttachment.vue 的理由：
 *   1. 同一个图标还要给 PLUS 的 chaoxing 面板与将来的工单详情复用，
 *      内联会让三处各自维护一份 path（AGENTS.md 第 13 条的同源问题）；
 *   2. 原文件带 DOCTYPE，抽取过程容易出错，集中在一处便于测试守住
 *      「viewBox 是 1271 1024」「fill 是 currentColor」「只有一条 path」。
 *
 * props:
 * - `label` 可访问性名称（默认「图片已遮挡」）；内容由调用方决定，
 *   因为「敏感」「检测中」「检测失败」在 UI 上是三种不同文案。
 *
 * 无障碍：role="img" + aria-label 提供语义，aria-hidden="true" 保证
 * 屏幕阅读器不会把这条纯装饰路径再读一遍 —— 与 TicketAttachment 的原语义一致，
 * 真正的说明文字由占位块里的 `<p>` 承担。
 */
export default {
  name: 'SensitiveImageIcon',
  props: {
    label: { type: String, default: '图片已遮挡' },
  },
};
</script>

<style scoped>
/*
 * 显示尺寸：原图宽高比 1271:1024 ≈ 1.24:1（横向长方形）。
 * 用 width 定尺寸 + height:auto 保持比例，**不要**同时写死 width/height，
 * 否则不同缩放下会被拉伸。24px 是占位块里与 12px 说明文字匹配的视觉尺寸。
 */
.sensitive-image-icon {
  display: block;
  width: 32px;
  height: auto;
  color: var(--console-muted, #8c8c8c);
  fill: currentColor;
}
</style>
