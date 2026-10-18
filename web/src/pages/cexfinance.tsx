// cexfinance.tsx —— CEX 财务页（arb CexFinancePage 借形 D-365）：每 real CEX
// 账户一卡（USDT 权益大字+盘点总值），体部两栏「资产明细（全币种）」+
// 「进出/收益流水」——weight_pct/usdt_equity 全后端算定直渲零前端换算。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { GetCexFinanceResponse } from "../gen/dashboardv1/ops_pb";
import { StatTile } from "../components/StatTile";
import { EmptyState } from "../components/EmptyState";
import { fmtUsd } from "../components/fmt";

export default function CexFinancePage() {
  const [resp, setResp] = useState<GetCexFinanceResponse | null>(null);
  const [err, setErr] = useState("");
  const load = useCallback(async () => {
    try { setResp(await client.getCexFinance({})); setErr(""); } catch (e) { setErr(errText(e)); }
  }, []);
  useEffect(() => {
    void load();
    return onCase("dataRefresh", (v) => {
      const t = (v as { topic: string }).topic;
      if (t === "cex_scan" || t === "funds") void load();
    });
  }, [load]);
  return (
    <div className="page">
      <h1>CEX 财务</h1>
      {err && <div className="err">{err}</div>}
      {resp && resp.accounts.length === 0 && <EmptyState>无 real CEX 配置（sim/testnet 不入）</EmptyState>}
      {resp?.accounts.map((a) => (
        <section key={a.key}>
          <h2>{a.venue}/{a.venueType}{a.simulated ? "（sim）" : ""}</h2>
          <div className="stats">
            <StatTile label="USDT 权益" value={fmtUsd(a.usdtEquity)} />
            <StatTile label="盘点总值" value={fmtUsd(a.inventoryUsd)} />
          </div>
          <div className="grid grid-funds">
            <div className="card">
              <h3 className="muted">资产明细（全币种）</h3>
              <div className="table-scroll">
                <table className="rows">
                  <thead><tr><th>币种</th><th>数量</th><th>估值 USD</th><th>占比</th></tr></thead>
                  <tbody>
                    {a.holdings.map((h) => (
                      <tr key={h.ccy}>
                        <td>{h.ccy}</td><td className="num">{h.qty}</td>
                        <td className="num">{fmtUsd(h.usdtValue)}</td>
                        <td className="num">{h.weightPct.toFixed(2)}%</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
            <div className="card">
              <h3 className="muted">流水账（进出+收益实收）</h3>
              <div className="table-scroll">
                <table className="rows">
                  <thead><tr><th>时点</th><th>类型</th><th>币种</th><th>金额</th></tr></thead>
                  <tbody>
                    {a.flows.map((f, i) => (
                      <tr key={i}>
                        <td className="num">{f.ts.slice(0, 16)}</td>
                        <td>{f.kind}</td><td>{f.ccy}</td><td className="num">{f.amount}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </section>
      ))}
    </div>
  );
}
