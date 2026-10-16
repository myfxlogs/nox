// investor/watch.tsx —— 观察中面板（第一眼层级·裁决段：系统在等什么）。
// D-578：armed 待入场区 ListXAssetPendingEntries 单源读面——
// wait_reason/ready/差值全后端算定，前端零重算；空态显式非段消失。
import type { XAssetPendingEntryRow } from "../../gen/dashboardv1/graduation_pb";
import { Chip } from "../../components/Chip";
import { EmptyState } from "../../components/EmptyState";
import { Collapse } from "../../components/collapse";

export function WatchPanel({ rows }: { rows: XAssetPendingEntryRow[] }) {
  return (
    <Collapse title="观察中（armed 待入场）" hint={rows.length + " 组合"} defaultOpen>
      {rows.length === 0 ? (
        <EmptyState>引擎评估中·暂无候选</EmptyState>
      ) : (
        <table>
          <thead><tr><th>组合</th><th>类别</th><th>拦路原因</th><th>差值 bps</th><th>状态</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={Number(r.comboId)}>
                <td>{r.comboLabel}</td>
                <td>{r.entryKind}</td>
                <td className="muted">{r.waitReason || "—"}</td>
                <td>{r.waitGapBps != null ? r.waitGapBps.toFixed(1) : "—"}</td>
                <td>{r.ready
                  ? <Chip tone="good">就绪·等机会</Chip>
                  : <Chip tone="warn">未过闸</Chip>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Collapse>
  );
}
