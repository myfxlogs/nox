// investor/positions_grid.tsx —— 持仓一策略一格（第一眼层级·持仓段）。
// D-577：按后端 strategy_key 分格（禁前端猜挂——unassigned/NULL 如实落
// 「未归组」格；中文名走静态映射，错配显原 key 不造名）。
import type { Position } from "../../gen/dashboardv1/position_pb";
import { Collapse } from "../../components/collapse";
import { EmptyState } from "../../components/EmptyState";
import { fmtUsd } from "../../components/fmt";

// STRATEGY_CN fin_opp_template 键→中文名静态映射（票钉：错配显原 key 不造名，
// 禁运行时查 DB）。新模板落地=往此表补行。
const STRATEGY_CN: Record<string, string> = {
  funding_carry: "资金费套息",
  cfd_swap_carry: "CFD 换仓息",
  term_basis: "期现基差",
  spot_carry: "现货套息",
  dci_put_sell: "DCI 卖沽",
  earn_apr_lending: "活期理财",
  cash_mgmt: "现金管理",
};
function strategyCN(key: string): string {
  if (key === "__unassigned__") return "未归组";
  return STRATEGY_CN[key] ?? key; // 错配显原 key 不造名
}
// strategyGroups 按 strategyKey 分格（unassigned/缺省键→__unassigned__ 格）。
function strategyGroups(rows: Position[]): [string, Position[]][] {
  const m = new Map<string, Position[]>();
  for (const p of rows) {
    const k = p.unassigned || !p.strategyKey ? "__unassigned__" : p.strategyKey;
    const arr = m.get(k);
    if (arr) arr.push(p); else m.set(k, [p]);
  }
  return [...m.entries()];
}

export function PositionsGrid({ rows }: { rows: Position[] }) {
  return (
    <>
      {rows.length === 0 && (
        <Collapse title="持仓" hint="0 仓"><EmptyState>零在跑仓</EmptyState></Collapse>
      )}
      {strategyGroups(rows).map(([key, g]) => (
        <Collapse key={key} title={"持仓·" + strategyCN(key)} hint={g.length + " 仓"}>
          <table>
            <thead><tr><th>ID</th><th>类型</th><th>净盈亏</th><th>浮动</th><th>持有天数</th></tr></thead>
            <tbody>
              {g.map((p) => (
                <tr key={Number(p.id)}>
                  <td>{Number(p.id)}</td><td>{p.type}</td>
                  <td className={(p.netPnlUsd ?? 0) >= 0 ? "good" : "crit"}>{fmtUsd(p.netPnlUsd)}</td>
                  <td>{fmtUsd(p.floatingPnlUsd)}</td>
                  <td>{p.holdDays?.toFixed(1) ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Collapse>
      ))}
    </>
  );
}
