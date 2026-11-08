import { defineComponent, h } from 'vue'
import { parseSafePaymentIcon } from '@/utils/safePaymentIcon.mjs'

// Reconstruct only validated VNodes. Never compile or inject raw SVG markup.
export default defineComponent({
  name: 'SafePaymentIcon',
  props: { iconSvg: { type: String, default: undefined }, fallback: { type: String, default: undefined }, value: { type: String, default: '' }, placeholder: { type: String, default: '支' } },
  setup(props) {
    const renderNode = node => h(node.tag, node.attrs, node.children.map(renderNode))
    return () => {
      const node = parseSafePaymentIcon(props.iconSvg ?? props.value)
      return h('span', { class: 'safe-payment-icon', style: { display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: '28px', height: '28px', flexShrink: 0 } }, [
        node ? h('svg', { ...node.attrs, style: { width: '100%', height: '100%', display: 'block' } }, node.children.map(renderNode)) : h('span', { class: 'payment-method-icon--placeholder', 'data-payment-icon-placeholder': '', 'aria-label': '支付图标占位' }, props.fallback ?? props.placeholder),
      ])
    }
  },
})
