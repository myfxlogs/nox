// TrxYieldRadar.tsx —— 收益率卡（arb 同名件借形，D-288 合并版式）：
// 6 格 grid=能量 7d/30d+投票当前/30d+组合 7d/30d（后端算定直渲，样本不足
// 标透传），敞口计量行=累积收益+对冲漂移。onCase("dataRefresh") topic=trx
// 流帧重取——arb 原型 30s interval 已退役同径。
import { useCallback, useEffect, useState } from "react";
import { client } from "../api";
import { onCase } from "../stream";
import type { GetTrxYieldRadarResponse, GetTrxYieldCurveResponse, HedgeCarry } from "../gen/dashboardv1/trx_cex_pb";
import { fmtUsd, fmtNum, ts } from "./fmt";

const fmtPct = (v: number) => (v ? `${v.toFixed(2)}%` : "—");
const round3 = (v: number) => Math.round(v * 1000) / 1000;

export function TrxYieldRadar() {
  const [radar, setRadar] = useState<GetTrxYieldRadarResponse | null>(null);
  const [curve, setCurve] = useState<GetTrxYieldCurveResponse | null>(null);
  const [carry, setCarry] = useState<HedgeCarry | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const fetchAll = useCallback(async () => {
    setLoading(true); setErr(null);
    try {
      const [r, c, h] = await Promise.all([
        client.getTrxYieldRadar({}),
        client.getTrxYieldCurve({ days: 30 }),
        client.getHedgeStructure({}),
      ]);
      setRadar(r); setCurve(c);
      if (h.structure?.carry) setCarry(h.structure.carry);
    } catch { setErr("数据未就绪"); }
    finally { setLoading(false); }
  }, []);

  useEffect(() => {
    void fetchAll();
    return onCase("dataRefresh", (v) => {
      if ((v as { topic: string }).topic === "trx") void fetchAll();
    });
  }, [fetchAll]);

  const ready = radar && curve;
  const cold7d = curve && curve.sampleDays7d > 0 && curve.sampleDays7d < 7;
  const cold30d = curve && curve.sampleDays30d > 0 && curve.sampleDays30d < 30;
  const belowFloor = radar && radar.floorPct > 0 && radar.energyApr7d > 0 && radar.energyApr7d < radar.floorPct;

  return (
    <section className="card">
      <div className="card-head">
        <h2>收益率</h2>
        <button onClick={() => void fetchAll()} disabled={loading}
          style={{ marginLeft: "auto", cursor: loading ? "wait" : "pointer" }} title="手动刷新收益率">
          {loading ? "刷新中…" : "刷新"}
        </button>
      </div>
      {err && !ready ? (
        <div className="empty" style={{ padding: "12px 0" }}>{err}</div>
      ) : !ready ? (
        <div className="empty" style={{ padding: "12px 0" }}>加载中…</div>
      ) : (
        <>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: "8px 16px", padding: "8px 0" }}>
            <div>
              <div className="muted">能量 7d 年化</div>
              <div className={belowFloor ? "u-bad" : "u-good"} style={{ fontSize: "1.4em", fontWeight: 600 }}>
                {radar.energyApr7d > 0 ? fmtPct(radar.energyApr7d) : "—"}
                {cold7d && <span className="muted" style={{ fontSize: "0.7em" }}> 样本不足({curve.sampleDays7d}d)</span>}
              </div>
              {belowFloor && radar.transition && <div className="u-grey" style={{ fontSize: "0.8em" }}>扩容过渡期：读数=旧租金÷新在押</div>}
              {belowFloor && !radar.transition && <div className="u-bad" style={{ fontSize: "0.8em" }}>低于参考线 {fmtPct(radar.floorPct)}</div>}
            </div>
            <div>
              <div className="muted">能量 30d 年化</div>
              <div className="u-good" style={{ fontSize: "1.4em", fontWeight: 600 }}>
                {radar.energyApr30d > 0 ? fmtPct(radar.energyApr30d) : "—"}
                {cold30d && <span className="muted" style={{ fontSize: "0.7em" }}> 样本不足({curve.sampleDays30d}d)</span>}
              </div>
            </div>
            <div>
              <div className="muted">投票当前</div>
              <div className="u-blue" style={{ fontSize: "1.4em", fontWeight: 600 }}>
                {radar.voteAprCurrent > 0 ? fmtPct(radar.voteAprCurrent) : "—"}
              </div>
              {radar.voteAprMax > 0 && (
                <div className="muted" style={{ fontSize: "0.8em" }}>
                  市场最高 {round3(radar.voteAprMax)}%（{radar.voteAprMaxSr || "—"}） · 差值 {round3(radar.voteAprSpread)}pp
                </div>
              )}
            </div>
            <div>
              <div className="muted">投票 30d 均值</div>
              <div className="u-blue" style={{ fontSize: "1.4em", fontWeight: 600 }}>
                {radar.voteApr30dAvg > 0 ? fmtPct(radar.voteApr30dAvg) : "—"}
              </div>
            </div>
            <div>
              <div className="muted">组合 7d 年化</div>
              <div className="u-good" style={{ fontSize: "1.4em", fontWeight: 600 }}>
                {curve.annualized7d > 0 ? fmtPct(curve.annualized7d) : "—"}
                {cold7d && <span className="muted" style={{ fontSize: "0.7em" }}> 样本不足({curve.sampleDays7d}d)</span>}
              </div>
            </div>
            <div>
              <div className="muted">组合 30d 年化</div>
              <div className="u-good" style={{ fontSize: "1.4em", fontWeight: 600 }}>
                {curve.annualized30d > 0 ? fmtPct(curve.annualized30d) : "—"}
                {cold30d && <span className="muted" style={{ fontSize: "0.7em" }}> 样本不足({curve.sampleDays30d}d)</span>}
              </div>
            </div>
          </div>
          <div style={{ marginTop: "8px", borderTop: "1px solid var(--line)", paddingTop: "8px" }}>
            <div style={{ display: "grid", gridTemplateColumns: "repeat(2, 1fr)", gap: "4px 16px" }}>
              <div>
                <span className="muted">累积收益（租金+领奖） </span>
                <strong>{fmtNum(curve.cumulativeUnhedgedIncomeTrx, 2)} TRX</strong>
                <span className="muted"> ≈ {fmtUsd(curve.cumulativeUnhedgedIncomeUsd)}</span>
              </div>
              {/* 对冲漂移格如实缺——NOX curve 响应无 hedgeRatioDrift* 字段（禁伪造） */}
            </div>
          </div>
        </>
      )}
      {carry && (
        <div className="hedge-carry" role="group" aria-label="carry 统计">
          <div className="hedge-carry-row">
            <span className="muted">近 7 日</span>
            <span>能量租金 {fmtNum(carry.trxRental7dTrx)} TRX</span>
            <span>投票 {fmtNum(carry.trxVote7dTrx)} TRX</span>
            <span>隔夜息 {fmtUsd(carry.cmcOvernight7dUsd)} USD</span>
          </div>
          <div className="hedge-carry-row">
            <span className="muted">本月净额</span>
            <span>{carry.mtdSampleDays > 0 ? `${fmtUsd(carry.mtdNetUsd)} USD` : "—"}</span>
            <span className="muted">{carry.mtdSampleDays > 0 ? `样本 ${carry.mtdSampleDays} 天` : ""}</span>
          </div>
          <div className="hedge-carry-row">
            <span className="muted">累计</span>
            <span>{fmtUsd(carry.cumUsd)} USD</span>
            <span className="muted">年化 {carry.annualizedPct > 0 ? `${carry.annualizedPct.toFixed(2)}%` : "—"}</span>
            {carry.hedgedCumUsd > 0 && (
              <span className="muted">对冲后 累计 {carry.hedgedCumUsd.toFixed(2)} USD · 年化 {carry.hedgedAnnualizedPct > 0 ? `${carry.hedgedAnnualizedPct.toFixed(2)}%` : "—"}</span>
            )}
          </div>
        </div>
      )}
      <div className="muted" style={{ fontSize: "0.75em", marginTop: "8px", borderTop: "1px solid var(--line)", paddingTop: "8px" }}>
        能量=实收年化（收入÷质押量）；投票=SR 报价年化；组合=三成分合成年化（能量+投票+隔夜息）。
        {radar?.updatedAt && <span> 更新于 {ts(radar.updatedAt)}。</span>}
      </div>
    </section>
  );
}
