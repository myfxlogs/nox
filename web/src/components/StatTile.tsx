import type { FreshDot } from "./freshness";
import { FreshDot as FreshDotDot } from "./FreshDot";

// StatTile KPI 瓦片：单一当前值（无图表的正确形态）。
export function StatTile({
  label,
  value,
  sub,
  title,
  dot,
}: {
  label: string;
  value: string;
  sub?: string;
  title?: string;
  dot?: FreshDot;
}) {
  return (
    <div className="stat" title={title}>
      <div className="stat-label">
        {dot ? <FreshDotDot dot={dot} /> : null}
        {label}
      </div>
      <div className="stat-value">{value}</div>
      {sub ? <div className="stat-sub">{sub}</div> : null}
    </div>
  );
}
