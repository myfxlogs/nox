// overview_duo.tsx —— 在仓订单+等待面满宽纵排区（业主令 10-19 再裁：双栏并列
// 两列行数差→行线错位病灶治；改两段顺序满宽 grid——无并列列即无行可对错，
// 满宽栅格卡密度反升+右侧空洞仍自然填实，长列 scroll-cap 封顶既有机制）。
import type { OverviewOrderRow, GetOverviewBundleResponse } from "../gen/dashboardv1/account_pb";
import { ORDER_KIND_CN, ORDER_STATUS_CN, untilCN } from "../components/orders_fmt";
import { fmtUsd } from "../components/fmt";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";

// orderTone 在仓订单态→徽标色（status 枚举显示映射非判据——键集按
// orders_fmt.ORDER_STATUS_CN 真枚举重建：filled/settled/failed/pending/live/
// LIVE/open/active/OPEN/CLOSED/proposed；holding/open_order/paper 非枚举不硬编码）。
function orderTone(s: string): "good" | "warn" | "critical" | "neutral" {
  switch (s) {
    case "open": case "filled": case "live": case "LIVE": case "active": case "proposed": case "OPEN":
      return "good";
    case "pending": case "closing":
      return "warn";
    case "failed": case "error":
      return "critical";
    default: // settled/CLOSED/未知=结清态或非枚举——neutral 如实缺
      return "neutral";
  }
}

export function OrderWaitDuo({ b, setCardModal }: {
  b: GetOverviewBundleResponse | null;
  setCardModal: (v: { title: string; body: string } | null) => void;
}) {
  return (
    <>
      <div>
        {/* §2 在仓订单缩览卡阵（D-低密度网格化：三行=状态/对象截断/⏱ 叙事；点击 Modal 全文） */}
        <h2>在仓订单</h2>
        {b && b.orders.length > 0 ? (
          <ul className="opp-cards opp-grid scroll-cap">
            {b.orders.map((o: OverviewOrderRow, i: number) => (
              <li key={i} className="opp-card" onClick={() => setCardModal({
                title: `${ORDER_KIND_CN[o.kind] ?? o.kind} · ${o.label}`,
                body: `状态 ${ORDER_STATUS_CN[o.status] ?? o.status}｜金额 ${fmtUsd(o.amountUsd)}｜` + untilCN(o),
              })}>
                <div className="opp-card-head">
                  <Chip tone={orderTone(o.status)}>{ORDER_STATUS_CN[o.status] ?? o.status}</Chip>
                  <span className="opp-card-friction">{fmtUsd(o.amountUsd)}</span>
                </div>
                <span className="opp-card-who">{ORDER_KIND_CN[o.kind] ?? o.kind} · {o.label}</span>
                <p className="opp-card-narrative muted clamp-2" title={untilCN(o)}>⏱ {untilCN(o)}</p>
              </li>
            ))}
          </ul>
        ) : <EmptyState>无在仓订单</EmptyState>}
        {b && b.paperCount > 0 && <div className="muted">纸面沙箱 {b.paperCount} 条（不入本金）</div>}
      </div>
      <div>
        {/* §8 等待面缩览（pending/观察中毕业+待执行提案——等待因+龄，后端拼行） */}
        <h2>等待面</h2>
        {b && b.waiting.length > 0 ? (
          <ul className="opp-cards opp-grid scroll-cap">
            {b.waiting.map((w: import("../gen/dashboardv1/account_pb").OverviewWaitingRow, i: number) => (
              <li key={i} className="opp-card" onClick={() => setCardModal({
                title: `${w.kind === "graduation_pending" ? "待批" : w.kind === "graduation_watch" ? "观察中" : "提案"} · ${w.label}`,
                body: w.detail + (w.sinceUnix > 0 ? "｜自 " + new Date(Number(w.sinceUnix) * 1000).toISOString().replace("T", " ").slice(0, 19) + "Z" : ""),
              })}>
                <div className="opp-card-head">
                  <Chip tone={w.kind === "proposal" ? "warn" : "neutral"}>
                    {w.kind === "graduation_pending" ? "待批" : w.kind === "graduation_watch" ? "观察中" : "提案"}
                  </Chip>
                  <span className="opp-card-friction">
                    {w.sinceUnix > 0 ? "自 " + new Date(Number(w.sinceUnix) * 1000).toISOString().slice(5, 16).replace("T", " ") + "Z" : "—"}
                  </span>
                </div>
                <span className="opp-card-who">{w.label}</span>
                <p className="opp-card-narrative muted clamp-2" title={w.detail}>{w.detail}</p>
              </li>
            ))}
          </ul>
        ) : <EmptyState>无等待项</EmptyState>}
      </div>
    </>
  );
}
