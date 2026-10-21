// overview.tsx —— S3′-a 默认首页重建（arb 骨架范式：StatTile 横排+
// 行动卡置顶+缩览卡阵；风险心跳段拆 overview_heartbeat.tsx（300 行闸））。
// 前端零运算只渲染；缺数据显缺不显 0；时戳 UTC 口径（业主令 10-15）。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { GetOverviewBundleResponse } from "../gen/dashboardv1/account_pb";
import { IdleStreakState } from "../gen/dashboardv1/account_pb";
import type { RequestBoxItem } from "../gen/dashboardv1/request_box_pb";
import { ResolveRequestState } from "../gen/dashboardv1/request_box_pb";
import { fmtUsd } from "../components/fmt";
import { RiskHeartbeat } from "./overview_heartbeat";
import { QuoteStrip } from "../components/QuoteStrip";
import { StatTile } from "../components/StatTile";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";
import { PnlOverviewCard } from "../components/OverviewAuxCards";
import { Modal } from "../components/Modal";
import { OrderWaitDuo } from "./overview_duo";
import type { FreshDot as FreshDotData } from "../components/freshness";

// STAGE_CN 十段工序 key→中文名（显示映射非判据；新段 key 补表行）
const STAGE_CN: Record<string, string> = {
  collect: "采集", research: "研究", opp_publish: "机会发布", gate_eval: "闸评",
  assembly: "装配", execution: "执行", holding: "持仓", exit: "平仓",
  ledger: "账本", review: "复盘",
};

const utc = (t: Date | undefined) =>
  t ? t.toISOString().replace("T", " ").slice(0, 19) + "Z" : "—";


// amtText 组成行金额渲染——USD 族 fmtUsd；空 ccy=指标数（年化等）带 sub；
// 其余原生币如实显原生量（跨币不换算——D-285 族律的展示面兑现）。
function amtText(ccy: string, amount: number, sub: string): string {
  if (ccy === "USD" || ccy === "USDT") return fmtUsd(amount);
  if (ccy === "") return `${amount.toFixed(2)}${sub ? " " + sub : ""}`;
  return `${amount.toLocaleString("en-US", { maximumFractionDigits: 6 })} ${ccy}`;
}

// TILE_GROUP 盘面卡→明细组键（显示映射非判据）
const TILE_GROUP: Record<string, string> = {
  nav: "NAV 总权益", invested: "总投入", principal: "本金合计", idle: "可动用",
  earning: "在息", perf: "基线后业绩",
};

