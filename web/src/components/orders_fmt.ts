// orders_fmt.ts —— 订单卡中文显示映射（业主实拍三缺陷修——纯呈现层，
// 数据域/判据零动）。抽件理由：overview.tsx 棘轮 300 行门禁。
import type { OverviewOrderRow } from "../gen/dashboardv1/account_pb";
import { timestampDate } from "@bufbuild/protobuf/wkt";

// ORDER_STATUS_CN 订单状态中文映射（显示层非判据——数据域零动）。
export const ORDER_STATUS_CN: Record<string, string> = {
  filled: "已成交", settled: "已结清", failed: "失败", pending: "待成交",
  live: "持有中", LIVE: "持有中", open: "持有中", active: "持有中",
  OPEN: "持有中", CLOSED: "已平", proposed: "待定",
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
