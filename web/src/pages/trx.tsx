// trx.tsx —— TRX 页 arb TrxPage 壳逐字借形（裸 div 三区零页头——撤 .page
// 包裹+h1）：收益率（赚多少）→对冲结构（安全吗）→流水（发生了什么）→
// 监控提醒（NOX 独存件——arb 无对应区，视觉对齐 carry）。
// D-254 on-open kick=trxAccountRefresh 双踢（体感优化静默败）。
import { useEffect } from "react";
import { client } from "../api";
import { TrxYieldRadar } from "../components/TrxYieldRadar";
import { TrxHedgeCard } from "../components/TrxHedgeCard";
import { HedgeFlow } from "../components/HedgeFlow";
import { TrxWatchCard } from "../components/TrxWatchCard";

export default function TrxPage() {
  useEffect(() => {
    void client.trxAccountRefresh({}).catch(() => {});
  }, []);

  return (
    <div>
      <TrxYieldRadar />
      <TrxHedgeCard />
      <HedgeFlow />
      <TrxWatchCard />
    </div>
  );
}
