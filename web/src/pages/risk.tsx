// risk.tsx —— 风控健康：数据源健康（case5）+保证金状态+覆盖率+告警（case3/10）。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { Alert, SourceHealth, VerificationStat } from "../gen/dashboardv1/market_pb";
import type { AccountMarginStatus } from "../gen/dashboardv1/account_pb";
import type { CapitalCoverageRow } from "../gen/dashboardv1/ops_pb";
import { fmtUsd, ts } from "../components/fmt";

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
      {err && <div className="err">{err}</div>}
      <h2>数据源健康</h2>
      <table>
        <thead><tr><th>venue</th><th>源</th><th>状态</th><th>最后事件</th><th>重连次数</th></tr></thead>
        <tbody>
          {[...health.values()].map((h) => (
            <tr key={h.venue}>
              <td>{h.venue}</td><td>{h.source}</td>
              <td className={h.state === "live" || h.state === "ok" ? "good" : "crit"}>{h.state}</td>
              <td>{ts(h.lastEventAt)}</td>
              <td>{String(h.reconnectCount)}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>账户保证金</h2>
      <table>
        <thead><tr><th>venue</th><th>权益</th><th>已用保证金</th><th>可用</th><th>覆盖天数</th><th>建议补足</th><th>tier</th><th>更新</th></tr></thead>
        <tbody>
          {margin.map((m) => (
            <tr key={m.venue}>
              <td>{m.venue}</td>
              <td>{fmtUsd(m.equity)}</td>
              <td>{fmtUsd(m.marginUsed)}</td>
              <td>{fmtUsd(m.freeMargin)}</td>
              <td className={m.coverageDays < 3 ? "crit" : ""}>{m.coverageDays.toFixed(1)}</td>
              <td>{fmtUsd(m.suggestedTopupUsd)}</td>
              <td>{m.tier}</td>
              <td>{ts(m.updatedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>资本覆盖率 {cov && <span className="muted">{cov.note}</span>}</h2>
      {cov && (
        <div className="cards">
          <div className="card"><div className="k">总资金</div><div className="v">{fmtUsd(cov.total)}</div></div>
          <div className="card"><div className="k">覆盖率</div><div className="v">{cov.pct.toFixed(1)}%</div></div>
        </div>
      )}
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

      <h2>告警</h2>
      <table>
        <thead><tr><th>时间</th><th>级别</th><th>内容</th><th>acked</th></tr></thead>
        <tbody>
          {alerts.map((a) => (
            <tr key={Number(a.id)}>
              <td>{ts(a.ts)}</td>
              <td className={a.level === "crit" ? "crit" : a.level === "warn" ? "warn" : ""}>{a.level}</td>
              <td style={{ whiteSpace: "normal" }}>{a.message}</td>
              <td>{String(a.acked)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
