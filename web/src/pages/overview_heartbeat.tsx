// overview_heartbeat.tsx —— 风险心跳段（业主令 10-17 三件修①②：段位升待办下
// 盘面上+critical 明细卡阵。自 overview.tsx 拆出=300 行闸；数据面=GetOverviewBundle
// crit_alerts（后端拼行直渲，前端零拼零新 RPC）；点击 Modal 全文兜底）。
import type { GetOverviewBundleResponse } from "../gen/dashboardv1/account_pb";
import { Chip } from "../components/Chip";
import { fmtUsd } from "../components/fmt";
import { StatTile } from "../components/StatTile";

export function RiskHeartbeat({
  b,
  onCard,
}: {
  b: GetOverviewBundleResponse | null;
  onCard: (c: { title: string; body: string }) => void;
}) {
  return (
    <>
      {/* §0.5 风险心跳（业主令 10-17：§1 风控硬线高于目标——段位升待办下盘面上；
          critical 明细随段=24h 窗后端拼行直渲，可见「是什么红」非只「有几个红」；
          点击 Modal 全文兜底，前端零拼零新 RPC） */}
      <h2>风险心跳</h2>
      <div className="stats">
        <StatTile
          label="急停开关"
          value={b ? (b.killSwitch ? "停机中" : "运行中") : "—"}
          valueClass={b ? (b.killSwitch ? "u-bad" : "u-good") : undefined}
          title="30d 滚动账本净额（含事故仓）≤ −$300 自动触发停机（D-030 硬线）；读败视同停机（宁显停不显通）；解除=人工 Resolve"
        />
        <StatTile label="30d 净额（对 −3% 硬线）" value={b ? fmtUsd(b.net30Usd) : "—"} />
        <StatTile label="24h critical" value={b ? String(b.critAlerts24h) : "—"} />
      </div>
      {b && b.critAlerts.length > 0 && (
        <ul className="opp-cards">
          {b.critAlerts.map((a, i) => (
            <li key={i} className="opp-card" onClick={() => onCard({
              title: "critical 告警 · " + new Date((a.ts ? Number(a.ts.seconds) * 1000 : 0)).toISOString().replace("T", " ").slice(0, 19) + "Z",
              body: a.message + (a.count > 1 && a.firstTs
                ? "\n\n组内 " + a.count + " 条：" +
                  new Date(Number(a.firstTs.seconds) * 1000).toISOString().slice(5, 16).replace("T", " ") + "Z → " +
                  new Date(Number(a.ts!.seconds) * 1000).toISOString().slice(5, 16).replace("T", " ") + "Z"
                : ""),
            })}>
              <div className="opp-card-head">
                <Chip tone="critical">critical</Chip>
                {a.count > 1 && <Chip tone="warn">×{a.count}</Chip>}
                <span className="opp-card-friction">{a.ts ? new Date(Number(a.ts.seconds) * 1000).toISOString().slice(5, 16).replace("T", " ") + "Z" : "—"}</span>
              </div>
              <p className="opp-card-narrative muted clamp-2" title={a.message}>{a.message}</p>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
