// investor/dci.tsx —— 收益轨（DCI/earn 成交订单透出，下钻段折叠）。
import type { DCIOrderRow } from "../../gen/dashboardv1/dci_panel_pb";
import { Collapse } from "../../components/collapse";

export function DciRail({ orders }: { orders: DCIOrderRow[] }) {
  return (
    <Collapse title="收益轨（DCI/earn 成交订单）" hint={orders.length + " 单"}>
      <table>
        <thead><tr><th>#</th><th>场所</th><th>模板</th><th>产品</th><th>数量</th><th>币种</th><th>状态</th><th>时点</th></tr></thead>
        <tbody>
          {orders.map((o) => (
            <tr key={String(o.id)}>
              <td>{String(o.id)}</td><td>{o.venue}</td><td>{o.templateId}</td>
              <td>{o.productRef}</td><td>{o.qty}</td><td>{o.investCcy}</td>
              <td>{o.status}</td><td>{new Date(Number(o.createdAt) * 1000).toLocaleString("zh-CN", { hour12: false })}</td>
            </tr>
          ))}
          {orders.length === 0 && <tr><td colSpan={8} className="muted">零订单</td></tr>}
        </tbody>
      </table>
    </Collapse>
  );
}
