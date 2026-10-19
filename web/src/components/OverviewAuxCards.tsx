// OverviewAuxCards.tsx —— 概览纵深两卡（arb OverviewPagePnLOverviewCard+
// OverviewPageStrategyLedgerCard 借形）：PnL 锚点（anchor/当日已实现/浮动净/
// 30d 年化全后端下发直读）+策略台账（per-strategy 行——承诺/实际/持仓数）。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { GetPnlAnchorResponse } from "../gen/dashboardv1/account_pb";
import type { StrategyLedgerEntry } from "../gen/dashboardv1/broker_admin_pb";
import { StatTile } from "./StatTile";
import { EmptyState } from "./EmptyState";
import { ts, pnlClass, fmtUsd } from "./fmt";

export function PnlOverviewCard() {
  const [r, setR] = useState<GetPnlAnchorResponse | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    (async () => {
      try { setR(await client.getPnlAnchor({})); } catch (e) { setErr(errText(e)); }
    })();
  }, []);
  if (err) return <div className="err">{err}</div>;
  if (!r) return null;
  return (
    <div className="stats">
      {/* PnL 正绿负红（色彩差量批 10-19——pnlClass=呈现层映射非判据；30d 年化零新阈值不着色） */}
      <StatTile label="锚点以来已实现" value={fmtUsd(r.realizedSinceAnchor)} sub={`锚 ${ts(r.anchor)}`}
        valueClass={pnlClass(r.realizedSinceAnchor, true)} />
      <StatTile label="当日已实现" value={fmtUsd(r.realizedToday)}
        valueClass={pnlClass(r.realizedToday, true)} />
      <StatTile label="浮动净额" value={fmtUsd(r.floatingNetUsd)} sub={r.floatingAvailable ? undefined : "净估缺"}
        valueClass={pnlClass(r.floatingNetUsd, r.floatingAvailable)} />
      <StatTile label="30d 净年化" value={r.netAnn30dAvailable ? `${r.netAnn30dPct.toFixed(1)}%` : "—"} />
    </div>
  );
}

export function StrategyLedgerCard() {
  const [rows, setRows] = useState<StrategyLedgerEntry[]>([]);
  const [err, setErr] = useState("");
  useEffect(() => {
    (async () => {
      try { setRows((await client.getStrategyLedger({})).entries); } catch (e) { setErr(errText(e)); }
    })();
  }, []);
  if (err) return <div className="err">{err}</div>;
  if (rows.length === 0) return <EmptyState>零策略台账行</EmptyState>;
  return (
    <table className="rows">
      <thead><tr><th>策略</th><th>持仓</th><th>承诺年化</th><th>实际年化</th><th>已实现 PnL</th><th>均持仓天</th></tr></thead>
      <tbody>
        {rows.map((e) => (
          <tr key={e.strategy}>
            <td>{e.strategy}</td>
            <td>{e.openPositions}/{e.totalPositions}</td>
            <td>{e.promisedAnnualPct.toFixed(1)}%</td>
            <td>{e.actualAnnualPct.toFixed(1)}%</td>
            <td className={pnlClass(e.realizedPnlUsd, true)} data-label="已实现$">
              {fmtUsd(e.realizedPnlUsd)}
            </td>
            <td>{e.avgHoldDays.toFixed(1)}d</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
