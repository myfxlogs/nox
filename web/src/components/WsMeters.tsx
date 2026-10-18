// WsMeters.tsx —— WS 重连计量卡（arb BrokersPageWsMeters 借形，D-512 T4）：
// 源健康透出只渲不算——行=source+累计重连计数+末次事件时戳；零行=诚实空态。
// case5 sourceHealth 行事件 keyed merge（NOX onCase 等价面）。
import { useCallback, useEffect, useState } from "react";
import { client } from "../api";
import { onCase } from "../stream";
import type { SourceHealth } from "../gen/dashboardv1/market_pb";
import { ts } from "./fmt";

export function WsMeters() {
  const [rows, setRows] = useState<SourceHealth[]>([]);

  const load = useCallback(async () => {
    try {
      const r = await client.listSourceHealth({});
      setRows(r.sources);
    } catch {
      /* 降级诚实空态 */
    }
  }, []);

  useEffect(() => {
    void load();
    return onCase("sourceHealth", (v) => {
      const row = v as SourceHealth;
      setRows((prev) => {
        const k = row.source || row.venue;
        const i = prev.findIndex((r) => (r.source || r.venue) === k);
        return i >= 0 ? prev.map((r, j) => (j === i ? row : r)) : [...prev, row];
      });
    });
  }, [load]);

  return (
    <details style={{ marginTop: 12 }}>
      <summary className="muted" style={{ cursor: "pointer" }}>WS 重连计量（{rows.length}）</summary>
      {rows.length === 0 ? (
        <div className="muted" style={{ fontSize: 12 }}>无 WS 源注册（流未启动）</div>
      ) : (
        <table className="rows" style={{ fontSize: 12 }}>
          <thead><tr><th>源</th><th>状态</th><th>重连计数</th><th>末次事件</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.source || r.venue}>
                <td>{r.source || r.venue}</td>
                <td>{r.state}</td>
                <td>{String(r.reconnectCount)}</td>
                <td>{ts(r.lastEventAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </details>
  );
}
