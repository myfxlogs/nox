// overview.tsx —— S3′-a 默认首页重建（arb 骨架范式：StatTile 横排+
// 行动卡置顶+缩览卡阵；§0 待办→§1 KPI→§2 缩览→§3 风险心跳）。
// 前端零运算只渲染；缺数据显缺不显 0；时戳 UTC 口径（业主令 10-15）。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { GetOverviewBundleResponse, OverviewOrderRow } from "../gen/dashboardv1/account_pb";
import type { RequestBoxItem } from "../gen/dashboardv1/request_box_pb";
import { ResolveRequestState } from "../gen/dashboardv1/request_box_pb";
import { fmtUsd } from "../components/fmt";
import { StatTile } from "../components/StatTile";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";
import { FreshDot } from "../components/FreshDot";
import type { FreshDot as FreshDotData } from "../components/freshness";

// STAGE_CN 十段工序 key→中文名（显示映射非判据；新段 key 补表行）
const STAGE_CN: Record<string, string> = {
  collect: "采集", research: "研究", opp_publish: "机会发布", gate_eval: "闸评",
  assembly: "装配", execution: "执行", holding: "持仓", exit: "平仓",
  ledger: "账本", review: "复盘",
};

const utc = (t: Date | undefined) =>
  t ? t.toISOString().replace("T", " ").slice(0, 19) + "Z" : "—";

// orderTone 在仓订单态→徽标色（status 枚举显示映射非判据）
function orderTone(s: string): "good" | "warn" | "critical" | "neutral" {
  if (s === "open" || s === "holding" || s === "active") return "good";
  if (s === "pending" || s === "waiting") return "warn";
  if (s === "error" || s === "failed") return "critical";
  return "neutral";
}

