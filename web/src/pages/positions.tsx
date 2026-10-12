// positions.tsx —— 持仓页：系统仓（case4）+ 券商逐仓（case15 upsert/remove）。
// 写径：CloseBrokerPosition / CloseBrokerPositions（D-213 钦定运维操作）。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { BrokerPosition, Position } from "../gen/dashboardv1/position_pb";
import type { BrokerPositionEvent } from "../gen/dashboardv1/stream_pb";
import { fmtUsd, ts } from "../components/fmt";

const key = (p: { venueKey: string; ticket: string }) => p.venueKey + "/" + p.ticket;

export default function PositionsPage() {
  const [sysPos, setSysPos] = useState<Map<number, Position>>(new Map());
  const [broker, setBroker] = useState<Map<string, BrokerPosition>>(new Map());
  const [sel, setSel] = useState<Set<string>>(new Set());
  const [sums, setSums] = useState<{ pnl?: number; net?: number }>({});
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [p, b] = await Promise.all([
        client.listPositions({ limit: 500 }),
        client.listBrokerPositions({ refresh: false }),
      ]);
      setSysPos(new Map(p.positions.map((x) => [Number(x.id), x])));
      setBroker(new Map(b.positions.map((x) => [key(x), x])));
      setSums({ pnl: b.pnlSumUsd, net: b.netSumUsd });
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  }, []);

  useEffect(() => {
    void load();
    const offs = [
      onCase("position", (v) => {
        const p = v as Position;
        setSysPos((m) => new Map(m).set(Number(p.id), p));
      }),
      onCase("brokerPositionEvent", (v) => {
        const e = v as BrokerPositionEvent;
        setSums({ pnl: e.pnlSumUsd, net: e.netSumUsd });
        if (e.kind.case === "upsert" && e.kind.value) {
          const p = e.kind.value;
          setBroker((m) => new Map(m).set(key(p), p));
        } else if (e.kind.case === "remove" && e.kind.value) {
          const r = e.kind.value;
          setBroker((m) => {
            const n = new Map(m);
            n.delete(key(r));
            return n;
          });
        }
      }),
      onCase("dataRefresh", (v) => {
        if ((v as { topic: string }).topic === "finance") void load();
      }),
    ];
    return () => offs.forEach((f) => f());
  }, [load]);

  async function closeOne(p: BrokerPosition) {
    setBusy(true);
    try {
      await client.closeBrokerPosition({ venueKey: p.venueKey, ticket: p.ticket });
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  }

  async function closeSelected() {
    setBusy(true);
    try {
      await client.closeBrokerPositions({
        items: [...sel].map((k) => {
          const p = broker.get(k)!;
          return { venueKey: p.venueKey, ticket: p.ticket };
        }),
      });
      setSel(new Set());
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  }

  const bList = [...broker.values()];
  return (
    <div>
      {err && <div className="err">{err}</div>}
      <div className="cards">
        <div className="card"><div className="k">券商仓 pnl 合计</div><div className="v">{fmtUsd(sums.pnl)}</div></div>
        <div className="card"><div className="k">净额合计</div><div className="v">{fmtUsd(sums.net)}</div></div>
        <div className="card">
          <div className="k">已选 {sel.size}</div>
          <button className="danger" disabled={busy || sel.size === 0} onClick={closeSelected}>平仓所选</button>
        </div>
      </div>

      <h2>券商逐仓</h2>
      <table>
        <thead>
          <tr><th></th><th>账户</th><th>品种</th><th>向</th><th>量</th><th>现价</th><th>pnl</th><th>swap</th><th>开时</th><th></th></tr>
        </thead>
        <tbody>
          {bList.map((p) => (
            <tr key={key(p)}>
              <td>
                <input
                  type="checkbox"
                  checked={sel.has(key(p))}
                  onChange={(e) =>
                    setSel((s) => {
                      const n = new Set(s);
                      e.target.checked ? n.add(key(p)) : n.delete(key(p));
                      return n;
                    })
                  }
                />
              </td>
              <td>{p.accountAlias || p.venueKey}</td>
              <td>{p.symbol}</td>
              <td>{p.side}</td>
              <td>{p.volume}</td>
              <td>{p.currentPrice || "—"}</td>
              <td className={p.pnl >= 0 ? "good" : "crit"}>{fmtUsd(p.pnl)}</td>
              <td>{fmtUsd(p.swap)}</td>
              <td>{ts(p.openedAt)}</td>
              <td><button disabled={busy} onClick={() => closeOne(p)}>平仓</button></td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>系统仓</h2>
      <table>
        <thead><tr><th>ID</th><th>类型</th><th>状态</th><th>净盈亏</th><th>浮动</th><th>腿数</th><th>开仓</th><th>平仓</th></tr></thead>
        <tbody>
          {[...sysPos.values()].map((p) => (
            <tr key={Number(p.id)}>
              <td>{Number(p.id)}</td><td>{p.type}</td><td>{p.status}</td>
              <td className={(p.netPnlUsd ?? 0) >= 0 ? "good" : "crit"}>{fmtUsd(p.netPnlUsd)}</td>
              <td>{fmtUsd(p.floatingPnlUsd)}</td>
              <td>{p.legs.length}</td>
              <td>{ts(p.openedAt)}</td>
              <td>{ts(p.closedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
