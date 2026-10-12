// overview.tsx —— 总览：净值卡/持仓聚合/告警/机会流/资金提醒。
// RPC 首载补全量；case 1-5/10/15/16/14(funds,finance) 流增量。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { Position } from "../gen/dashboardv1/position_pb";
import type { Alert, Opportunity } from "../gen/dashboardv1/market_pb";
import type { FundingNeedNotice } from "../gen/dashboardv1/stream_pb";
import { fmtUsd, ts } from "../components/fmt";

export default function OverviewPage() {
  const [equity, setEquity] = useState<number | null>(null);
  const [realized, setRealized] = useState<number | null>(null);
  const [floatNet, setFloatNet] = useState<number | null>(null);
  const [ann30, setAnn30] = useState<number | null>(null);
  const [positions, setPositions] = useState<Map<number, Position>>(new Map());
  const [alerts, setAlerts] = useState<Map<number, Alert>>(new Map());
  const [opps, setOpps] = useState<Opportunity[]>([]);
  const [funding, setFunding] = useState<FundingNeedNotice | null>(null);
  const [minNetBps, setMinNetBps] = useState<number | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    try {
      const [fin, pnl, pos, al, op, cfg] = await Promise.all([
        client.getFinanceOverview({}),
        client.getPnlAnchor({}),
        client.listPositions({ status: "ACTIVE", limit: 200 }),
        client.listAlerts({ limit: 20 }),
        client.listOpportunities({ limit: 20 }),
        client.getFrontConfig({}),
      ]);
      setEquity(fin.totalEquityUsd);
      setRealized(fin.totalRealizedUsd);
      setFloatNet(pnl.floatingNetUsd);
      setAnn30(pnl.netAnn30dPct);
      setPositions(new Map(pos.positions.map((p) => [Number(p.id), p])));
      setAlerts(new Map(al.alerts.map((a) => [Number(a.id), a])));
      setOpps(op.opportunities);
      setMinNetBps(cfg.notifyMinNetBps);
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
        setPositions((m) => new Map(m).set(Number(p.id), p));
      }),
      onCase("alert", (v) => {
        const a = v as Alert;
        setAlerts((m) => new Map(m).set(Number(a.id), a));
      }),
      onCase("opportunity", (v) => {
        const o = v as Opportunity;
        setOpps((l) => [o, ...l.filter((x) => x.id !== o.id)].slice(0, 20));
      }),
      onCase("fundingNeed", (v) => setFunding(v as FundingNeedNotice)),
      onCase("dataRefresh", (v) => {
        const t = (v as { topic: string }).topic;
        if (t === "funds" || t === "finance") void load();
      }),
    ];
    return () => offs.forEach((f) => f());
  }, [load]);

  const posList = [...positions.values()];
  const alertList = [...alerts.values()];
  return (
    <div>
      {err && <div className="err">{err}</div>}
      {funding && (
        <div className="card crit" style={{ marginBottom: 8 }}>
          资金需求：{funding.canonical} {funding.aliasLong || funding.venueLong}→
          {funding.aliasShort || funding.venueShort} 缺 {fmtUsd(funding.needUsd)}（{funding.reason}）
        </div>
      )}
      <div className="cards">
        <div className="card"><div className="k">总权益</div><div className="v">{fmtUsd(equity)}</div></div>
        <div className="card"><div className="k">已实现累计</div><div className="v">{fmtUsd(realized)}</div></div>
        <div className="card"><div className="k">浮动净值</div><div className="v">{fmtUsd(floatNet)}</div></div>
        <div className="card"><div className="k">30d 净年化</div><div className="v">{ann30 == null ? "—" : ann30.toFixed(2) + "%"}</div></div>
        <div className="card"><div className="k">活跃持仓</div><div className="v">{posList.length}</div></div>
      </div>

      <h2>持仓</h2>
      <table>
        <thead><tr><th>ID</th><th>类型</th><th>状态</th><th>预期净bps</th><th>净盈亏</th><th>开仓时间</th></tr></thead>
        <tbody>
          {posList.map((p) => (
            <tr key={Number(p.id)}>
              <td>{Number(p.id)}</td><td>{p.type}</td><td>{p.status}</td>
              <td>{p.expectedNetBps.toFixed(1)}</td>
              <td className={(p.netPnlUsd ?? 0) >= 0 ? "good" : "crit"}>{fmtUsd(p.netPnlUsd)}</td>
              <td>{ts(p.openedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>机会流</h2>
      <table>
        <thead><tr><th>品种</th><th>买</th><th>卖</th><th>净bps</th><th>净USD</th><th>过期</th></tr></thead>
        <tbody>
          {opps.map((o) => (
            <tr key={o.id} className={minNetBps != null && o.netBps < minNetBps ? "muted" : ""}>
              <td>{o.type}</td>
              <td>{o.aliasBuy || o.venueBuy}</td>
              <td>{o.aliasSell || o.venueSell}</td>
              <td className={o.netBps >= 0 ? "good" : "crit"}>{o.netBps.toFixed(2)}</td>
              <td>{fmtUsd(o.netUsd)}</td>
              <td>{ts(o.expiresAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>告警</h2>
      <table>
        <thead><tr><th>时间</th><th>级别</th><th>内容</th></tr></thead>
        <tbody>
          {alertList.map((a) => (
            <tr key={Number(a.id)}>
              <td>{ts(a.ts)}</td>
              <td className={a.level === "crit" ? "crit" : a.level === "warn" ? "warn" : ""}>{a.level}</td>
              <td style={{ whiteSpace: "normal" }}>{a.message}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
