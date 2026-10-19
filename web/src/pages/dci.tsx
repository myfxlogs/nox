// dci.tsx —— 持仓与理财面板（财务可见性两件套②）：在跑仓（personal positions OPEN）
// + DCI/earn 订单历史（fin_dci_order 读面）。纯渲染后端行，零运算。
// S3′-final 批迁：第一眼=在跑仓 opp-card 阵（卡=仓单资产/类型/量/入场/时点）；
// DCI/earn 订单=13 列台账密表保留（记录网格正当形态）。纯 JSX/CSS 重排零逻辑动。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { PersonalPositionRow, DCIOrderRow } from "../gen/dashboardv1/dci_panel_pb";
import { tsUnix } from "../components/fmt";
import { StatTile } from "../components/StatTile";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";

function fmtAmt(v: number): string {
  return v.toLocaleString("en-US", { maximumFractionDigits: 8 });
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
    <div>
      <h1>持仓与理财 <small className="u-dim" style={{ fontSize: 12 }}>在跑仓 + DCI/earn 订单账本</small></h1>
      {err && <div className="err">{err}</div>}

      {/* 第一眼=在跑仓卡阵（仓单主体）；订单账本密表居后 */}
      <h2>在跑仓</h2>
      {positions.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {positions.map((p) => (
            <li key={String(p.id)} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">{p.asset}</span>
                <Chip tone={p.status === "OPEN" ? "good" : "neutral"}>{p.status}</Chip>
                <span className="opp-card-who">{p.kind}</span>
              </div>
              <div className="opp-card-stats stats">
                <StatTile label="数量" value={fmtAmt(p.qty)} />
                <StatTile label="入场价" value={`${fmtAmt(p.entryPrice)} ${p.priceSymbol}`} />
              </div>
              <div className="opp-card-narrative">
                #{String(p.id)} · {p.note || "—"} · 开 {tsUnix(p.openedAt)}
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零在跑仓</EmptyState>
      )}

      <h2>DCI/earn 订单</h2>
      {orders.length > 0 ? <OrdersTable rows={orders} /> : <EmptyState>零订单</EmptyState>}
    </div>
  );
}
