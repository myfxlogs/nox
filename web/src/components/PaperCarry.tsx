// PaperCarry.tsx —— 纸面沙箱 carry 轨卡（arb PositionsPagePaperCarry 借形，D-352 行+轨汇总）。
// ListPaperSpotCarry 一次拉全（后端已排序：active→demo→14d exited）；
// 轨 tab 选择=显示面过滤非业务判据；数值全后端算好直渲，null 传「—」。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { PaperSpotCarryRow, PaperTrackSummary } from "../gen/dashboardv1/ops_pb";
import { Chip } from "./Chip";
import { EmptyState } from "./EmptyState";
import { fmtUsd } from "./fmt";

const TRACK_TEXT: Record<string, string> = {
  spot_carry: "现货×永续",
  perp_perp: "永续×永续",
  long_cfd_short_perp: "多CFD·空永续",
  short_cfd_long_perp: "空CFD·多永续",
  long_spot_short_perp: "现货×永续（双所）",
};
const trackName = (s: string) => TRACK_TEXT[s] ?? `${s} 轨`;

const opt = (v: number | undefined, f: (x: number) => string) =>
  v === undefined ? "—" : f(v);

export function PaperCarry() {
  const [rows, setRows] = useState<PaperSpotCarryRow[]>([]);
  const [tracks, setTracks] = useState<PaperTrackSummary[]>([]);
  const [sel, setSel] = useState<string | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await client.listPaperSpotCarry({});
      setRows(r.rows); setTracks(r.tracks); setErr("");
    } catch (e) { setErr(errText(e)); }
  }, []);

  useEffect(() => {
    void load();
    return onCase("dataRefresh", () => void load());
  }, [load]);

  const shown = sel ? rows.filter((r) => r.structure === sel) : rows;

  return (
    <section>
      <h2>纸面沙箱 carry 轨</h2>
      {err && <div className="err">{err}</div>}
      {tracks.length > 0 && (
        <div className="stats" style={{ marginBottom: 8 }}>
          {tracks.map((t) => (
            <a key={t.structure} href="#" onClick={(e) => { e.preventDefault(); setSel(sel === t.structure ? null : t.structure); }}
              className={`chip ${sel === t.structure ? "chip-active" : ""}`} style={{ textDecoration: "none" }}>
              {trackName(t.structure)} 在轨{t.activeN}/试{t.demoN}·退{t.exitedN}
              {t.exitedN > 0 ? ` 胜${t.winRatePct.toFixed(0)}%` : ""}·息{fmtUsd(t.accruedHoldingUsd)}
            </a>
          ))}
        </div>
      )}
      {shown.length === 0 ? (
        <EmptyState>零纸面行</EmptyState>
      ) : (
        <table className="rows" style={{ fontSize: 12 }}>
          <thead><tr>
            <th>状态</th><th>venue</th><th>symbol</th><th>方向</th><th>名义</th>
            <th>入场年化</th><th>现年化</th><th>净息</th><th>应计</th><th>基差浮盈</th><th>净浮盈</th>
            <th>天数</th><th>收益率</th><th>实收年化</th>
          </tr></thead>
          <tbody>
            {shown.map((r) => (
              <tr key={String(r.id)}>
                <td>
                  <Chip tone={r.status === "active" ? "good" : r.status === "demo" ? "warn" : "neutral"}>{r.status}</Chip>
                  {r.estRoundTripFeeUsd === undefined && <span className="muted" title="往返费估计缺行"> 观测轨</span>}
                </td>
                <td>{r.venue}{r.mtVenue ? `+${r.mtVenue}` : ""}</td>
                <td>{r.symbol}</td>
                <td>{r.side}</td>
                <td>{fmtUsd(r.qtyUsd)}</td>
                <td>{r.entryFundingAnn.toFixed(1)}%</td>
                <td>{opt(r.lastFundingAnn, (v) => v.toFixed(1) + "%")}</td>
                <td>{opt(r.netNow, (v) => v.toFixed(2) + "%")}</td>
                <td>{fmtUsd(r.accruedUsd)}</td>
                <td>{opt(r.basisPnlUsd, (v) => fmtUsd(v))}</td>
                <td>{opt(r.totalPnlUsd, (v) => fmtUsd(v))}</td>
                <td>{r.daysHeld.toFixed(1)}d</td>
                <td>{r.retPct.toFixed(2)}%</td>
                <td>{opt(r.realizedAnnPct, (v) => v.toFixed(1) + "%")}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
