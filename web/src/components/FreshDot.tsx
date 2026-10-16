import type { FreshDot as FreshDotData } from "./freshness";

// FreshDot 源新鲜度状态点（机会 tile / 矩阵格共用；D-047 F6 从 Matrix/StatTile
// 内联渲染收敛为单一组件——同一视觉标记只实现一次）。
export function FreshDot({ dot }: { dot: FreshDotData }) {
  return (
    <i
      className={`fresh-dot ${dot.status}`}
      title={dot.title}
      aria-hidden="true"
    />
  );
}
