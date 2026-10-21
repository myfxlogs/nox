// orders_fmt.ts —— 订单卡中文显示映射（业主实拍三缺陷修——纯呈现层，
// 数据域/判据零动）。抽件理由：overview.tsx 棘轮 300 行门禁。
import type { OverviewOrderRow } from "../gen/dashboardv1/account_pb";
import { timestampDate } from "@bufbuild/protobuf/wkt";

// ORDER_STATUS_CN 订单状态中文映射（显示层非判据——数据域零动）。
export const ORDER_STATUS_CN: Record<string, string> = {
  // 业主定名 10-17：这面的第一语义=持仓态非成交事件——filled=持仓中（至到期/结清）。
  filled: "持仓中", settled: "已结清", failed: "未成交", pending: "下单中",
  live: "持仓中", LIVE: "持仓中", open: "持仓中", active: "持仓中",
  OPEN: "持仓中", CLOSED: "已平", proposed: "自动候执行",
  // earn 聚合卡金额源=快照桶——订单行在而快照桶缺=断供/已清如实标（禁回落订单Σ）。
  "缺快照": "快照缺",
};

// ORDER_KIND_CN 订单类别中文映射（kind 由后端 product_ref 派生）。
export const ORDER_KIND_CN: Record<string, string> = {
  earn: "理财", dci: "双币", autotrade: "策略", deposit: "入金",
};

// untilCN 到期语义分型（显示映射——活期无到期/定期到期/已过未结如实标）。
export function untilCN(o: OverviewOrderRow): string {
  if (o.kind === "earn") {
    const d = o.until ? timestampDate(o.until) : undefined;
    return d ? "申购于 " + d.toISOString().slice(5, 10) + " · 持续中" : "持续中";
  }
  if (!o.until) return "持续中";
  const t = timestampDate(o.until);
  if (t.getTime() < Date.now() && (o.status === "filled" || o.status === "pending")) {
    return "已到期待结（" + t.toISOString().replace("T", " ").slice(0, 19) + "Z）";
  }
  return "到期 " + t.toISOString().replace("T", " ").slice(0, 19) + "Z";
}
