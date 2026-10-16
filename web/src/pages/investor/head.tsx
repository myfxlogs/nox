// investor/head.tsx —— 头版+NAV/业绩卡阵（第一眼层级·健康面：业主第一眼=多少钱）。
// 纯呈现：全字段后端算定值直渲，前端零重算。
import { useState } from "react";
import { StatTile } from "../../components/StatTile";
import { fmtUsd } from "../../components/fmt";
import { NOX_NAME } from "../../nox";

export interface PerfLine {
  baseline: number; at: Date | null; principal: number;
  netFlow: number; perf: number; ann: number; provisional: boolean;
}

// STABLE 结算币种白名单（USD/USDT/USDC 显 $ 面值；杂币=原生量桶——
// D-528 零换算，禁冒 $ 前缀，「NFT $51.7M」幻影病灶教训）。
const STABLE = new Set(["USD", "USDT", "USDC"]);

export function InvestorHead(p: {
  nav: number | null; navCcy: Record<string, number>;
  realized: number | null; today: number | null; floatNet: number | null;
  ann30: number | null; perf: PerfLine | null; posCount: number;
}) {
  const [showDust, setShowDust] = useState(false); // NAV 尘币折叠
  const fmtRaw = (v: number) => v.toLocaleString("en-US", { maximumFractionDigits: 2 });
  const entries = Object.entries(p.navCcy).filter(([, v]) => v !== 0);
  const stable = entries.filter(([c]) => STABLE.has(c)).sort();
  const other = entries.filter(([c]) => !STABLE.has(c)).sort((a, b) => Math.abs(b[1]) - Math.abs(a[1]));
  const shown = other.slice(0, 3);
  const dust = other.slice(3);
  return (
    <>
      <div className="investor-head">
        <h1>{NOX_NAME} 投资人报告</h1>
        <div className="muted">只读摘要 · 净值与收益由后端账本口径算定</div>
      </div>
      {/* D-535：业绩基线面（本金口径 USDT+USD——后端算定，前端零算） */}
      <div className="cards">
        <div className="card">
          <div className="k">NAV 总权益</div>
          <div className="v">{fmtUsd(p.nav)}</div>
          {stable.map(([c, v]) => (
            <div key={c} className="muted" style={{ fontSize: "12px" }}>NAV {c} = {fmtUsd(v)} {c}</div>
          ))}
          {shown.map(([c, v]) => (
            <div key={c} className="muted" style={{ fontSize: "12px" }}>NAV {c} = {fmtRaw(v)} {c}（原生量）</div>
          ))}
          {dust.length > 0 && (
            <div className="muted" style={{ fontSize: "12px" }}>
              <a href="#" onClick={(e) => { e.preventDefault(); setShowDust(!showDust); }}>
                其他 {dust.length} 币 {showDust ? "▾" : "▸"}
              </a>
              {showDust && dust.map(([c, v]) => (
                <div key={c}>NAV {c} = {fmtRaw(v)} {c}</div>
              ))}
            </div>
          )}
        </div>
        <StatTile label="锚点以来已实现" value={fmtUsd(p.realized)} />
        <StatTile label="今日已实现" value={fmtUsd(p.today)} />
        <StatTile label="浮动净值" value={fmtUsd(p.floatNet)} />
        <StatTile label="30d 净年化" value={p.ann30 == null ? "—" : p.ann30.toFixed(2) + "%"} />
        <StatTile label="在持仓位" value={String(p.posCount)} />
        {/* D-535：业绩基线（10-15 锚 $11,317 起算；净流扣减 provisional 标） */}
        <div className="card">
          <div className="k">本金池（USDT+USD）</div>
          <div className="v">{p.perf ? fmtUsd(p.perf.principal) : "—"}</div>
          {p.perf && (
            <div className="muted" style={{ fontSize: "12px" }}>
              基线 {fmtUsd(p.perf.baseline)}（{p.perf.at ? p.perf.at.toISOString().slice(0, 10) : "—"} 起算）
            </div>
          )}
        </div>
        <div className="card">
          <div className="k">基线后业绩{p.perf?.provisional ? "（provisional）" : ""}</div>
          <div className="v">{p.perf ? (p.perf.perf >= 0 ? "+" : "") + fmtUsd(p.perf.perf) : "—"}</div>
          {p.perf && (
            <div className="muted" style={{ fontSize: "12px" }}>
              净流扣减 {fmtUsd(p.perf.netFlow)} ｜ 年化 {p.perf.ann.toFixed(2)}%
            </div>
          )}
        </div>
      </div>
    </>
  );
}
