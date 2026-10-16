// investor.tsx —— 投资人页（S2′ arb 骨架迁入：第一眼层级段序=
// 健康→行动→裁决→持仓→下钻；巨石拆 investor/ 六件纯移位）。
// 只读露面零写径；流事件驱动重取既有 RPC（D-004 禁轮询，positions 页同件复用）。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import type { PositionDailyIncome } from "../gen/dashboardv1/account_pb";
import type { Position } from "../gen/dashboardv1/position_pb";
import type { RequestBoxItem } from "../gen/dashboardv1/request_box_pb";
import { ResolveRequestState } from "../gen/dashboardv1/request_box_pb";
import type { DCIOrderRow } from "../gen/dashboardv1/dci_panel_pb";
import type { XAssetPendingEntryRow } from "../gen/dashboardv1/graduation_pb";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { NOX_REPORT_FOOTER } from "../nox";
import { Collapse } from "../components/collapse";
import { onCase } from "../stream";
import GraduationPanel from "../components/graduation";
import { InvestorHead, type PerfLine } from "./investor/head";
import { RequestBox } from "./investor/request_box";
import { WatchPanel } from "./investor/watch";
import { PositionsGrid } from "./investor/positions_grid";
import { DciRail } from "./investor/dci";
import { IncomeLog } from "./investor/income";

export default function InvestorPage() {
  const [nav, setNav] = useState<number | null>(null);
  const [navCcy, setNavCcy] = useState<Record<string, number>>({}); // D-522④：NAV 分币种
  const [realized, setRealized] = useState<number | null>(null);
  const [today, setToday] = useState<number | null>(null);
  const [floatNet, setFloatNet] = useState<number | null>(null);
  const [ann30, setAnn30] = useState<number | null>(null);
  const [perf, setPerf] = useState<PerfLine | null>(null);
  const [income, setIncome] = useState<PositionDailyIncome[]>([]);
  const [positions, setPositions] = useState<Position[]>([]);
  const [box, setBox] = useState<RequestBoxItem[]>([]);
  const [busy, setBusy] = useState<number | null>(null);
  const [err, setErr] = useState("");
  const [orders, setOrders] = useState<DCIOrderRow[]>([]); // DCI/earn 收益轨透出（D-577）
  const [watchRows, setWatchRows] = useState<XAssetPendingEntryRow[]>([]); // D-578 待入场区

  const load = useCallback(async () => {
    try {
      const [fin, pnl, inc, pos, bx, pe] = await Promise.all([
        client.getFinanceOverview({}),
        client.getPnlAnchor({}),
        client.listDailyIncome({ limit: 30 }),
        client.listPositions({ status: "ACTIVE", limit: 100 }),
        client.listRequestBox({}),
        client.listXAssetPendingEntries({}),
      ]);
      setNav(fin.totalEquityUsd);
      setNavCcy(fin.equityByCcy ?? {});
      // D-535：业绩基线面（本金口径 USDT+USD——后端算定，前端零算）。
      if (fin.perfAvailable) {
        setPerf({
          baseline: fin.baselineUsd,
          at: fin.baselineAt ? timestampDate(fin.baselineAt) : null,
          principal: fin.principalNowUsd,
          netFlow: fin.netExternalFlowUsd,
          perf: fin.perfUsd,
          ann: fin.perfAnnPct,
          provisional: fin.perfFlowProvisional,
        });
      }
      setRealized(pnl.realizedSinceAnchor);
      setToday(pnl.realizedToday);
      setFloatNet(pnl.floatingNetUsd);
      setAnn30(pnl.netAnn30dPct);
      setIncome(inc.items);
      setPositions(pos.positions);
      setBox(bx.items);
      setWatchRows(pe.rows);
      const od = await client.listDCIOrders({});
      setOrders(od.orders);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  }, []);

  useEffect(() => {
    void load();
    // D-578/S2′：接 StreamUpdates 流件（D-004 禁轮询）——dataRefresh/
    // position/opportunity 事件驱动重取既有 RPC（与 positions 页同件复用）。
    const offs = [
      onCase("dataRefresh", () => void load()),
      onCase("position", () => void load()),
      onCase("opportunity", () => void load()),
    ];
    return () => offs.forEach((f) => f());
  }, [load]);

  // D-268 M4.6：请求箱回执——按钮只发 id+二态，态机判权全后端（前端零信任）。
  const resolve = async (id: number, state: ResolveRequestState) => {
    setBusy(id);
    try {
      await client.resolveRequest({ id: BigInt(id), state });
      const bx = await client.listRequestBox({});
      setBox(bx.items);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <div>
      {/* 第一眼层级段序（D-046 原则·业主第一眼=多少钱→该我做的→系统在等什么）：
          ①健康 NAV/业绩卡阵 ②行动 请求箱 ③裁决 毕业授权+观察中
          ④持仓 一策略一格 ⑤下钻 收益轨/日收益折叠 */}
      <InvestorHead nav={nav} navCcy={navCcy} realized={realized} today={today}
        floatNet={floatNet} ann30={ann30} perf={perf} posCount={positions.length} />
      {err && <div className="err">{err}</div>}
      <RequestBox items={box} busy={busy} resolve={resolve} />
      <Collapse title="毕业授权" hint="" defaultOpen>
        <GraduationPanel />
      </Collapse>
      <WatchPanel rows={watchRows} />
      <PositionsGrid rows={positions} />
      <DciRail orders={orders} />
      <IncomeLog items={income} />
      <div className="footer-note">{NOX_REPORT_FOOTER}</div>
    </div>
  );
}
