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
        <StatTile label="基线后业绩" value={b?.perfAvailable ? (b.perfUsd >= 0 ? "+" : "") + fmtUsd(b.perfUsd) : "—"}
          sub={b?.perfAvailable ? `年化 ${b.perfAnnPct.toFixed(2)}%${b.perfFlowProvisional ? "（provisional）" : ""}` : "基线未配置"} />
      </div>

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
