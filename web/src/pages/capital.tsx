// capital.tsx —— 资金地图（业主令 10-13 + D-285）：后端聚合池级 权益/闲置/在跑，
// 按币种分组各报各的零换算。纯渲染零运算；幽灵池独立段，陈旧行置灰标时点。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { CapitalPoolsGroup, CapitalPool } from "../gen/dashboardv1/capital_pools_pb";
import { ts } from "../components/fmt";

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
            <td>{fmtAmt(p.equity)}</td>
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

export default function CapitalPage() {
  const [groups, setGroups] = useState<CapitalPoolsGroup[]>([]);
  const [computedAt, setComputedAt] = useState("");
  const [err, setErr] = useState("");

  useEffect(() => {
    (async () => {
      try {
        const r = await client.getCapitalPools({});
        setGroups(r.groups);
        setComputedAt(ts(r.computedAt));
        setErr("");
      } catch (e) {
        setErr(errText(e));
      }
    })();
  }, []);

  return (
    <main>
      <h2>资金地图 <small style={{ fontSize: 12, color: "#777" }}>算于 {computedAt} · 各币种原值零换算</small></h2>
      {err && <div className="err">{err}</div>}
      {groups.map((g) => {
        const live = g.pools.filter((p) => !p.ghost);
        const ghosts = g.pools.filter((p) => p.ghost);
        return (
          <section key={g.currency}>
            <div className="cards">
              <div className="card"><div className="k">币种</div><div className="v">{g.currency || "—"}</div></div>
              <div className="card"><div className="k">权益合计</div><div className="v">{fmtAmt(g.equity)}</div></div>
              <div className="card"><div className="k">闲置</div><div className="v">{fmtAmt(g.idle)}</div></div>
              <div className="card"><div className="k">在跑</div><div className="v">{fmtAmt(g.deployed)}</div></div>
            </div>
            <PoolTable pools={live} />
            {ghosts.length > 0 && (
              <details>
                <summary>幽灵池（账户已移除残行，不计合计）×{ghosts.length}</summary>
                <PoolTable pools={ghosts} />
              </details>
            )}
          </section>
        );
      })}
      {groups.length === 0 && !err && <div>无资金池快照</div>}
    </main>
  );
}
