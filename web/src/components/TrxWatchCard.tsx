// TrxWatchCard.tsx —— TRX 监控提醒卡（arb PriceAlertCard 借形，watch_rule 域全法）：
// 卡头「N 条+新增/取消」+新增/编辑共用表单行（level 三选+LEVEL_HINT 悬停/
// message≤200/fire_mode once·repeat/venue_key ''=全源）+.rows 六列
// （标的·条件|级别 LevelChip|文案截断|最近触发|启用 toggle|编辑·删除 delArm
// 二次确认）+source_matched=false→⚠ 行情源未覆盖+footer「变更即生效
// （事件驱动热载）」=kickConfig 实证口径。校验后端权威前端零信任。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { WatchRule } from "../gen/dashboardv1/watch_rule_pb";
import { LevelChip } from "./Chip";
import { EmptyState } from "./EmptyState";

const LEVEL_HINT: Record<string, string> = {
  info: "仅展示不推飞书",
  warn: "推飞书（常规）",
  critical: "推飞书（紧急）",
};
const OP_CN: Record<string, string> = { lt: "<", le: "≤", gt: ">", ge: "≥" };
const EMPTY_FORM = { subject: "TRXUSDT", op: "lt", threshold: "", level: "warn", message: "", note: "", fire_mode: "repeat", venue_key: "" };

