import type { FreshDot } from "./freshness";
import { FreshDot as FreshDotDot } from "./FreshDot";

// StatTile KPI 瓦片：单一当前值（无图表的正确形态）。
// onClick 提供时瓦片可点（组成明细下钻——active 标当前展开态）。
export function StatTile({
  label,
  value,
  sub,
  title,
  dot,
  onClick,
  active,
  valueClass,
}: {
  label: string;
  value: string;
  sub?: string;
  title?: string;
  dot?: FreshDot;
  onClick?: () => void;
  active?: boolean;
  // 值的语义色类（u-good/u-bad 等——20-semantic 件；呈现层映射非判据）
  valueClass?: string;
}) {
  return (
    <div
      className={`stat${onClick ? " stat-click" : ""}${active ? " stat-active" : ""}`}
      /* 实时卡防抖：sub 单行钳制后全文兜底——调用方未传 title 时缺省=值｜sub */
      title={title ?? (sub ? `${value}｜${sub}` : value)}
      onClick={onClick}
      role={onClick ? "button" : undefined}
      tabIndex={onClick ? 0 : undefined}
      onKeyDown={onClick ? (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onClick(); } } : undefined}
    >
      <div className="stat-label">
        {dot ? <FreshDotDot dot={dot} /> : null}
        {label}
      </div>
      <div className={valueClass ? `stat-value ${valueClass}` : "stat-value"}>{value}</div>
      {sub ? <div className="stat-sub">{sub}</div> : null}
    </div>
  );
}
