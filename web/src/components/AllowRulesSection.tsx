// AllowRulesSection.tsx —— 「账户开关与上限」（arb BrokersPageAllowRules 借形，
// D-557/D-567 合并面）：venue/canonical 闸规则+venue 敞口上限单表。
// 执法语义=后端字（enabled/上限 0=冻结、unset=合成占位未设闸）——前端零判定，
// 行字段直渲；备注=审计必填径（add/delete note 非空闸）。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { AutotradeAllowRuleRow } from "../gen/dashboardv1/ops_pb";
import type { VenueExposureCapRow } from "../gen/dashboardv1/graduation_pb";
import { Chip } from "./Chip";
import { EmptyState } from "./EmptyState";
import { ConfirmDialog } from "./Modal";

export function AllowRulesSection() {
  const [rules, setRules] = useState<AutotradeAllowRuleRow[]>([]);
  const [caps, setCaps] = useState<VenueExposureCapRow[]>([]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [cfm, setCfm] = useState<{ text: string; run: () => void } | null>(null);
  const [capForm, setCapForm] = useState({ venueKey: "", capUsd: "", note: "" });
  const [ruleForm, setRuleForm] = useState({ value: "", note: "" });

  const load = useCallback(async () => {
    try {
      const [r, c] = await Promise.all([
        client.listAutotradeAllowRules({}),
        client.listVenueExposureCaps({}),
      ]);
      setRules(r.rules);
      setCaps(c.rows);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  }, []);

  useEffect(() => {
    void load();
    return onCase("dataRefresh", (v) => {
      const t = (v as { topic: string }).topic;
      if (t === "allow_rules" || t === "funds") void load();
    });
  }, [load]);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      await load();
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section>
      <h2>账户开关与上限</h2>
      {err && <div className="err">{err}</div>}
      {rules.length > 0 || caps.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {rules.map((r) => (
            <li key={Number(r.id)} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">{r.value}</span>
                <Chip tone={r.enabled ? "good" : "critical"}>{r.enabled ? "允许" : "禁止"}</Chip>
                <span className="opp-card-who">{r.kind}{r.simulated ? " · sim" : ""}</span>
              </div>
              <div className="opp-card-narrative">
                {r.note || "—"}
                {r.kind === "venue" && r.balance !== undefined && ` · 余额 $${r.balance.toFixed(0)}`}
              </div>
              <div className="opp-card-narrative" style={{ display: "flex", gap: 8 }}>
                <a href="#" onClick={(e) => { e.preventDefault(); setCfm({
                  text: `${r.enabled ? "禁止" : "允许"} ${r.kind === "venue" ? "账户" : "标的"} ${r.value}？（禁止=阻止新开仓，不影响在管持仓）`,
                  run: () => act(() => client.setAutotradeAllowRuleEnabled({ id: r.id, enabled: !r.enabled })),
                }); }}>{r.enabled ? "禁止" : "允许"}</a>
                <a href="#" onClick={(e) => { e.preventDefault(); setCfm({
                  text: `删规则 ${r.value}？（备注随请求走审计径）`,
                  run: () => act(() => client.deleteAutotradeAllowRule({ id: r.id, note: "ui-删除" })),
                }); }}>删</a>
              </div>
            </li>
          ))}
          {caps.map((c) => (
            <li key={c.venueKey} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">{c.venueKey}</span>
                <Chip tone={c.unset ? "warn" : c.enabled ? "good" : "critical"}>
                  {c.unset ? "未设闸" : c.enabled ? `上限 $${c.capUsd.toFixed(0)}` : "冻结"}
                </Chip>
              </div>
              <div className="opp-card-narrative">
                {c.allocatedUsd !== undefined && `已配 $${c.allocatedUsd.toFixed(0)}`}
                {c.liveAvailableUsd !== undefined && ` · 可用 $${c.liveAvailableUsd.toFixed(0)}${c.liveSource ? `(${c.liveSource})` : ""}`}
                {c.note ? ` · ${c.note}` : ""}
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零闸行——venue 全开（未设闸如实标）</EmptyState>
      )}
      <form className="inline" onSubmit={(e) => { e.preventDefault(); act(() => client.setVenueExposureCap({
        venueKey: capForm.venueKey, capUsd: Number(capForm.capUsd) || 0, enabled: true, note: capForm.note,
      })); }}>
        <input placeholder="venue_key 敞口上限" value={capForm.venueKey} onChange={(e) => setCapForm({ ...capForm, venueKey: e.target.value })} style={{ width: 150 }} />
        <input placeholder="cap USD（0=冻结）" value={capForm.capUsd} onChange={(e) => setCapForm({ ...capForm, capUsd: e.target.value })} style={{ width: 110 }} />
        <input placeholder="备注" value={capForm.note} onChange={(e) => setCapForm({ ...capForm, note: e.target.value })} />
        <button disabled={busy || !capForm.venueKey} type="submit">设上限</button>
      </form>
      <form className="inline" onSubmit={(e) => { e.preventDefault(); act(() => client.addAutotradeAllowRule({
        kind: "canonical", value: ruleForm.value, note: ruleForm.note || "ui-新增",
      })); }}>
        <input placeholder="canonical 白名单（空=不限）" value={ruleForm.value} onChange={(e) => setRuleForm({ ...ruleForm, value: e.target.value })} />
        <input placeholder="审计备注（必填）" value={ruleForm.note} onChange={(e) => setRuleForm({ ...ruleForm, note: e.target.value })} />
        <button disabled={busy || !ruleForm.value || !ruleForm.note} type="submit">加标的闸</button>
      </form>
      <ConfirmDialog open={cfm != null} text={cfm?.text}
        onCancel={() => setCfm(null)}
        onConfirm={() => { const r = cfm?.run; setCfm(null); r?.(); }} />
    </section>
  );
}
