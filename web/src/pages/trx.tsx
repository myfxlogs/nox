// trx.tsx —— TRX 页 arb 原版式三卡重排（业主令 10-19）+监控提醒 TrxWatchCard
// 件（arb PriceAlertCard 法——300 闸拆件）：收益率（TrxYieldRadar 6 格 grid）
// →对冲结构（TrxHedgeCard 镜像双腿表+净敞口+carry 条+双账户）→统一流水
// （HedgeFlow 页码式真分页）→监控提醒（watch_rule 域全法卡）。
// on-open trxAccountRefresh 双踢（D-254）。只读监控+提醒设置零自动执行面。
import { useEffect } from "react";
import { client } from "../api";
import { TrxYieldRadar } from "../components/TrxYieldRadar";
import { TrxHedgeCard } from "../components/TrxHedgeCard";
import { HedgeFlow } from "../components/HedgeFlow";
import { TrxWatchCard } from "../components/TrxWatchCard";

export default function TrxPage() {
  // D-254：on-open kick——TRX 页挂载即触发后端双踢 trx_account+trx_apr
  // （体感优化，静默败——下帧自然回填）。
  useEffect(() => {
    void client.trxAccountRefresh({}).catch(() => {});
  }, []);

  return (
    <div className="page">
      <h1>TRX <small className="muted" style={{ fontSize: 12 }}>手动投资页·只读监控+提醒</small></h1>
      <TrxYieldRadar />
      <TrxHedgeCard />
      <HedgeFlow />
      <TrxWatchCard />
    </div>
  );
}
