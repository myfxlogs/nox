// investor.tsx —— 投资人页 v0（NOX 露面②）：只读 NAV/收益/持仓摘要，零写径零运维件。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { PositionDailyIncome } from "../gen/dashboardv1/account_pb";
import type { Position } from "../gen/dashboardv1/position_pb";
import { NOX_NAME, NOX_REPORT_FOOTER } from "../nox";
import { fmtUsd } from "../components/fmt";

export default function InvestorPage() {
  const [nav, setNav] = useState<number | null>(null);
  const [realized, setRealized] = useState<number | null>(null);
  const [today, setToday] = useState<number | null>(null);
  const [floatNet, setFloatNet] = useState<number | null>(null);
  const [ann30, setAnn30] = useState<number | null>(null);
  const [income, setIncome] = useState<PositionDailyIncome[]>([]);
  const [positions, setPositions] = useState<Position[]>([]);
  const [err, setErr] = useState("");

  useEffect(() => {
    (async () => {
      try {
        const [fin, pnl, inc, pos] = await Promise.all([
          client.getFinanceOverview({}),
          client.getPnlAnchor({}),
          client.listDailyIncome({ limit: 30 }),
          client.listPositions({ status: "ACTIVE", limit: 100 }),
        ]);
        setNav(fin.totalEquityUsd);
        setRealized(pnl.realizedSinceAnchor);
        setToday(pnl.realizedToday);
        setFloatNet(pnl.floatingNetUsd);
        setAnn30(pnl.netAnn30dPct);
        setIncome(inc.items);
        setPositions(pos.positions);
        setErr("");
      } catch (e) {
        setErr(errText(e));
      }
    })();
  }, []);

  return (
    <div>
      <div className="investor-head">
        <h1>{NOX_NAME} 投资人报告</h1>
        <div className="muted">只读摘要 · 净值与收益由后端账本口径算定</div>
      </div>
      {err && <div className="err">{err}</div>}
      <div className="cards">
        <div className="card"><div className="k">NAV 总权益</div><div className="v">{fmtUsd(nav)}</div></div>
        <div className="card"><div className="k">锚点以来已实现</div><div className="v">{fmtUsd(realized)}</div></div>
        <div className="card"><div className="k">今日已实现</div><div className="v">{fmtUsd(today)}</div></div>
        <div className="card"><div className="k">浮动净值</div><div className="v">{fmtUsd(floatNet)}</div></div>
        <div className="card"><div className="k">30d 净年化</div><div className="v">{ann30 == null ? "—" : ann30.toFixed(2) + "%"}</div></div>
        <div className="card"><div className="k">在持仓位</div><div className="v">{positions.length}</div></div>
      </div>

      <h2>持仓摘要</h2>
      <table>
        <thead><tr><th>ID</th><th>类型</th><th>净盈亏</th><th>浮动</th><th>持有天数</th></tr></thead>
        <tbody>
          {positions.map((p) => (
            <tr key={Number(p.id)}>
              <td>{Number(p.id)}</td><td>{p.type}</td>
              <td className={(p.netPnlUsd ?? 0) >= 0 ? "good" : "crit"}>{fmtUsd(p.netPnlUsd)}</td>
              <td>{fmtUsd(p.floatingPnlUsd)}</td>
              <td>{p.holdDays?.toFixed(1) ?? "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>日收益（近 30 条）</h2>
      <table>
        <thead><tr><th>日</th><th>收益</th><th>pnl</th><th>note</th></tr></thead>
        <tbody>
          {income.map((d) => (
            <tr key={Number(d.id)}>
              <td>{d.day}</td>
              <td className={d.incomeUsd >= 0 ? "good" : "crit"}>{fmtUsd(d.incomeUsd)}</td>
              <td>{fmtUsd(d.pnlUsd)}</td>
              <td className="muted">{d.note}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="footer-note">{NOX_REPORT_FOOTER}</div>
    </div>
  );
}
