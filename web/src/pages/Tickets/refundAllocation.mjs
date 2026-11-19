// 退款金额在多个充值订单之间的分摊（2026-11-19 用户要求）。
//
// 用户原话：「假如我要退款10块钱，但是支付记录有两笔，每笔都7.5，这个时候要
// 允许用户选择两笔订单。用户可以自定义输入退回多少，默认情况下，最旧的那笔
// 全额退回，然后最新的部分退款……因为两笔的退款金额已经达到了申请退款金额，
// 所以不能选择第三笔。用户可以修改退款金额，以选择第三笔。需要注意的是，
// 避免覆盖用户手动修改的金额。」
//
// 抽成纯函数而不是写在组件里：这套规则的每一条都会出错（尤其是「不覆盖手改」），
// 而写在 .vue 里的逻辑无法用 node 单测覆盖 —— 只能靠人点界面看，那不叫验证。

// computeRefundAllocations 重算分摊表。
//
// 入参：
//   orders         —— 全部可退订单 [{ id, paidCents, paidAt }]（顺序不要求）
//   requestedCents —— 申请退款总额（分）
//   selections     —— 当前选中状态 [{ orderId, cents, manual }]
//   addedOrderId   —— 本次新勾选的订单；已有手改行时只给新行分配剩余额度
//
// 返回：新的 selections 数组，按「最旧优先」排序。
//
// 规则：
//   1. manual=true 的行**原样保留**，任何情况下都不被改写；
//   2. 其余额度按 paidAt 升序分配：较旧的先全额，最后一笔取剩余；
//   3. 自动分配不超过订单可退额度，非整分额度向下取整；
//   4. 手改总额超过申请金额时仍保留，由表单提示冲突；自动行不再分配金额。
export function computeRefundAllocations({ orders, requestedCents, selections, addedOrderId }) {
  const list = Array.isArray(selections) ? selections : [];
  if (!list.length) return [];

  const orderById = new Map(
    (Array.isArray(orders) ? orders : []).map(order => [Number(order.id), order]),
  );

  // 手改行的金额先占额度。顺序很重要：先扣 manual 再分配，
  // 否则手改值会被当成"尚未分配"而重算掉 —— 那正是用户抱怨的覆盖。
  const manualTotal = list
    .filter(item => item.manual)
    .reduce((sum, item) => sum + (Number(item.cents) || 0), 0);
  let remaining = (Number(requestedCents) || 0) - manualTotal;

  // 最旧优先；同一时刻按 id 稳定排序，否则同一份输入两次调用可能给出不同顺序。
  const ordered = [...list].sort((left, right) => {
    const a = orderById.get(Number(left.orderId));
    const b = orderById.get(Number(right.orderId));
    const byTime = (Date.parse(a?.paidAt) || 0) - (Date.parse(b?.paidAt) || 0);
    return byTime !== 0 ? byTime : Number(left.orderId) - Number(right.orderId);
  });

  const orderCap = item => {
    const cap = Number(orderById.get(Number(item.orderId))?.paidCents);
    return Number.isFinite(cap) ? Math.max(0, Math.floor(cap)) : 0;
  };

  // 手改某行后腾出的空间留给新勾选的订单，不能又自动填回原有行。
  // 没有手改行时仍按旧到新重排，以支持先选新订单、再选旧订单。
  const added = list.find(item => Number(item.orderId) === Number(addedOrderId));
  if (list.some(item => item.manual) && added && !added.manual) {
    const preserved = ordered.map(item => item.manual
      ? { ...item }
      : { orderId: Number(item.orderId), cents: Math.min(orderCap(item), Math.max(0, Number(item.cents) || 0)), manual: false });
    const allocated = preserved.reduce((sum, item) => Number(item.orderId) === Number(addedOrderId)
      ? sum
      : sum + (Number(item.cents) || 0), 0);
    return preserved.map(item => Number(item.orderId) === Number(addedOrderId)
      ? { ...item, cents: Math.min(orderCap(item), Math.max(0, (Number(requestedCents) || 0) - allocated)) }
      : item);
  }

  return ordered.map(item => {
    if (item.manual) return { ...item };
    const cap = orderCap(item);
    const cents = Math.min(cap, Math.max(0, remaining));
    remaining -= cents;
    return { orderId: Number(item.orderId), cents, manual: false };
  });
}

// refundAllocationTotals 汇总已分配 / 剩余。
export function refundAllocationTotals(requestedCents, selections) {
  const allocated = (Array.isArray(selections) ? selections : []).reduce(
    (sum, item) => sum + (Number(item.cents) || 0),
    0,
  );
  const requested = Number(requestedCents) || 0;
  return {
    requested,
    allocated,
    remaining: Math.max(0, requested - allocated),
    excess: Math.max(0, allocated - requested),
  };
}
