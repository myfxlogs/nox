// capital.tsx —— 资金地图（业主令 10-13 + D-285）：后端聚合池级 权益/闲置/在跑，
// 按币种分组各报各的零换算。纯渲染零运算；幽灵池独立段，陈旧行置灰标时点。
// S3′-final 批迁：第一眼=主币权益/闲置/在跑 StatTile+池 opp-card 阵（幽灵/陈旧
// 徽标原语义）；幽灵/其他 Modal 与手工仓位段绑径原样。纯 JSX/CSS 重排零逻辑动。
import { useEffect, useState } from "react";
import { Modal } from "../components/Modal";
import { client, errText } from "../api";
import type { CapitalPoolsGroup, CapitalPool } from "../gen/dashboardv1/capital_pools_pb";
import type { CapitalMapTier } from "../gen/dashboardv1/ops_pb";
import type { ManualPosition } from "../gen/dashboardv1/manual_position_pb";
import { ts } from "../components/fmt";
import { StatTile } from "../components/StatTile";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";

function fmtAmt(v: number): string {
  return v.toLocaleString("en-US", { maximumFractionDigits: 2 });
}

function PoolTable({ pools }: { pools: CapitalPool[] }) {
  return (
    <table>
      <thead>
        <tr>
          <th>池</th><th>类型</th><th>权益</th><th>闲置</th><th>在跑</th>
          <th>在跑明细</th><th>状态</th><th>快照时点</th>
        </tr>
      </thead>
      <tbody>
        {pools.map((p) => (
          <tr key={p.venue + ":" + p.venueType} style={p.stale || p.ghost ? { opacity: 0.45 } : undefined}>
            <td>{p.venue}</td>
            <td>{p.venueType}</td>
            <td>{fmtAmt(p.equity)}
              {p.ownershipShare > 0 && p.ownershipShare < 1 &&
                <span className="warn" title={`代管 ${fmtAmt(p.custodialEquity)} 不进判据`}>
                  {` 归属${(p.ownershipShare * 100).toFixed(1)}%·代管${fmtAmt(p.custodialEquity)}`}
                </span>}
            </td>
            <td>{fmtAmt(p.idle)}</td>
            <td>{p.deployed > 0 ? <a href="#/dci">{fmtAmt(p.deployed)}</a> : fmtAmt(p.deployed)}</td>
            <td>{p.deployedParts.map((d) => `${d.ref} ${fmtAmt(d.amount)}`).join("；") || "—"}</td>
            <td>
              {p.ghost ? "幽灵" : p.stale ? "陈旧" : p.online ? "在线" : "—"}
            </td>
            <td>{ts(p.updatedAt)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function PoolCards({ pools }: { pools: CapitalPool[] }) {
  return (
    <ul className="opp-cards opp-grid">
      {pools.map((p) => (
        <li key={p.venue + ":" + p.venueType} className="opp-card"
          style={p.stale || p.ghost ? { opacity: 0.45 } : undefined}>
          <div className="opp-card-head">
            <span className="opp-card-kind">{p.venue}</span>
            <Chip tone={p.ghost ? "warn" : p.stale ? "critical" : "good"}>
              {p.ghost ? "幽灵" : p.stale ? "陈旧" : p.online ? "在线" : "—"}
            </Chip>
            <span className="opp-card-who">{p.venueType}</span>
          </div>
          <div className="opp-card-stats stats">
            <StatTile label="权益" value={fmtAmt(p.equity)} />
            <StatTile label="闲置" value={fmtAmt(p.idle)} />
            <StatTile label="在跑" value={fmtAmt(p.deployed)} />
          </div>
          <div className="opp-card-narrative">
            {p.ownershipShare > 0 && p.ownershipShare < 1 && (
              <span className="warn" title={`代管 ${fmtAmt(p.custodialEquity)} 不进判据`}>
                {`归属${(p.ownershipShare * 100).toFixed(1)}%·代管${fmtAmt(p.custodialEquity)}`}{" "}
              </span>
            )}
            {p.deployed > 0 && <a href="#/dci">在跑明细↗ </a>}
            {p.deployedParts.map((d) => `${d.ref} ${fmtAmt(d.amount)}`).join("；")}
            {p.deployedParts.length === 0 && "—"}
            {" · "}{ts(p.updatedAt)}
          </div>
        </li>
      ))}
    </ul>
  );
}

export default function CapitalPage() {
  const [groups, setGroups] = useState<CapitalPoolsGroup[]>([]);
  const [computedAt, setComputedAt] = useState("");
  const [stalePools, setStalePools] = useState<string[]>([]);
  const [manual, setManual] = useState<ManualPosition[]>([]);
  const [err, setErr] = useState("");
  // D-512 录入面（qty/价/注；id 空=新行）
  const [form, setForm] = useState({ symbol: "", qty: "", price: "", venueLabel: "", note: "" });
  const [busy, setBusy] = useState(false);
  const [modal, setModal] = useState<"" | "ghosts" | "others">("");
  const [tiers, setTiers] = useState<CapitalMapTier[]>([]);

  async function load() {
    try {
      const [r, cm] = await Promise.all([
        client.getCapitalPools({}),
        client.getCapitalMap({}),
      ]);
      setTiers(cm.tiers);
      setGroups(r.groups);
      setComputedAt(ts(r.computedAt));
      setStalePools(r.stalePools);
      setManual(r.manualPositions);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  }
  useEffect(() => { void load(); }, []);

  async function addManual() {
    setBusy(true);
    try {
      const r = await client.upsertManualPosition({
        venueLabel: form.venueLabel || "manual",
        symbol: form.symbol.trim(),
        qty: Number(form.qty) || 0,
        priceUsd: form.price ? Number(form.price) : undefined,
        note: form.note, enabled: true,
      });
      if (!r.ok) { setErr(r.error); return; }
      setForm({ symbol: "", qty: "", price: "", venueLabel: "", note: "" });
      await load();
    } catch (e) { setErr(errText(e)); } finally { setBusy(false); }
  }
  async function delManual(id: number) {
    setBusy(true);
    try {
      await client.deleteManualPosition({ id: BigInt(id) });
      await load();
    } catch (e) { setErr(errText(e)); } finally { setBusy(false); }
  }

  return (
    <main>
      <h1>资金地图 <small style={{ fontSize: 12, color: "#777" }}>算于 {computedAt} · 各币种原值零换算</small></h1>
      {err && <div className="err">{err}</div>}
      {stalePools.length > 0 && (
        <div className="warn">分母缺池（陈旧快照剔计）：{stalePools.join("、")}——合计为实有鲜池口径</div>
      )}
      {/* 主币组照常渲染；非主币折叠——业主令 10-13 小币种口径（计件不计值）。 */}
      {groups.filter((g) => g.currency === "USD" || g.currency === "USDT").map((g) => {
        const live = g.pools.filter((p) => !p.ghost && (p.equity !== 0 || p.idle !== 0 || p.deployed !== 0));
        const ghosts = g.pools.filter((p) => p.ghost);
        return (
          <section key={g.currency}>
            <h2>{g.currency || "—"}</h2>
            <div className="stats">
              <StatTile label="权益合计" value={fmtAmt(g.equity)} />
              <StatTile label="闲置" value={fmtAmt(g.idle)} />
              <StatTile label="在跑" value={fmtAmt(g.deployed)} />
            </div>
            {live.length > 0 ? <PoolCards pools={live} /> : <EmptyState>零活池</EmptyState>}
            {ghosts.length > 0 && (
              <>
                <div onClick={() => setModal("ghosts")} style={{ cursor: "pointer" }}>
                  幽灵池（账户已移除残行，不计合计）×{ghosts.length} <span className="muted">▸</span>
                </div>
                <Modal open={modal === "ghosts"} onClose={() => setModal("")} title="幽灵池（不计合计）">
                  <PoolTable pools={ghosts} />
                </Modal>
              </>
            )}
          </section>
        );
      })}
      {(() => {
        // 业主令 10-13：非主币池折叠单行计件不计值（零换算口径不可求和）。
        const others = groups
          .filter((g) => g.currency !== "USD" && g.currency !== "USDT")
          .flatMap((g) =>
            g.pools.filter((p) => !p.ghost && (p.equity !== 0 || p.idle !== 0 || p.deployed !== 0))
          );
        if (others.length === 0) return null;
        return (
          <>
            <div onClick={() => setModal("others")} style={{ cursor: "pointer" }}>
              其他小币种 ×{others.length}（计件不计值——零换算口径不可求和） <span className="muted">▸</span>
            </div>
            <Modal open={modal === "others"} onClose={() => setModal("")}
              title="其他小币种（计件不计值）">
            <table>
              <thead><tr><th>池</th><th>类型</th><th>权益（原币）</th><th>状态</th><th>快照时点</th></tr></thead>
              <tbody>
                {others.map((p) => (
                  <tr key={p.venue + ":" + p.venueType + ":" + p.currency}
                    style={p.stale ? { opacity: 0.45 } : undefined}>
                    <td>{p.venue}</td>
                    <td>{p.venueType}</td>
                    <td>{fmtAmt(p.equity)} {p.currency}</td>
                    <td>{p.stale ? "陈旧" : p.online ? "在线" : "—"}</td>
                    <td>{ts(p.updatedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            </Modal>
          </>
        );
      })()}
      {groups.length === 0 && !err && <EmptyState>无资金池快照</EmptyState>}

      {/* 画骨④：资金地图分层区（arb CapitalCoverageCard/GetCapitalMap 借形——
          立即可用/可转可用/锁定慢动三档后端分层直渲，节点原币+USD 折算） */}
      {tiers.length > 0 && (
        <section>
          <h2>分层可动性</h2>
          {tiers.map((t) => (
            <div key={t.tier} style={{ marginBottom: 10 }}>
              <h3 style={{ fontSize: 13 }}>
                {t.tier}
                {t.hasUsd && <span className="muted"> ≈${fmtAmt(t.totalUsd)}</span>}
                {t.note && <span className="muted" style={{ fontWeight: "normal" }}> {t.note}</span>}
              </h3>
              {t.nodes.length > 0 ? (
                <ul className="opp-cards opp-grid">
                  {t.nodes.map((n) => (
                    <li key={n.label} className="opp-card" style={{ cursor: "default", padding: "6px 10px" }}>
                      <div className="opp-card-head">
                        <span className="opp-card-kind">{n.label}</span>
                        <span className="opp-card-who">
                          {fmtAmt(n.amount)} {n.currency}
                          {n.hasUsd ? ` ≈$${fmtAmt(n.amountUsd)}` : " —"}
                        </span>
                      </div>
                      <div className="opp-card-narrative muted">{n.source}</div>
                    </li>
                  ))}
                </ul>
              ) : (
                <EmptyState>零节点</EmptyState>
              )}
            </div>
          ))}
        </section>
      )}

      {/* D-512 手工仓位只读段：src=manual——只进全景展示，不进敞口/分母/风控。 */}
      <section>
        <h2>手工仓位 <small style={{ fontSize: 12, color: "#777" }}>只读台账（manual）· 不计敞口不分母</small></h2>
        {manual.length > 0 && (
          <table>
            <thead><tr><th>来源</th><th>品种</th><th>量</th><th>录入价</th><th>注</th><th>更新</th><th></th></tr></thead>
            <tbody>
              {manual.map((m) => (
                <tr key={m.id.toString()}>
                  <td>{m.venueLabel}</td>
                  <td>{m.symbol}</td>
                  <td>{m.qty}</td>
                  <td>{m.priceUsd !== undefined ? fmtAmt(m.priceUsd) : "—"}</td>
                  <td>{m.note}</td>
                  <td>{ts(m.updatedAt)}</td>
                  <td><button disabled={busy} onClick={() => delManual(Number(m.id))}>删</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <div style={{ display: "flex", gap: 8, marginTop: 8, flexWrap: "wrap" }}>
          <input placeholder="品种" value={form.symbol} onChange={(e) => setForm({ ...form, symbol: e.target.value })} />
          <input placeholder="量" value={form.qty} onChange={(e) => setForm({ ...form, qty: e.target.value })} style={{ width: 100 }} />
          <input placeholder="录入价USD(可空)" value={form.price} onChange={(e) => setForm({ ...form, price: e.target.value })} style={{ width: 120 }} />
          <input placeholder="来源标(默认manual)" value={form.venueLabel} onChange={(e) => setForm({ ...form, venueLabel: e.target.value })} style={{ width: 140 }} />
          <input placeholder="注" value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} />
          <button disabled={busy || !form.symbol.trim() || !(Number(form.qty) > 0)} onClick={addManual}>录入</button>
        </div>
      </section>
    </main>
  );
}
