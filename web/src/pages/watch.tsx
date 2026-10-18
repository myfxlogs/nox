// watch.tsx —— 盯盘规则页（业主视窗两件套 B）：watch_rule CRUD + 启停开关。
// 评估在后端引擎（QuoteBus 事件驱动）；本页纯渲染零运算零轮询——改动后经
// configKick 热载即生效，本页手动操作后重拉一次列表。
// S3′-final 批迁：第一眼=规则活态 opp-card 阵（启停/改/删绑径原样）；
// 编辑表单挪卡内段（新增/改共享）。纯 JSX/CSS 重排零逻辑动。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { WatchRule } from "../gen/dashboardv1/watch_rule_pb";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";

const OPS = ["lt", "le", "gt", "ge"] as const;

export default function WatchPage() {
  const [rules, setRules] = useState<WatchRule[]>([]);
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  // 编辑表单（id=0 新增）
  const [form, setForm] = useState<WatchRule | null>(null);

  const load = async () => {
    const r = await client.listWatchRules({});
    setRules(r.rules);
  };
  useEffect(() => {
    (async () => {
      try {
        await load();
        setErr("");
      } catch (e) {
        setErr(errText(e));
      }
    })();
  }, []);

  const newForm = (): WatchRule =>
    ({
      id: 0n,
      kind: "price",
      subject: "",
      venueKey: "",
      op: "ge",
      threshold: 0,
      fireMode: "once",
      cooldownSec: 0,
      note: "",
      enabled: true,
    }) as unknown as WatchRule;

  const save = async () => {
    if (!form) return;
    try {
      await client.upsertWatchRule({ rule: form });
      setForm(null);
      await load();
      setMsg("已保存（引擎热载生效）");
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  };

  const toggle = async (r: WatchRule) => {
    try {
      await client.upsertWatchRule({ rule: { ...r, enabled: !r.enabled } });
      await load();
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  };

  const del = async (id: bigint) => {
    try {
      await client.deleteWatchRule({ id });
      await load();
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  };

  return (
    <div>
      <h1>盯盘</h1>
      {err && <div className="err">{err}</div>}
      {msg && <div className="muted">{msg}</div>}

      {/* 第一眼=规则活态卡阵；新增入口挂段头 */}
      <h2>
        盯盘规则
        <button style={{ marginLeft: 12 }} onClick={() => setForm(newForm())}>新增</button>
      </h2>
      {rules.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {rules.map((r) => (
            <li key={Number(r.id)} className="opp-card" style={{ opacity: r.enabled ? 1 : 0.45 }}>
              <div className="opp-card-head">
                <span className="opp-card-kind">{r.subject}</span>
                <Chip tone={r.stale ? "critical" : r.enabled ? "good" : "neutral"}>
                  {r.stale ? "stale" : r.enabled ? "启用" : "停用"}
                </Chip>
                <span className="opp-card-who">{r.venueKey || "全源"}</span>
              </div>
              <div className="opp-card-narrative">
                mid {r.op} {r.threshold} · {r.fireMode}
                {r.fireMode === "repeat" ? ` 冷却${r.cooldownSec}s` : ""}
                {r.note ? ` · ${r.note}` : ""}
              </div>
              <div className="opp-card-narrative muted">
                #{Number(r.id)} · 触发 {r.lastFiredTs > 0n ? new Date(Number(r.lastFiredTs) * 1000).toLocaleString() : "—"}
              </div>
              <div className="opp-card-narrative" style={{ display: "flex", gap: 8 }}>
                <a href="#" onClick={(e) => { e.preventDefault(); void toggle(r); }}>
                  {r.enabled ? "停用" : "启用"}
                </a>
                <a href="#" onClick={(e) => { e.preventDefault(); setForm(r); }}>改</a>
                <a href="#" onClick={(e) => { e.preventDefault(); void del(r.id); }}>删</a>
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>无规则——阈值业主自填，系统零预设</EmptyState>
      )}

      {form && (
        <div className="card" style={{ marginTop: 12, padding: 12 }}>
          <h3>{Number(form.id) === 0 ? "新增规则" : `改规则 #${Number(form.id)}`}</h3>
          <table>
            <tbody>
              <tr>
                <td style={{ width: 110 }}>标的</td>
                <td>
                  <input
                    value={form.subject}
                    onChange={(e) => setForm({ ...form, subject: e.target.value })}
                    placeholder="canonical 如 TRXUSDT"
                  />
                </td>
              </tr>
              <tr>
                <td>源</td>
                <td>
                  <input
                    value={form.venueKey}
                    onChange={(e) => setForm({ ...form, venueKey: e.target.value })}
                    placeholder="空=全源；或 venue:type 如 binance:perp"
                  />
                </td>
              </tr>
              <tr>
                <td>条件</td>
                <td>
                  <select value={form.op} onChange={(e) => setForm({ ...form, op: e.target.value })}>
                    {OPS.map((o) => (
                      <option key={o} value={o}>{o}</option>
                    ))}
                  </select>{" "}
                  <input
                    style={{ width: 140 }}
                    type="number"
                    step="any"
                    value={form.threshold}
                    onChange={(e) => setForm({ ...form, threshold: Number(e.target.value) })}
                  />{" "}
                  <span className="muted">mid 价基准</span>
                </td>
              </tr>
              <tr>
                <td>模式</td>
                <td>
                  <select value={form.fireMode} onChange={(e) => setForm({ ...form, fireMode: e.target.value })}>
                    <option value="once">once（触发即退役）</option>
                    <option value="repeat">repeat（冷却防抖）</option>
                  </select>{" "}
                  {form.fireMode === "repeat" && (
                    <>
                      冷却s{" "}
                      <input
                        style={{ width: 90 }}
                        type="number"
                        value={form.cooldownSec}
                        onChange={(e) => setForm({ ...form, cooldownSec: Number(e.target.value) })}
                      />
                    </>
                  )}
                </td>
              </tr>
              <tr>
                <td>备注</td>
                <td>
                  <input
                    style={{ width: 420 }}
                    value={form.note}
                    onChange={(e) => setForm({ ...form, note: e.target.value })}
                  />
                </td>
              </tr>
              <tr>
                <td />
                <td>
                  <button onClick={() => void save()}>保存</button>{" "}
                  <button onClick={() => setForm(null)}>取消</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
