// CapitalCoverageCard.tsx —— 资本覆盖率全形卡（arb 同名件借形 D-359）：
// GetCapitalCoverage 总行+逐池行直渲（balance/apr/status/note/productive/
// real_money 后端字段零计算——has_* 布尔对语义守：false=「—」不冒充 0）。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { CapitalCoverageRow } from "../gen/dashboardv1/ops_pb";
import { StatTile } from "./StatTile";
import { EmptyState } from "./EmptyState";

export function CapitalCoverageCard() {
  const [rows, setRows] = useState<CapitalCoverageRow[]>([]);
  const [total, setTotal] = useState(0);
  const [pct, setPct] = useState(0);
  const [note, setNote] = useState("");
  const [err, setErr] = useState("");
  useEffect(() => {
    (async () => {
      try {
        const r = await client.getCapitalCoverage({});
        setRows(r.rows); setTotal(r.totalUsd); setPct(r.coveragePct); setNote(r.caliberNote);
      } catch (e) { setErr(errText(e)); }
    })();
  }, []);
  return (
    <section>
      <h2>覆盖率明细 <span className="muted">{note}</span></h2>
      {err && <div className="err">{err}</div>}
      <div className="stats">
        <StatTile label="总资金" value={`$${total.toFixed(0)}`} />
        <StatTile label="覆盖率" value={`${pct.toFixed(1)}%`} />
      </div>
      {rows.length > 0 ? (
        <table className="rows">
          <thead><tr><th>池</th><th>余额</th><th>币种</th><th>APR</th><th>在产</th><th>真金</th><th>状态/注</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.pool}>
                <td>{r.pool}</td>
                <td>{r.hasBalance ? r.balanceUsd.toFixed(2) : "—"}</td>
                <td>{r.balanceCcy || "USD"}</td>
                <td>{r.hasApr ? (r.aprText || r.aprPct.toFixed(1) + "%") : (r.aprText || "—")}</td>
                <td>{r.productive ? "✓" : "—"}</td>
                <td>{r.realMoney ? "真金" : "纸面"}</td>
                <td className="muted">{r.status}{r.note ? ` ${r.note}` : ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <EmptyState>零覆盖率行</EmptyState>
      )}
      {/* 画骨批三带入： productive/real_money/apr 直读 Chip 语义 */}
    </section>
  );
}