export function TrxWatchCard() {
  const [rules, setRules] = useState<WatchRule[]>([]);
  const [err, setErr] = useState("");
  const [showForm, setShowForm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [editId, setEditId] = useState<bigint | null>(null); // 非 null=编辑回填态
  const [delArm, setDelArm] = useState<bigint | null>(null); // 二次确认待删 id
  const [f, setF] = useState(EMPTY_FORM);

  const load = useCallback(async () => {
    try {
      const r = await client.listWatchRules({});
      // TRX 域=subject 前缀「TRX」（旧件 subject==="TRX" 与表单默认 TRXUSDT
      // 互斥=新建永不显示 bug——prefix 过滤同修）。
      setRules(r.rules.filter((x) => x.subject.startsWith("TRX")));
      setErr("");
    } catch (e) { setErr(errText(e)); }
  }, []);
  useEffect(() => {
    void load();
    return onCase("dataRefresh", (v) => {
      const t = (v as { topic: string }).topic;
      if (t === "trx" || t === "watch_rules") void load();
    });
  }, [load]);

  const save = async () => {
    setBusy(true);
    try {
      await client.upsertWatchRule({ rule: {
        id: editId ?? undefined,
        kind: "price",
        subject: f.subject, op: f.op, threshold: Number(f.threshold) || 0,
        level: f.level, message: f.message, note: f.note,
        fireMode: f.fire_mode, venueKey: f.venue_key, enabled: true,
      } });
      setShowForm(false); setEditId(null); setF(EMPTY_FORM);
      await load();
    } catch (e) { setErr(errText(e)); } finally { setBusy(false); }
  };
  const del = async (id: bigint) => {
    if (delArm !== id) { setDelArm(id); return; } // 二次确认（delArm 态）
    try { await client.deleteWatchRule({ id }); setDelArm(null); await load(); }
    catch (e) { setErr(errText(e)); }
  };
  const edit = (r: WatchRule) => {
    setEditId(r.id);
    setF({ subject: r.subject, op: r.op, threshold: String(r.threshold),
      level: r.level || "warn", message: r.message, note: r.note,
      fire_mode: r.fireMode, venue_key: r.venueKey });
    setShowForm(true);
  };

  return (
    <section className="card">
      <div className="card-head">
        <h2>监控提醒 <span className="muted">（{rules.length} 条）</span></h2>
        <button style={{ marginLeft: "auto" }}
          onClick={() => { setShowForm(!showForm); setEditId(null); setF(EMPTY_FORM); }}>
          {showForm ? "取消" : "新增"}
        </button>
      </div>
      {err && <div className="err">{err}</div>}

      {showForm && (
        <div style={{ display: "grid", gridTemplateColumns: "1fr 0.5fr 0.8fr 0.8fr", gap: 8, padding: "8px 0", borderTop: "1px solid var(--line)" }}>
          <input value={f.subject} onChange={(e) => setF({ ...f, subject: e.target.value })} placeholder="标的 canonical（TRXUSDT）" />
          <select value={f.op} onChange={(e) => setF({ ...f, op: e.target.value })}>
            {Object.keys(OP_CN).map((o) => <option key={o} value={o}>{OP_CN[o]}（{o}）</option>)}
          </select>
          <input type="number" step="any" value={f.threshold} onChange={(e) => setF({ ...f, threshold: e.target.value })} placeholder="阈值（mid 价）" />
          <select value={f.level} onChange={(e) => setF({ ...f, level: e.target.value })} title={LEVEL_HINT[f.level]}>
            {["info", "warn", "critical"].map((l) => <option key={l} value={l} title={LEVEL_HINT[l]}>{l}</option>)}
          </select>
          <input style={{ gridColumn: "1 / 3" }} type="text" maxLength={200} value={f.message}
            onChange={(e) => setF({ ...f, message: e.target.value })} placeholder="告警文案（≤200 字符，空=默认格式串）" />
          <input value={f.note} onChange={(e) => setF({ ...f, note: e.target.value })} placeholder="备注" />
          <div>
            <select value={f.fire_mode} onChange={(e) => setF({ ...f, fire_mode: e.target.value })}>
              <option value="once">once（触发一次即停用）</option>
              <option value="repeat">repeat（冷却重触）</option>
            </select>{" "}
            <button disabled={busy} onClick={() => void save()}>{editId ? "保存" : "新增"}</button>
          </div>
        </div>
      )}

      {rules.length > 0 ? (
        <div className="table-scroll">
          <table className="rows">
            <thead>
              <tr><th>标的·条件</th><th>级别</th><th>文案</th><th>最近触发</th><th>启用</th><th>操作</th></tr>
            </thead>
            <tbody>
              {rules.map((r) => (
                <tr key={Number(r.id)}>
                  <td>
                    {r.subject} {OP_CN[r.op] ?? r.op} {r.threshold}
                    {r.sourceMatched === false && (
                      <span className="price-alert-unmatched" title="该 subject 不在行情源已订阅集合，规则可能永不触发"> ⚠ 源未配</span>
                    )}
                  </td>
                  <td title={LEVEL_HINT[r.level]}><LevelChip level={r.level || "warn"} /></td>
                  <td className="muted" title={r.message}>{r.message ? (r.message.length > 40 ? r.message.slice(0, 40) + "…" : r.message) : "—"}</td>
                  <td>{r.lastFiredTs > 0n ? new Date(Number(r.lastFiredTs) * 1000).toISOString().slice(5, 16).replace("T", " ") : "从未"}</td>
                  <td>
                    <a href="#" onClick={(e) => { e.preventDefault(); void client.upsertWatchRule({ rule: { ...r, enabled: !r.enabled } }).then(load).catch((e2) => setErr(errText(e2))); }}>
                      {r.enabled ? "开" : "关"}
                    </a>
                  </td>
                  <td>
                    <a href="#" onClick={(e) => { e.preventDefault(); edit(r); }}>编辑</a>{" "}
                    <a href="#" style={{ color: delArm === r.id ? "var(--critical)" : undefined }}
                      onClick={(e) => { e.preventDefault(); void del(r.id); }}>
                      {delArm === r.id ? "确认删除？" : "删除"}
                    </a>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState>零 TRX 域规则——新增即接现成盯盘引擎</EmptyState>
      )}
      <div className="muted" style={{ fontSize: "0.75em", marginTop: 8, borderTop: "1px solid var(--line)", paddingTop: 8 }}>
        变更即生效（事件驱动热载——盯盘引擎 QuoteBus tick 即判，once 触发自翻 disabled，repeat 走冷却）。
      </div>
    </section>
  );
}
