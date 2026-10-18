// cexscan.tsx —— CEX 扫描快照页（arb CexScanPage 借形）：理财 APR 行（年化/
// 分层/判断徽标后端 verdict 直渲）+价差/资金费率/余额三辅表。手动刷新钮+
// cex_scan 流帧重取（无定时器）。
import { useCallback, useEffect, useState } from "react";
import { client } from "../api";
import { onCase } from "../stream";
import type { GetCexScanSnapshotResponse } from "../gen/dashboardv1/trx_cex_pb";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";

const VERDICT_TONE: Record<string, "good" | "warn" | "critical" | "neutral"> = {
  opportunity: "good", not_opportunity: "neutral", trap: "critical",
};

export default function CexScanPage() {
  const [snap, setSnap] = useState<GetCexScanSnapshotResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");
  const load = useCallback(async () => {
    setLoading(true);
    try { setSnap(await client.getCexScanSnapshot({})); setErr(""); }
    catch { setErr("数据未就绪"); }
    finally { setLoading(false); }
  }, []);
  useEffect(() => {
    void load();
    return onCase("dataRefresh", (v) => {
      if ((v as { topic: string }).topic === "cex_scan") void load();
    });
  }, [load]);
  return (
    <div className="page">
      <h1>CEX 扫描</h1>
      <section>
        <h2>理财 APR <button onClick={() => void load()} disabled={loading} style={{ float: "right", fontSize: 12 }}>{loading ? "刷新中…" : "刷新"}</button></h2>
        {err && !snap && <div className="err">{err}</div>}
        {!snap ? <EmptyState>加载中…</EmptyState> : snap.earnAprs.length === 0 ? (
          <EmptyState>数据未就绪（等待采集）</EmptyState>
        ) : (
          <table className="rows">
            <thead><tr><th>交易所</th><th>币种</th><th>产品</th><th>年化 %</th><th>分层</th><th>判断</th></tr></thead>
            <tbody>
              {snap.earnAprs.map((r, i) => {
                const j = snap.judgments.find((x) => x.venue === r.venue && x.ccy === r.ccy && x.productType === r.productType);
                return (
                  <tr key={`${r.venue}-${r.ccy}-${i}`}>
                    <td>{r.venue}</td><td>{r.ccy}</td><td>{r.productType}</td>
                    <td className="num">{(r.apr * 100).toFixed(2)}</td>
                    <td className="muted">{r.tierApr ?? "—"}</td>
                    <td>{j ? <Chip tone={VERDICT_TONE[j.verdict] ?? "neutral"}>{j.verdict}</Chip> : <span className="muted">—</span>}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </section>
      {snap && snap.spreads.length > 0 && (
        <section>
          <h2>价差速览</h2>
          <table className="rows">
            <thead><tr><th>品种</th><th>买 venue</th><th>卖 venue</th><th>价差 bps</th></tr></thead>
            <tbody>{snap.spreads.map((s, i) => <tr key={i}><td>{s.symbol}</td><td>{s.venueBuy}</td><td>{s.venueSell}</td><td className="num">{s.spreadBps.toFixed(1)}</td></tr>)}</tbody>
          </table>
        </section>
      )}
      {snap && snap.fundings.length > 0 && (
        <section>
          <h2>资金费率</h2>
          <table className="rows">
            <thead><tr><th>品种</th><th>venue</th><th>rate</th><th>时点</th></tr></thead>
            <tbody>{snap.fundings.map((f, i) => <tr key={i}><td>{f.symbol}</td><td>{f.venue}</td><td className="num">{(f.rate * 100).toFixed(4)}%</td><td className="muted">{f.ts.slice(0, 16)}</td></tr>)}</tbody>
          </table>
        </section>
      )}
    </div>
  );
}
