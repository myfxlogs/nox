// investor/income.tsx —— 日收益流水（下钻段折叠）。
import type { PositionDailyIncome } from "../../gen/dashboardv1/account_pb";
import { Collapse } from "../../components/collapse";
import { fmtUsd } from "../../components/fmt";

export function IncomeLog({ items }: { items: PositionDailyIncome[] }) {
  return (
    <Collapse title="日收益（近 30 条）" hint={items.length + " 条"}>
      <table>
        <thead><tr><th>日</th><th>收益</th><th>pnl</th><th>note</th></tr></thead>
        <tbody>
          {items.map((d) => (
            <tr key={Number(d.id)}>
              <td>{d.day}</td>
              <td className={d.incomeUsd >= 0 ? "good" : "crit"}>{fmtUsd(d.incomeUsd)}</td>
              <td>{fmtUsd(d.pnlUsd)}</td>
              <td className="muted">{d.note}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Collapse>
  );
}
