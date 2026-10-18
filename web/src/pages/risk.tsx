// risk.tsx —— 风控健康：数据源健康（case5）+保证金状态+覆盖率+告警（case3/10）。
// S3′-final 批迁：第一眼=护栏态（账户保证金卡阵升顶——覆盖天数 crit 直透）+
// 覆盖率 StatTile；源健康改卡阵挂 FreshDot；告警/验证比率台账密表保留。
// 纯 JSX/CSS 重排零逻辑动。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { Alert, SourceHealth, VerificationStat } from "../gen/dashboardv1/market_pb";
import type { AccountMarginStatus } from "../gen/dashboardv1/account_pb";
import type { CapitalCoverageRow } from "../gen/dashboardv1/ops_pb";
import { fmtUsd, ts } from "../components/fmt";
import { StatTile } from "../components/StatTile";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";
import { FreshDot as Dot } from "../components/FreshDot";
import { freshOf } from "../components/freshness";

export default function RiskPage() {
  const [health, setHealth] = useState<Map<string, SourceHealth>>(new Map());
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [margin, setMargin] = useState<AccountMarginStatus[]>([]);
  const [vstats, setVstats] = useState<VerificationStat[]>([]);
  const [cov, setCov] = useState<{ rows: CapitalCoverageRow[]; total: number; pct: number; note: string } | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    try {
      const [h, a, m, v, c] = await Promise.all([
        client.listSourceHealth({}),
        client.listAlerts({ limit: 30 }),
        client.listAccountMarginStatus({}),
        client.listVerificationStats({}),
        client.getCapitalCoverage({}),
      ]);
      setHealth(new Map(h.sources.map((r) => [r.venue, r])));
      setAlerts(a.alerts);
      setMargin(m.rows);
      setVstats(v.stats);
      setCov({ rows: c.rows, total: c.totalUsd, pct: c.coveragePct, note: c.caliberNote });
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  }, []);

  useEffect(() => {
    void load();
    const offs = [
      onCase("sourceHealth", (v) => {
        const s = v as SourceHealth;
        setHealth((m) => new Map(m).set(s.venue, s));
      }),
      onCase("alert", (v) => {
        const a = v as Alert;
        setAlerts((l) => [a, ...l].slice(0, 30));
      }),
      onCase("fundingNeed", () => void load()),
    ];
    return () => offs.forEach((f) => f());
  }, [load]);

  return (
    <div>
      <h1>风控</h1>
      {err && <div className="err">{err}</div>}

      {/* 第一眼=护栏态：账户保证金覆盖天数 <3d 直透 crit */}
      <h2>账户保证金（护栏态）</h2>
      {margin.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {margin.map((m) => (
            <li key={m.venue} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">{m.venue}</span>
                <Chip tone={m.coverageDays < 3 ? "critical" : "good"}>覆盖 {m.coverageDays.toFixed(1)}d</Chip>
                <span className="opp-card-who">{m.tier}</span>
              </div>
              <div className="opp-card-stats stats">
                <StatTile label="权益" value={fmtUsd(m.equity)} />
                <StatTile label="已用保证金" value={fmtUsd(m.marginUsed)} />
                <StatTile label="可用" value={fmtUsd(m.freeMargin)} />
                <StatTile label="建议补足" value={fmtUsd(m.suggestedTopupUsd)} />
              </div>
              <div className="opp-card-narrative">更新 {ts(m.updatedAt)}</div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零保证金行</EmptyState>
      )}

      <h2>资本覆盖率 {cov && <span className="muted">{cov.note}</span>}</h2>
      {cov && (
        <div className="stats">
          <StatTile label="总资金" value={fmtUsd(cov.total)} />
          <StatTile label="覆盖率" value={`${cov.pct.toFixed(1)}%`} />
        </div>
      )}

      <h2>告警{" "}
        {alerts.some((a) => a.acked) && (
          <a href="#" className="muted" style={{ fontSize: 12 }} onClick={(e) => {
            e.preventDefault();
            void (async () => {
              try { await client.clearAckedAlerts({}); void load(); } catch (x) { setErr(errText(x)); }
            })();
          }}>清除已确认</a>
        )}
      </h2>
      {alerts.length > 0 ? (
        <table>
          <thead><tr><th>时间</th><th>级别</th><th>内容</th><th>acked</th><th></th></tr></thead>
          <tbody>
            {alerts.map((a) => (
              <tr key={Number(a.id)}>
                <td>{ts(a.ts)}</td>
                <td className={a.level === "crit" ? "crit" : a.level === "warn" ? "warn" : ""}>{a.level}</td>
                <td style={{ whiteSpace: "normal" }}>{a.message}</td>
                <td>{String(a.acked)}</td>
                <td>
                  <a href="#" onClick={(e) => {
                    e.preventDefault();
                    void (async () => {
                      try { await client.deleteAlerts({ ids: [a.id] }); void load(); } catch (x) { setErr(errText(x)); }
                    })();
                  }}>删</a>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <EmptyState>零告警行</EmptyState>
      )}

      <h2>数据源健康</h2>
      {[...health.values()].length > 0 ? (
        <ul className="opp-cards opp-grid">
          {[...health.values()].map((h) => (
            <li key={h.venue} className="opp-card">
              <div className="opp-card-head">
                <Dot dot={freshOf({ name: h.venue, status: h.state, lastFactAt: h.lastEventAt ? Number(h.lastEventAt.seconds) * 1000 : 0 })} />
                <span className="opp-card-kind">{h.venue}</span>
                <Chip tone={h.state === "live" || h.state === "ok" ? "good" : "critical"}>{h.state}</Chip>
              </div>
              <div className="opp-card-narrative">
                {h.source} · 最后 {ts(h.lastEventAt)} · 重连 {String(h.reconnectCount)}
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零源健康行</EmptyState>
      )}

      <h2>验证比率</h2>
      {vstats.length > 0 ? (
        <table>
          <thead><tr><th>vkey</th><th>样本</th><th>预期USD</th><th>实得USD</th><th>比率</th><th>末样本</th></tr></thead>
          <tbody>
            {vstats.map((v) => (
              <tr key={v.vkey}>
                <td>{v.vkey}</td><td>{v.samples}</td>
                <td>{fmtUsd(v.expectedUsd)}</td><td>{fmtUsd(v.realizedUsd)}</td>
                <td className={v.ratio < 0.5 ? "crit" : ""}>{v.ratio.toFixed(2)}</td>
                <td>{ts(v.lastSampleAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <EmptyState>零验证行</EmptyState>
      )}
    </div>
  );
}
