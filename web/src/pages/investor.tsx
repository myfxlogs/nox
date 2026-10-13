// investor.tsx —— 投资人页 v0（NOX 露面②）：只读 NAV/收益/持仓摘要，零写径零运维件。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { PositionDailyIncome } from "../gen/dashboardv1/account_pb";
import type { Position } from "../gen/dashboardv1/position_pb";
import type { RequestBoxItem } from "../gen/dashboardv1/request_box_pb";
import { ResolveRequestState } from "../gen/dashboardv1/request_box_pb";
import { NOX_NAME, NOX_REPORT_FOOTER } from "../nox";
import { fmtUsd, fmtAge } from "../components/fmt";

export default function InvestorPage() {
  const [nav, setNav] = useState<number | null>(null);
  const [realized, setRealized] = useState<number | null>(null);
  const [today, setToday] = useState<number | null>(null);
  const [floatNet, setFloatNet] = useState<number | null>(null);
  const [ann30, setAnn30] = useState<number | null>(null);
  const [income, setIncome] = useState<PositionDailyIncome[]>([]);
  const [positions, setPositions] = useState<Position[]>([]);
  const [box, setBox] = useState<RequestBoxItem[]>([]);
  const [busy, setBusy] = useState<number | null>(null);
  const [err, setErr] = useState("");

  useEffect(() => {
    (async () => {
      try {
        const [fin, pnl, inc, pos, bx] = await Promise.all([
          client.getFinanceOverview({}),
          client.getPnlAnchor({}),
          client.listDailyIncome({ limit: 30 }),
          client.listPositions({ status: "ACTIVE", limit: 100 }),
          client.listRequestBox({}),
        ]);
        setNav(fin.totalEquityUsd);
        setRealized(pnl.realizedSinceAnchor);
        setToday(pnl.realizedToday);
        setFloatNet(pnl.floatingNetUsd);
        setAnn30(pnl.netAnn30dPct);
        setIncome(inc.items);
        setPositions(pos.positions);
        setBox(bx.items);
        setErr("");
      } catch (e) {
        setErr(errText(e));
      }
    })();
  }, []);

  // D-268 M4.6：请求箱回执——按钮只发 id+二态，态机判权全后端（前端零信任）。
  const resolve = async (id: number, state: ResolveRequestState) => {
    setBusy(id);
    try {
      await client.resolveRequest({ id: BigInt(id), state });
      const bx = await client.listRequestBox({});
      setBox(bx.items);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(null);
    }
  };

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

      <h2>请求箱{box.length > 0 ? `（${box.length}）` : ""}</h2>
      {box.length === 0 ? (
        <div className="muted">无待办请求</div>
      ) : (
        <table>
          <thead><tr><th>#</th><th>种类</th><th>模板</th><th>对象</th><th>请求</th><th>龄</th><th></th></tr></thead>
          <tbody>
            {box.map((r) => (
              <tr key={Number(r.id)}>
                <td>{Number(r.id)}</td>
                <td>{r.kind}</td>
                <td>{r.templateId}</td>
                <td>{r.subject}</td>
                <td className="muted">{r.requestText}</td>
                <td>{fmtAge(Number(r.ageSeconds))}</td>
                <td>
                  <button disabled={busy === Number(r.id)}
                    onClick={() => resolve(Number(r.id), ResolveRequestState.FULFILLED)}>已喂条件</button>
                  {" "}
                  <button disabled={busy === Number(r.id)}
                    onClick={() => resolve(Number(r.id), ResolveRequestState.WAIVED)}>放弃</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

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
