import { defineComponent, h } from 'vue'
import { parseSafePaymentIcon } from '@/utils/safePaymentIcon.mjs'

// Reconstruct only validated VNodes. Never compile or inject raw SVG markup.
export default defineComponent({
  name: 'SafePaymentIcon',
  props: { value: { type: String, default: '' }, placeholder: { type: String, default: '支' } },
  setup(props) {
    const renderNode = node => h(node.tag, node.attrs, node.children.map(renderNode))
    return () => {
      const node = parseSafePaymentIcon(props.value)
      return node ? renderNode(node) : h('span', { class: 'payment-method-icon--placeholder', 'data-payment-icon-placeholder': '', 'aria-label': '支付图标占位' }, props.placeholder)
    }
  },
})
