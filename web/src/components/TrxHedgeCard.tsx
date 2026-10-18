// TrxHedgeCard.tsx —— 对冲结构卡（arb 同名件借形，D-212 对冲工作台）：
// 镜像双腿表（开仓价/现价/浮动盈亏/杠杆/强平价/距强平%）+净敞口徽章+carry
// 统计条+双账户块+↻ 刷新钮。arb 原型 setTimeout(2s) 等待拍=前端人为延时——
// NOX 改流帧驱动：kick trxAccountRefresh 后等 onCase("dataRefresh") trx 帧
// 重取 getHedgeStructure（流先零定时器令，票面明令）。
import { useCallback, useEffect, useState } from "react";
import { client } from "../api";
import { onCase } from "../stream";
import type { HedgeStructure } from "../gen/dashboardv1/trx_cex_pb";
import { fmtUsd, fmtNum, fmtPrice, fmtThr, pnlClass, fmtSigned } from "./fmt";

export function TrxHedgeCard() {
  const [structure, setStructure] = useState<HedgeStructure | null>(null);
  const [trxRefreshing, setTrxRefreshing] = useState(false);

  const refetch = useCallback(async () => {
    try {
      const hs = await client.getHedgeStructure({});
      if (hs.structure) setStructure(hs.structure);
    } catch { /* 体感件静默败——下帧自然回填 */ }
  }, []);

  // 挂载首取+trx 流帧重取（arb setTimeout(2s) 等待拍→流帧驱动）。
  useEffect(() => {
    void refetch();
    return onCase("dataRefresh", (v) => {
      if ((v as { topic: string }).topic === "trx") void refetch();
    });
  }, [refetch]);

  const ready = structure && structure.mid > 0;
  const legSpot = structure?.legSpot;
  const legShort = structure?.legShort;
  const carry = structure?.carry;
  const accts = structure?.accts;

  return (
    <section className="card">
      <div className="card-head">
        <h2>对冲结构</h2>
        <span className="ws-dot-wrap" title={structure?.wsConnected ? "WS 在线" : "REST 兜底"}>
          <span className={`ws-dot ${structure?.wsConnected ? "ws-dot-ok" : "ws-dot-warn"}`} />
          <span className="muted">{structure?.wsConnected ? "WS 在线" : "REST 兜底"}</span>
        </span>
        {ready && structure!.platformAlertState && structure!.platformAlertState !== "normal" && (
          <span className="ws-dot-wrap" title={`平台预警: ${structure!.platformAlertState}`}>
            <span className="ws-dot ws-dot-warn" />
            <span className="muted">{structure!.platformAlertState === "breach" ? "平台强平触碰" : "平台预警"}</span>
          </span>
        )}
      </div>
      {!ready && <div className="empty" style={{ padding: "12px 0" }}>数据未就绪</div>}
      {ready && (
        <>
          <div className="table-scroll scroll-cap">
            <table className="rows">
              <thead>
                <tr><th>腿</th><th>数量</th><th>开仓价</th><th>现价</th><th>浮动盈亏</th><th>杠杆</th><th>强平价</th><th>距强平%</th></tr>
              </thead>
              <tbody>
                <tr>
                  <td>{legSpot?.label ?? "质押现货"}</td>
                  <td>质押 {fmtNum(legSpot?.qtyStaked ?? 0)} TRX</td>
                  <td>{fmtPrice(structure!.baselinePrice)}</td>
                  <td>{fmtPrice(structure!.ask || structure!.mid)}</td>
                  <td className={pnlClass(legSpot?.pnlVsBase ?? 0, true)}>{fmtUsd(legSpot?.pnlVsBase ?? 0)}</td>
                  <td>—</td><td>—</td><td>—</td>
                </tr>
                <tr>
                  <td>{legShort?.label ?? "CMC 空单"}</td>
                  <td>{fmtNum(legShort?.qty ?? 0)} TRX</td>
                  <td>{fmtPrice(structure!.baselinePrice)}</td>
                  <td>{fmtPrice(structure!.ask || structure!.mid)}</td>
                  <td className={pnlClass(legShort?.pnlVsBase ?? 0, true)}>{fmtUsd(legShort?.pnlVsBase ?? 0)}</td>
                  <td>{(legShort?.lev ?? 0) > 0 ? `${legShort!.lev}×` : "—"}</td>
                  <td>{(legShort?.liq ?? 0) > 0 ? (
                    <span title={structure!.closeoutSource === "platform_rule_50"
                      ? `平台口径 · 50% 规则\nCMC Balance 面板标定：强平=保证金 50%，预警=保证金 80%\n强平净值 ${fmtUsd(structure!.closeoutLevelUsd)}\n预警净值 ${fmtUsd(structure!.warningLevelUsd)}`
                      : undefined}>{fmtThr(legShort!.liq)}</span>
                  ) : "—"}</td>
                  <td className={(legShort?.distPct ?? 0) < 0 ? "neg" : ""}>
                    {(legShort?.distPct ?? 0) > 0 ? legShort!.distPct.toFixed(2) + "%" : "—"}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <div className="banner" role="status">
            <span>
              净敞口 <strong>{fmtSigned(structure!.netExposureTrx)} TRX</strong>
              （≈ {fmtUsd(structure!.netExposureUsd)} USD）
              <span className="muted" style={{ marginLeft: 8 }}>· 近似中性</span>
            </span>
          </div>
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
          {accts && (
            <div className="hedge-accts" role="group" aria-label="双账户摘要">
              <div className="hedge-acct-block">
                <h3 className="muted">
                  TRX 账户
                  {/* D-254：kick trxAccountRefresh→等 trx 流帧回填（setTimeout 等待拍禁搬入） */}
                  <button className="trx-refresh-btn" disabled={trxRefreshing}
                    onClick={async () => {
                      if (trxRefreshing) return;
                      setTrxRefreshing(true);
                      try { await client.trxAccountRefresh({}); void refetch(); }
                      catch { /* 体感件静默败 */ }
                      finally { setTrxRefreshing(false); }
                    }}
                    aria-label="刷新 TRX 账户" title="刷新 TRX 账户（双踢采集+流帧回填）">
                    <span className={trxRefreshing ? "refresh-spin" : ""}>↻</span>
                  </button>
                </h3>
                <div>余额 {fmtNum(accts.trxBalance, 3)} TRX</div>
                <div title="tronscan rewardNum 链上真值（与 TronLink 一致），每 30s 采集">待领取 {fmtNum(accts.trxAllowance, 3)} TRX</div>
                <div>能量委托 {fmtNum(accts.energyDelegated, 3)} TRX</div>
                <div>票权 {fmtNum(accts.votes)}</div>
                {accts.netLimit > 0 && (
                  <div className="muted" title={`带宽上限 ${fmtNum(accts.netLimit + accts.freeNetLimit, 0)} BP`}>
                    剩余带宽 {fmtNum((accts.freeNetLimit - accts.freeNetUsed) + (accts.netLimit - accts.netUsed), 0)} BP
                    {accts.bandwidthStatus === "red" && <span className="u-bad"> · 红</span>}
                    {accts.bandwidthStatus === "yellow" && <span className="u-orange"> · 黄</span>}
                  </div>
                )}
                {accts.energyLimit > 0 && (
                  <div className="muted" title={`能量上限 ${fmtNum(accts.energyLimit, 0)} Energy`}>
                    剩余能量 {fmtNum(accts.energyLimit - accts.energyUsed, 0)} Energy
                  </div>
                )}
              </div>
              <div className="hedge-acct-block">
                <h3 className="muted">CMC 账户</h3>
                <div>现金 {fmtUsd(accts.cmcCash)} USD</div>
                <div>净值 {fmtUsd(accts.cmcEquity)} USD</div>
                <div className="muted">强平净值 {fmtUsd(structure!.closeoutLevelUsd)} / 预警净值 {fmtUsd(structure!.warningLevelUsd)}</div>
                <div className="muted" title="平台口径 · 50% 规则：CMC Balance 面板标定，强平=保证金 50%，预警=保证金 80%；规则变更后需复核">口径：强平=保证金 50%，预警=80%（平台标定）</div>
              </div>
            </div>
          )}
        </>
      )}
    </section>
  );
}
