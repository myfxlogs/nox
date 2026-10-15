// overview.tsx —— D-536 总览页五段（§0 待办→§1 本金→§2 业绩→§3 在仓订单→§4 风险心跳）。
// 前端零运算只渲染；缺数据显缺不显 0；时戳 UTC 口径（业主令 10-15——venue/CFD 全 UTC）。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { GetOverviewBundleResponse, OverviewOrderRow } from "../gen/dashboardv1/account_pb";
import type { RequestBoxItem } from "../gen/dashboardv1/request_box_pb";
import { ResolveRequestState } from "../gen/dashboardv1/request_box_pb";
import { fmtUsd } from "../components/fmt";

const utc = (t: Date | undefined) =>
  t ? t.toISOString().replace("T", " ").slice(0, 19) + "Z" : "—";

export default function OverviewPage() {
  const [b, setB] = useState<GetOverviewBundleResponse | null>(null);
  const [box, setBox] = useState<RequestBoxItem[]>([]);
  const [busy, setBusy] = useState<number | null>(null);
  const [err, setErr] = useState("");

  const load = async () => {
    try {
      const [bd, bx] = await Promise.all([
        client.getOverviewBundle({}),
        client.listRequestBox({}),
      ]);
      setB(bd); setBox(bx.items); setErr("");
    } catch (e) { setErr(errText(e)); }
  };
  useEffect(() => { void load(); }, []);

  const resolve = async (id: number, state: ResolveRequestState) => {
    setBusy(id);
    try {
      await client.resolveRequest({ id: BigInt(id), state });
      await load();
    } catch (e) { setErr(errText(e)); } finally { setBusy(null); }
  };

  const pending = box; // 请求箱行本身即待办面
  return (
    <div>
      <h1>总览</h1>
      {err && <div className="err">{err}</div>}

      {/* §0 待办横幅 */}
      {pending.length > 0 && (
        <div className="card" style={{ borderColor: "#d97706" }}>
          <h2>待办（{pending.length}）</h2>
          <table><tbody>
            {pending.map((r) => (
              <tr key={Number(r.id)}>
                <td>#{Number(r.id)}</td><td>{r.kind}</td><td>{r.subject}</td>
                <td className="muted">{r.requestText}</td>
                <td>
                  <button disabled={busy === Number(r.id)} onClick={() => resolve(Number(r.id), ResolveRequestState.FULFILLED)}>已喂条件</button>{" "}
                  <button disabled={busy === Number(r.id)} onClick={() => resolve(Number(r.id), ResolveRequestState.WAIVED)}>放弃</button>
                </td>
              </tr>
            ))}
          </tbody></table>
        </div>
      )}

      {/* §1 本金条（USDT+USD，D-534） */}
      <h2>本金（USDT+USD）</h2>
      <div className="cards">
        <div className="card"><div className="k">本金合计</div><div className="v">{b ? fmtUsd(b.principalTotalUsd) : "—"}</div></div>
        <div className="card"><div className="k">可动用</div><div className="v">{b ? fmtUsd(b.principalIdleUsd) : "—"}</div></div>
        <div className="card"><div className="k">在息</div><div className="v">{b ? fmtUsd(b.principalEarningUsd) : "—"}</div></div>
        <div className="card"><div className="k">锁定</div><div className="v">{b ? fmtUsd(b.principalLockedUsd) : "—"}</div>
          {b?.earliestRelease && <div className="muted" style={{fontSize:"12px"}}>最早回笼 {utc(timestampDate(b.earliestRelease))}</div>}
        </div>
      </div>

      {/* §2 业绩条（D-535 基线） */}
      <h2>业绩</h2>
      {b?.perfAvailable ? (
        <div className="cards">
          <div className="card"><div className="k">基线</div><div className="v">{fmtUsd(b.baselineUsd)}</div>
            <div className="muted" style={{fontSize:"12px"}}>{utc(b.baselineAt ? timestampDate(b.baselineAt) : undefined)} 起算</div></div>
          <div className="card"><div className="k">基线后业绩{b.perfFlowProvisional ? "（provisional）" : ""}</div>
            <div className={b.perfUsd >= 0 ? "v good" : "v crit"}>{(b.perfUsd >= 0 ? "+" : "") + fmtUsd(b.perfUsd)}</div>
            <div className="muted" style={{fontSize:"12px"}}>年化 {b.perfAnnPct.toFixed(2)}%</div></div>
        </div>
      ) : <div className="muted">业绩基线未配置（perf_baseline_*）</div>}

      {/* §3 在仓订单统一表 */}
      <h2>在仓订单</h2>
      {b && b.orders.length > 0 ? (
        <table>
          <thead><tr><th>类</th><th>对象</th><th>额 USD</th><th>态</th><th>时点（UTC）</th></tr></thead>
          <tbody>
            {b.orders.map((o: OverviewOrderRow, i: number) => (
              <tr key={i}>
                <td>{o.kind}</td><td>{o.label}</td>
                <td>{fmtUsd(o.amountUsd)}</td><td>{o.status}</td>
                <td className="muted">{o.until ? utc(timestampDate(o.until)) : "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : <div className="muted">无在仓订单</div>}
      {b && b.paperCount > 0 && <div className="muted">纸面沙箱 {b.paperCount} 条（不入本金）</div>}

      {/* §4 风险心跳 */}
      <h2>风险心跳</h2>
      <div className="cards">
        <div className="card"><div className="k">KillSwitch</div>
          <div className={b?.killSwitch ? "v crit" : "v good"}>{b ? (b.killSwitch ? "ON" : "off") : "—"}</div></div>
        <div className="card"><div className="k">30d 净额（对 −3% 硬线）</div>
          <div className={b && b.net30Usd < 0 ? "v crit" : "v"}>{b ? fmtUsd(b.net30Usd) : "—"}</div></div>
        <div className="card"><div className="k">24h critical</div>
          <div className={b && b.critAlerts24h > 0 ? "v crit" : "v"}>{b ? String(b.critAlerts24h) : "—"}</div></div>
      </div>
    </div>
  );
}