export default function OverviewPage() {
  const [b, setB] = useState<GetOverviewBundleResponse | null>(null);
  const [nav, setNav] = useState<number | null>(null);
  const [box, setBox] = useState<RequestBoxItem[]>([]);
  const [busy, setBusy] = useState<number | null>(null);
  const [err, setErr] = useState("");

  const load = async () => {
    try {
      const [bd, bx, fin] = await Promise.all([
        client.getOverviewBundle({}),
        client.listRequestBox({}),
        client.getFinanceOverview({}), // NAV/敞口顶壳瓦片（既有 RPC 复用零新增）
      ]);
      setB(bd); setBox(bx.items); setNav(fin.totalEquityUsd); setErr("");
    } catch (e) { setErr(errText(e)); }
  };
  // D-536 复审收口：首载 RPC + 事件流驱动重取——账户事件/域刷新/持仓事件/告警
  // 任一到帧即拖尾重取 bundle（2s 拖尾合突发帧，venue 轮询节拍高频不连发）。
  useEffect(() => {
    void load();
    let t: number | undefined;
    const re = () => { window.clearTimeout(t); t = window.setTimeout(() => void load(), 2000); };
    const offs = [
      onCase("accountEvent", re),
      onCase("dataRefresh", re),
      onCase("brokerPositionEvent", re),
      onCase("position", re),
      onCase("alert", re),
      onCase("fundingNeed", re),
    ];
    return () => { window.clearTimeout(t); offs.forEach((f) => f()); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const resolve = async (id: number, state: ResolveRequestState) => {
    setBusy(id);
    try {
      await client.resolveRequest({ id: BigInt(id), state });
      await load();
    } catch (e) { setErr(errText(e)); } finally { setBusy(null); }
  };

  return (
    <div>
      <h1>总览</h1>
      {err && <div className="err">{err}</div>}

      {/* §0 行动卡置顶（业主第一眼=该我做的——待办横幅升 opp-card 卡阵态） */}
      {box.length > 0 && (
        <section className="card" style={{ borderColor: "#d97706" }} aria-labelledby="todo-h">
          <h2 id="todo-h">待办（{box.length}）</h2>
          <ul className="opp-cards">
            {box.map((r) => (
              <li key={Number(r.id)} className="opp-card">
                <div className="opp-card-head">
                  <Chip tone="warn">{r.kind}</Chip>
                  <span className="opp-card-kind">#{Number(r.id)} {r.subject}</span>
                </div>
                <p className="opp-card-narrative muted">{r.requestText}</p>
                <div>
                  <button disabled={busy === Number(r.id)} onClick={() => resolve(Number(r.id), ResolveRequestState.FULFILLED)}>已喂条件</button>{" "}
                  <button disabled={busy === Number(r.id)} onClick={() => resolve(Number(r.id), ResolveRequestState.WAIVED)}>放弃</button>
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}

      {/* §1 KPI 横排（StatTile 阵——NAV/可动用/在息/年化/敞口粗列） */}
      <h2>盘面</h2>
      <div className="stats">
        <StatTile label="NAV 总权益" value={nav != null ? fmtUsd(nav) : "—"} />
        <StatTile label="本金合计（USDT+USD）" value={b ? fmtUsd(b.principalTotalUsd) : "—"} />
        <StatTile label="可动用" value={b ? fmtUsd(b.principalIdleUsd) : "—"} />
        <StatTile label="在息" value={b ? fmtUsd(b.principalEarningUsd) : "—"}
          sub={b && b.principalLockedUsd > 0 ? `含锁定 ${fmtUsd(b.principalLockedUsd)}${b.earliestRelease ? "·最早回笼 " + utc(timestampDate(b.earliestRelease)) : ""}` : undefined} />
        <StatTile label="眠钱时长" value={b && b.idleStreakDays > 0 ? `${b.idleStreakDays} 天` : "—"}
          sub={b && b.idleStreakPool ? `最长池 ${b.idleStreakPool}` : "无在册破地板池"} />
        <StatTile label="基线后业绩" value={b?.perfAvailable ? (b.perfUsd >= 0 ? "+" : "") + fmtUsd(b.perfUsd) : "—"}
          sub={b?.perfAvailable ? `年化 ${b.perfAnnPct.toFixed(2)}%${b.perfFlowProvisional ? "（provisional）" : ""}` : "基线未配置"} />
      </div>

      {/* S3′-b 十段工序实时状态卡（后端 PipelineStageHealth 读模——
          状态点+一行实况叙事直渲；禁前端拼 dept/判故障） */}
      <h2>工序状态</h2>
      {b && b.stages.length > 0 ? (
        /* S3′-b 补正：流程图形态——十段小卡横排连线（闭环序读模原序直排） */
        <div className="pipe-flow">
          {b.stages.map((st) => {
            const tone: FreshDotData["status"] =
              st.state === "working" ? "live" : st.state === "waiting" ? "stale" : "down";
            return (
              <div key={st.key} className={`pipe-card ${tone}`} title={st.narrative + (st.waitReason ? "｜" + st.waitReason : "") + (st.error ? "｜" + st.error : "")}>
                <div className="pipe-head">
                  <FreshDot dot={{ status: tone, title: st.state }} />
                  <span className="nm">{STAGE_CN[st.key] ?? st.key}</span>
                  <span className="pipe-ts" title="liveness 证据分型">{st.evidenceKind === "beat" ? "⏱" : st.evidenceKind === "event" ? "⚡" : "—"}</span>
                  {st.lastEventUnix > 0 && (
                    <span className="pipe-ts">
                      {new Date(Number(st.lastEventUnix) * 1000).toISOString().slice(5, 16).replace("T", " ")}
                    </span>
                  )}
                </div>
                <div className="pipe-line">
                  {st.error ? <span className="crit">{st.error}</span> : (st.waitReason || st.narrative || "—")}
                </div>
              </div>
            );
          })}
        </div>
      ) : <EmptyState>工序健康读模未供</EmptyState>}

      {/* §2 在仓订单缩览卡阵（kind 徽标+对象+额+态——替旧表） */}
      <h2>在仓订单</h2>
      {b && b.orders.length > 0 ? (
        <ul className="opp-cards scroll-cap">
          {b.orders.map((o: OverviewOrderRow, i: number) => (
            <li key={i} className="opp-card">
              <div className="opp-card-head">
                <Chip tone={orderTone(o.status)}>{o.status}</Chip>
                <span className="opp-card-kind">{o.kind}</span>
                <span className="opp-card-who">{o.label}</span>
                <span className="opp-card-friction">{fmtUsd(o.amountUsd)}</span>
              </div>
              <p className="opp-card-narrative muted">{o.until ? "到期 " + utc(timestampDate(o.until)) : "持续中"}</p>
            </li>
          ))}
        </ul>
      ) : <EmptyState>无在仓订单</EmptyState>}
      {b && b.paperCount > 0 && <div className="muted">纸面沙箱 {b.paperCount} 条（不入本金）</div>}

      {/* §6 收益归因分解（后端聚合——策略×30d 结算净额；零命中不造行） */}
      <h2>收益归因（30d）</h2>
      {b && b.attribution.length > 0 ? (
        <table>
          <thead><tr><th>策略</th><th>结算仓</th><th>净额</th><th>胜负</th></tr></thead>
          <tbody>
            {b.attribution.map((a, i) => (
              <tr key={i}>
                <td>{a.strategy}</td>
                <td>{a.positions}</td>
                <td className={a.net30Usd >= 0 ? "good" : "crit"}>{(a.net30Usd >= 0 ? "+" : "") + fmtUsd(a.net30Usd)}</td>
                <td className="muted">{a.wins}胜/{a.losses}负</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : <EmptyState>30d 窗内无结算归因行</EmptyState>}

      {/* §7 闭环漏斗计数（八环 48h——后端 COUNT 直出；淘汰环 cumulative 如实标注） */}
      <h2>闭环漏斗（48h）</h2>
      {b && b.funnel.length > 0 ? (
        <div className="stats">
          {b.funnel.map((f) => (
            <StatTile key={f.stage} label={f.label + (f.cumulative ? "（累计）" : "")}
              value={String(f.count48h)} />
          ))}
        </div>
      ) : <EmptyState>漏斗计数未供</EmptyState>}

      {/* §8 等待面缩览（pending/观察中毕业+待执行提案——等待因+龄，后端拼行） */}
      <h2>等待面</h2>
      {b && b.waiting.length > 0 ? (
        <ul className="opp-cards scroll-cap">
          {b.waiting.map((w, i) => (
            <li key={i} className="opp-card">
              <div className="opp-card-head">
                <Chip tone={w.kind === "proposal" ? "warn" : "neutral"}>
                  {w.kind === "graduation_pending" ? "待批" : w.kind === "graduation_watch" ? "观察中" : "提案"}
                </Chip>
                <span className="opp-card-kind">{w.label}</span>
                <span className="opp-card-friction">
                  {w.sinceUnix > 0 ? "自 " + new Date(Number(w.sinceUnix) * 1000).toISOString().slice(5, 16).replace("T", " ") + "Z" : "—"}
                </span>
              </div>
              <p className="opp-card-narrative muted">{w.detail}</p>
            </li>
          ))}
        </ul>
      ) : <EmptyState>无等待项</EmptyState>}

      {/* §3 风险心跳 */}
      <h2>风险心跳</h2>
      <div className="stats">
        <StatTile label="KillSwitch" value={b ? (b.killSwitch ? "ON" : "off") : "—"} />
        <StatTile label="30d 净额（对 −3% 硬线）" value={b ? fmtUsd(b.net30Usd) : "—"} />
        <StatTile label="24h critical" value={b ? String(b.critAlerts24h) : "—"} />
      </div>
    </div>
  );
}