export default function OverviewPage() {
  const [b, setB] = useState<GetOverviewBundleResponse | null>(null);
  const [nav, setNav] = useState<number | null>(null);
  const [box, setBox] = useState<RequestBoxItem[]>([]);
  const [busy, setBusy] = useState<number | null>(null);
  const [err, setErr] = useState("");
  const [cardModal, setCardModal] = useState<{ title: string; body: string } | null>(null);
  const [tile, setTile] = useState<string | null>(null); // 盘面卡下钻组键

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

      {/* S3′ 三件补：报价条=待办下 KPI 上第二眼活体感（自持订阅 quote 帧+UTC 钟） */}
      <QuoteStrip />

      {/* §1 KPI 横排（StatTile 阵——NAV/总投入/本金/可动用/在息/业绩；
          卡可点击下钻组成明细——后端 breakdowns 按 group 过滤直渲） */}
      <h2>盘面</h2>
      <div className="stats">
        <StatTile label="NAV 总权益" value={nav != null ? fmtUsd(nav) : "—"}
          active={tile === "nav"} onClick={() => setTile(tile === "nav" ? null : "nav")} />
        <StatTile label="总投入（基线+注资）" value={b?.investedAvailable ? fmtUsd(b.netInvestedUsd) : "—"}
          sub={b?.perfFlowProvisional ? "净流 provisional" : undefined}
          active={tile === "invested"} onClick={() => setTile(tile === "invested" ? null : "invested")} />
        <StatTile label="本金合计（USDT+USD）" value={b?.principalAvailable ? fmtUsd(b.principalTotalUsd) : "—"}
          active={tile === "principal"} onClick={() => setTile(tile === "principal" ? null : "principal")} />
        <StatTile label="可动用" value={b?.principalAvailable ? fmtUsd(b.principalIdleUsd) : "—"}
          active={tile === "idle"} onClick={() => setTile(tile === "idle" ? null : "idle")} />
        <StatTile label="在息" value={b?.principalAvailable ? fmtUsd(b.principalEarningUsd) : "—"}
          sub={b?.principalAvailable && b.principalLockedUsd > 0 ? `含锁定 ${fmtUsd(b.principalLockedUsd)}${b.earliestRelease ? "·最早回笼 " + utc(timestampDate(b.earliestRelease)) : ""}` : undefined}
          active={tile === "earning"} onClick={() => setTile(tile === "earning" ? null : "earning")} />
        {/* 眠钱时长：后端 idle_streak_state 显式分态全信——healthy_zero=「0」常志态、
            streak=「N 天」；非健康态「—」恒带琥珀警示（断供≠无眠钱），加载窗显「…」
            ——裸横线结构性消灭（业主报障 10-21：裸「—」与断供态不可判读） */}
        <StatTile label="眠钱时长"
          value={b == null ? "…"
            : b.idleStreakState === IdleStreakState.STREAK ? `${b.idleStreakDays} 天`
            : b.idleStreakState === IdleStreakState.HEALTHY_ZERO ? "0" : "—"}
          valueClass={b != null && b.idleStreakState !== IdleStreakState.STREAK
            && b.idleStreakState !== IdleStreakState.HEALTHY_ZERO ? "u-amber" : undefined}
          sub={b?.idleStreakState === IdleStreakState.STREAK ? `最长池 ${b.idleStreakPool}`
            : b?.idleStreakState === IdleStreakState.HEALTHY_ZERO ? "在册零破地板池" : undefined}
          title={b != null && b.idleStreakState !== IdleStreakState.STREAK
            && b.idleStreakState !== IdleStreakState.HEALTHY_ZERO
            ? "idle_floor_streak 读模零行/读败=断供（D-612 首轮动窗前零行为预期）" : undefined} />
        <StatTile label="基线后业绩" value={b?.perfAvailable ? (b.perfUsd >= 0 ? "+" : "") + fmtUsd(b.perfUsd) : "—"}
          sub={b?.perfAvailable ? `年化 ${b.perfAnnPct.toFixed(2)}%｜目标≥8%${b.perfFlowProvisional ? "（provisional）" : ""}` : "基线未配置"}
          active={tile === "perf"} onClick={() => setTile(tile === "perf" ? null : "perf")} />
        {/* D-630 权威口径：权益差−io 净流（穿透归因层；缺盖池如实标注） */}
        <StatTile label="真实净盈利（权威口径）"
          value={b?.trueNetAvailable ? `30d ${(b.trueNet30dUsd >= 0 ? "+" : "")}${fmtUsd(b.trueNet30dUsd)}｜全时 ${(b.trueNetAllUsd >= 0 ? "+" : "")}${fmtUsd(b.trueNetAllUsd)}` : "—"}
          sub={b?.trueNetUncovered ? `缺基线池: ${b.trueNetUncovered}` : "权益差−io 净流"} />
      </div>

      <PnlOverviewCard />
      {tile && (
        <Modal open={tile != null} onClose={() => setTile(null)}
          title={`${TILE_GROUP[tile]}·组成明细`} width={520}>
          {b && b.breakdowns.some((r) => r.group === tile) ? (
            <ul>
              {b.breakdowns.filter((r) => r.group === tile).map((r, i) => (
                <li key={i}>
                  <span className="sd-label">{r.label}</span>
                  <span className="sd-amt">{amtText(r.ccy, r.amount, "")}</span>
                  {r.sub && <span className="sd-sub muted">{r.sub}</span>}
                </li>
              ))}
            </ul>
          ) : (
            <div className="muted">组成明细后端未供（读败或零池——如实缺）</div>
          )}
        </Modal>
      )}

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
              /* 实时卡防抖：钳 2 行后全文兜底=卡 title（hover）+点卡 Modal（与 opp-card 同径） */
              <div key={st.key} className={`pipe-card ${tone}`} title={st.narrative + (st.waitReason ? "｜" + st.waitReason : "") + (st.error ? "｜" + st.error : "")}
                onClick={() => setCardModal({
                  title: "工序 · " + (STAGE_CN[st.key] ?? st.key),
                  body: st.narrative + (st.waitReason ? "｜" + st.waitReason : "") + (st.error ? "｜" + st.error : ""),
                })}>
                <div className="pipe-head">
                  {/* 业主定案 10-17：边框色单载状态——FreshDot 删（组件留他面），
                      证据 icon 移左端放大；state 语义落 icon title（点删后悬停可查） */}
                  <span className={`pipe-ev pipe-ts-${tone}`} title={st.state + "｜liveness 证据分型：" + st.evidenceKind}>
                    {st.evidenceKind === "beat" ? "⏱" : st.evidenceKind === "event" ? "⚡" : "—"}
                  </span>
                  <span className="nm">{STAGE_CN[st.key] ?? st.key}</span>
                  {st.lastEventUnix > 0 && (
                    /* HH:MM 缩写（重构定案——全时戳 title 兜底） */
                    <span className="pipe-clock" title={new Date(Number(st.lastEventUnix) * 1000).toISOString().slice(5, 16).replace("T", " ") + "Z"}>
                      {new Date(Number(st.lastEventUnix) * 1000).toISOString().slice(11, 16)}
                    </span>
                  )}
                </div>
                <div className="pipe-line clamp-2">
                  {st.error ? <span className="crit">{st.error}</span> : (st.waitReason || st.narrative || "—")}
                </div>
              </div>
            );
          })}
        </div>
      ) : <EmptyState>工序健康读模未供</EmptyState>}

      {/* §2+§8 在仓订单+等待面——OrderWaitDuo 双栏并置件（300 棘轮拆件） */}
      <OrderWaitDuo b={b} setCardModal={setCardModal} />

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
      ) : <EmptyState>30d 归因读模未供或零行</EmptyState>}

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

      {/* S3′ 三件补：风险心跳页底最末段（全段之后——组件/数据零动仅位移） */}
      <RiskHeartbeat b={b} onCard={setCardModal} />

      {cardModal && (
        <Modal open onClose={() => setCardModal(null)} title={cardModal.title} width={520}>
          <div style={{ whiteSpace: "pre-wrap", fontSize: 13 }}>{cardModal.body}</div>
        </Modal>
      )}
    </div>
  );
}
