// positions.tsx —— 持仓页：系统仓（case4）+ 券商逐仓（case15 upsert/remove）。
// 写径：CloseBrokerPosition / CloseBrokerPositions（D-213 钦定运维操作）。
// S3′-final 批迁：第一眼=券商仓 PnL/净额 StatTile + 逐仓 opp-card 阵（卡内
// 勾选/平仓绑径原样），系统仓台账同卡化；纯 JSX/CSS 重排零逻辑动。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { BrokerPosition, Position } from "../gen/dashboardv1/position_pb";
import type { BrokerPositionEvent } from "../gen/dashboardv1/stream_pb";
import { fmtUsd, ts } from "../components/fmt";
import { StatTile } from "../components/StatTile";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";

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
      <h1>持仓</h1>
      {err && <div className="err">{err}</div>}

      {/* 第一眼=券商仓损益合计（后端 sums 直供单值）+ 批平仓动作 */}
      <div className="stats">
        <StatTile label="券商仓 PnL 合计" value={fmtUsd(sums.pnl)} />
        <StatTile label="净额合计" value={fmtUsd(sums.net)} />
      </div>
      <div style={{ margin: "8px 0" }}>
        <button className="danger" disabled={busy || sel.size === 0} onClick={closeSelected}>
          平仓所选（{sel.size}）
        </button>
      </div>

      <h2>券商逐仓</h2>
      {bList.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {bList.map((p) => (
            <li key={key(p)} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">
                  {p.symbol}
                  {p.external && (
                    <span className="warn" title="系统外单：台账查无记录——敞口并表但系统不开不平"> 外单</span>
                  )}
                </span>
                <Chip tone={p.pnl >= 0 ? "good" : "critical"}>{p.side}</Chip>
                <span className="opp-card-who">{p.accountAlias || p.venueKey}</span>
              </div>
              <div className="opp-card-stats stats">
                <StatTile label="pnl" value={fmtUsd(p.pnl)} />
                <StatTile label="swap" value={fmtUsd(p.swap)} />
                <StatTile label="现价" value={String(p.currentPrice || "—")} />
                <StatTile label="量" value={String(p.volume)} />
              </div>
              <div className="opp-card-narrative">
                <label>
                  <input
                    type="checkbox"
                    disabled={p.external}
                    checked={!p.external && sel.has(key(p))}
                    onChange={(e) =>
                      setSel((s) => {
                        const n = new Set(s);
                        e.target.checked ? n.add(key(p)) : n.delete(key(p));
                        return n;
                      })
                    }
                  />{" "}
                  勾选批平
                </label>{" "}
                {p.external ? (
                  <span style={{ opacity: 0.5 }}>不接管</span>
                ) : (
                  <button disabled={busy} onClick={() => closeOne(p)}>平仓</button>
                )}{" "}
                <span className="muted">开 {ts(p.openedAt)}</span>
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零券商仓</EmptyState>
      )}

      <h2>系统仓台账</h2>
      {sysPos.size > 0 ? (
        <ul className="opp-cards opp-grid">
          {[...sysPos.values()].map((p) => (
            <li key={Number(p.id)} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">#{Number(p.id)} {p.type}</span>
                <Chip tone={p.status === "ACTIVE" ? "good" : "neutral"}>{p.status}</Chip>
              </div>
              <div className="opp-card-stats stats">
                <StatTile label="净盈亏" value={fmtUsd(p.netPnlUsd)} />
                <StatTile label="浮动" value={fmtUsd(p.floatingPnlUsd)} />
                <StatTile label="腿数" value={String(p.legs.length)} />
              </div>
              <div className="opp-card-narrative">
                开 {ts(p.openedAt)} · 平 {ts(p.closedAt)}
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零系统仓</EmptyState>
      )}
    </div>
  );
}
