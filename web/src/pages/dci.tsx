// dci.tsx —— 持仓与理财面板（财务可见性两件套②）：在跑仓（personal positions OPEN）
// + DCI/earn 订单历史（fin_dci_order 读面）。纯渲染后端行，零运算。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { PersonalPositionRow, DCIOrderRow } from "../gen/dashboardv1/dci_panel_pb";
import { tsUnix } from "../components/fmt";

function fmtAmt(v: number): string {
  return v.toLocaleString("en-US", { maximumFractionDigits: 8 });
}

function PositionsTable({ rows }: { rows: PersonalPositionRow[] }) {
  return (
    <table>
      <thead>
        <tr>
          <th>#</th><th>资产</th><th>类型</th><th>数量</th><th>入场价</th>
          <th>状态</th><th>备注</th><th>开仓时点</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((p) => (
          <tr key={String(p.id)}>
            <td>{String(p.id)}</td>
            <td>{p.asset}</td>
            <td>{p.kind}</td>
            <td>{fmtAmt(p.qty)}</td>
            <td>{fmtAmt(p.entryPrice)} {p.priceSymbol}</td>
            <td>{p.status}</td>
            <td>{p.note || "—"}</td>
            <td>{tsUnix(p.openedAt)}</td>
          </tr>
        ))}
        {rows.length === 0 && <tr><td colSpan={8}>零在跑仓</td></tr>}
      </tbody>
    </table>
  );
}

function OrdersTable({ rows }: { rows: DCIOrderRow[] }) {
  return (
    <table>
      <thead>
        <tr>
          <th>#</th><th>场所</th><th>模板</th><th>产品</th><th>类型</th>
          <th>行权价</th><th>数量</th><th>币种</th><th>期天</th>
          <th>状态</th><th>回执</th><th>提案</th><th>创建时点</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((o) => (
          <tr key={String(o.id)}>
            <td>{String(o.id)}</td>
            <td>{o.venue}</td>
            <td>{o.templateId}</td>
            <td>{o.productRef}</td>
            <td>{o.optType}</td>
            <td>{o.strike ? fmtAmt(o.strike) : "—"}</td>
            <td>{fmtAmt(o.qty)}</td>
            <td>{o.investCcy}</td>
            <td>{o.tenorDays ? String(o.tenorDays) : "—"}</td>
            <td>{o.status}</td>
            <td title={o.response}>{o.response ? o.response.slice(0, 60) : "—"}</td>
            <td>{o.proposalId ? `#${o.proposalId}` : "—"}</td>
            <td>{tsUnix(o.createdAt)}</td>
          </tr>
        ))}
        {rows.length === 0 && <tr><td colSpan={13}>零订单</td></tr>}
      </tbody>
    </table>
  );
}

export default function DciPage() {
  const [positions, setPositions] = useState<PersonalPositionRow[]>([]);
  const [orders, setOrders] = useState<DCIOrderRow[]>([]);
  const [err, setErr] = useState("");

  useEffect(() => {
    (async () => {
      try {
        const [p, d] = await Promise.all([
          client.listPersonalPositions({ status: "OPEN" }),
          client.listDCIOrders({}),
        ]);
        setPositions(p.positions);
        setOrders(d.orders);
        setErr("");
      } catch (e) {
        setErr(errText(e));
      }
    })();
  }, []);

  return (
    <main>
      <h2>持仓与理财 <small style={{ fontSize: 12, color: "#777" }}>在跑仓 + DCI/earn 订单账本</small></h2>
      {err && <div className="err">{err}</div>}
      <h3>在跑仓</h3>
      <PositionsTable rows={positions} />
      <h3>DCI/earn 订单</h3>
      <OrdersTable rows={orders} />
    </main>
  );
}
