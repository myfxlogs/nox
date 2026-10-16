// investor.tsx —— 投资人页 v0（NOX 露面②）：只读 NAV/收益/持仓摘要，零写径零运维件。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { PositionDailyIncome } from "../gen/dashboardv1/account_pb";
import type { Position } from "../gen/dashboardv1/position_pb";
import type { RequestBoxItem } from "../gen/dashboardv1/request_box_pb";
import { ResolveRequestState } from "../gen/dashboardv1/request_box_pb";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { NOX_NAME, NOX_REPORT_FOOTER } from "../nox";
import { fmtUsd, fmtAge } from "../components/fmt";
import GraduationPanel from "../components/graduation";
import { Collapse } from "../components/collapse";
import type { DCIOrderRow } from "../gen/dashboardv1/dci_panel_pb";

export default function InvestorPage() {
  const [nav, setNav] = useState<number | null>(null);
  const [navCcy, setNavCcy] = useState<Record<string, number>>({}); // D-522④：NAV 分币种
  const [realized, setRealized] = useState<number | null>(null);
  const [today, setToday] = useState<number | null>(null);
  const [floatNet, setFloatNet] = useState<number | null>(null);
  const [ann30, setAnn30] = useState<number | null>(null);
  const [perf, setPerf] = useState<{
    baseline: number; at: Date | null; principal: number;
    netFlow: number; perf: number; ann: number; provisional: boolean;
  } | null>(null);
  const [income, setIncome] = useState<PositionDailyIncome[]>([]);
  const [positions, setPositions] = useState<Position[]>([]);
  const [box, setBox] = useState<RequestBoxItem[]>([]);
  const [busy, setBusy] = useState<number | null>(null);
  const [err, setErr] = useState("");
  const [showDust, setShowDust] = useState(false); // NAV 尘币折叠
  const [orders, setOrders] = useState<DCIOrderRow[]>([]); // DCI/earn 收益轨透出（D-577）

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
        setNavCcy(fin.equityByCcy ?? {});
        // D-535：业绩基线面（本金口径 USDT+USD——后端算定，前端零算）。
        if (fin.perfAvailable) {
          setPerf({
            baseline: fin.baselineUsd,
            at: fin.baselineAt ? timestampDate(fin.baselineAt) : null,
            principal: fin.principalNowUsd,
            netFlow: fin.netExternalFlowUsd,
            perf: fin.perfUsd,
            ann: fin.perfAnnPct,
            provisional: fin.perfFlowProvisional,
          });
        }
        setRealized(pnl.realizedSinceAnchor);
        setToday(pnl.realizedToday);
        setFloatNet(pnl.floatingNetUsd);
        setAnn30(pnl.netAnn30dPct);
        setIncome(inc.items);
        setPositions(pos.positions);
        setBox(bx.items);
        const od = await client.listDCIOrders({});
        setOrders(od.orders);
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
        <div className="card"><div className="k">NAV 总权益</div><div className="v">{fmtUsd(nav)}</div>
          {/* D-522④：结算币种分账行——USD/USDT 稳币显 $ 面值；杂币=原生量
              桶（D-528 零换算）禁冒 $ 前缀（NFT「$51.7M」幻影病灶修），尘币折叠。 */}
          {(() => {
            const STABLE = new Set(["USD", "USDT", "USDC"]);
            const entries = Object.entries(navCcy).filter(([, v]) => v !== 0);
            const stable = entries.filter(([c]) => STABLE.has(c)).sort();
            const other = entries.filter(([c]) => !STABLE.has(c)).sort((a, b) => Math.abs(b[1]) - Math.abs(a[1]));
            const shown = other.slice(0, 3);
            const dust = other.slice(3);
            const fmtRaw = (v: number) =>
              v.toLocaleString("en-US", { maximumFractionDigits: 2 });
            return (
              <>
                {stable.map(([c, v]) => (
                  <div key={c} className="muted" style={{fontSize: "12px"}}>NAV {c} = {fmtUsd(v)} {c}</div>
                ))}
                {shown.map(([c, v]) => (
                  <div key={c} className="muted" style={{fontSize: "12px"}}>NAV {c} = {fmtRaw(v)} {c}（原生量）</div>
                ))}
                {dust.length > 0 && (
                  <div className="muted" style={{fontSize: "12px"}}>
                    <a href="#" onClick={(e) => { e.preventDefault(); setShowDust(!showDust); }}>
                      其他 {dust.length} 币 {showDust ? "▾" : "▸"}
                    </a>
                    {showDust && dust.map(([c, v]) => (
                      <div key={c}>NAV {c} = {fmtRaw(v)} {c}</div>
                    ))}
                  </div>
                )}
              </>
            );
          })()}
        </div>
        <div className="card"><div className="k">锚点以来已实现</div><div className="v">{fmtUsd(realized)}</div></div>
        <div className="card"><div className="k">今日已实现</div><div className="v">{fmtUsd(today)}</div></div>
        <div className="card"><div className="k">浮动净值</div><div className="v">{fmtUsd(floatNet)}</div></div>
        <div className="card"><div className="k">30d 净年化</div><div className="v">{ann30 == null ? "—" : ann30.toFixed(2) + "%"}</div></div>
        {/* D-535：业绩基线（10-15 锚 $11,317 起算；净流扣减 provisional 标） */}
        <div className="card"><div className="k">本金池（USDT+USD）</div><div className="v">{perf ? fmtUsd(perf.principal) : "—"}</div>
          {perf && <div className="muted" style={{fontSize: "12px"}}>基线 {fmtUsd(perf.baseline)}（{perf.at ? perf.at.toISOString().slice(0, 10) : "—"} 起算）</div>}
        </div>
        <div className="card"><div className="k">基线后业绩{perf?.provisional ? "（provisional）" : ""}</div>
          <div className="v">{perf ? (perf.perf >= 0 ? "+" : "") + fmtUsd(perf.perf) : "—"}</div>
          {perf && <div className="muted" style={{fontSize: "12px"}}>净流扣减 {fmtUsd(perf.netFlow)} ｜ 年化 {perf.ann.toFixed(2)}%</div>}
        </div>
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

      {/* D-577 抽屉式分区：请求箱置顶不折；其余段入抽屉格默认折叠，
          标题+徽标行外显（一策略=一抽屉的分组键在归组 RPC 侧，本段先按
          区块分型抽屉——持仓/收益轨/毕业授权/收益流水） */}
      <Collapse title="毕业授权" hint={""} defaultOpen>
        <GraduationPanel />
      </Collapse>

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

      <Collapse title="持仓摘要" hint={positions.length + " 仓"}>

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
      </Collapse>

      <Collapse title="日收益（近 30 条）" hint={income.length + " 条"}>

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
      </Collapse>
      <div className="footer-note">{NOX_REPORT_FOOTER}</div>
    </div>
  );
}
