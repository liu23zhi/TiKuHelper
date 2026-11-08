import { createApp, h } from 'vue';
import {
  AlipayOutlined,
  AlipayCircleOutlined,
  WechatOutlined,
  CreditCardOutlined,
  BankOutlined,
  WalletOutlined,
  PayCircleOutlined,
  PayCircleFilled,
  DollarCircleOutlined,
  SafetyCertificateOutlined,
  QrcodeOutlined,
  TransactionOutlined,
  AccountBookOutlined,
  MoneyCollectOutlined,
  ShopOutlined,
  GiftOutlined,
  MobileOutlined,
} from '@ant-design/icons-vue';

// Only trusted static components. Freeze metadata, not Vue component internals.
export const paymentIconCatalog = Object.freeze([
  Object.freeze({ id: 'antd:alipay:v1', label: '支付宝', component: AlipayOutlined }),
  Object.freeze({ id: 'antd:alipay-circle:v1', label: '支付宝圆标', component: AlipayCircleOutlined }),
  Object.freeze({ id: 'antd:wechat:v1', label: '微信', component: WechatOutlined }),
  Object.freeze({ id: 'antd:credit-card:v1', label: '银行卡', component: CreditCardOutlined }),
  Object.freeze({ id: 'antd:bank:v1', label: '银行', component: BankOutlined }),
  Object.freeze({ id: 'antd:wallet:v1', label: '钱包', component: WalletOutlined }),
  Object.freeze({ id: 'antd:pay-circle:v1', label: '支付', component: PayCircleOutlined }),
  Object.freeze({ id: 'antd:pay-circle-filled:v1', label: '支付实心', component: PayCircleFilled }),
  Object.freeze({ id: 'antd:dollar-circle:v1', label: '金额', component: DollarCircleOutlined }),
  Object.freeze({ id: 'antd:safety-certificate:v1', label: '安全认证', component: SafetyCertificateOutlined }),
  Object.freeze({ id: 'antd:qrcode:v1', label: '二维码', component: QrcodeOutlined }),
  Object.freeze({ id: 'antd:transaction:v1', label: '交易', component: TransactionOutlined }),
  Object.freeze({ id: 'antd:account-book:v1', label: '账本', component: AccountBookOutlined }),
  Object.freeze({ id: 'antd:money-collect:v1', label: '收款', component: MoneyCollectOutlined }),
  Object.freeze({ id: 'antd:shop:v1', label: '商店', component: ShopOutlined }),
  Object.freeze({ id: 'antd:gift:v1', label: '礼品', component: GiftOutlined }),
  Object.freeze({ id: 'antd:mobile:v1', label: '手机', component: MobileOutlined }),
]);
const catalogById = new Map(paymentIconCatalog.map(entry => [entry.id, entry]));

// Browser-only trusted-component serialization, not a sanitizer for legacy SVG.
// icons-vue may register one shared trusted stylesheet asynchronously.
export function serializePaymentIcon(id) {
  const entry = typeof id === 'string' ? catalogById.get(id) : undefined;
  if (!entry) throw new Error('UNKNOWN_PAYMENT_ICON');
  if (typeof document === 'undefined') throw new Error('PAYMENT_ICON_BROWSER_REQUIRED');
  const host = document.createElement('div');
  const app = createApp({ render: () => h(entry.component) });
  try {
    app.mount(host);
    const svgs = host.querySelectorAll('svg');
    if (svgs.length !== 1) throw new Error('PAYMENT_ICON_SVG_REQUIRED');
    const svg = svgs[0].cloneNode(true);
    svg.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
    return svg.outerHTML;
  } finally {
    try { app.unmount(); } finally { host.replaceChildren(); }
  }
}
