// SettingsCards.tsx —— 设置页两卡（arb SettingsSecretsCard+SettingsPnlResetCard
// 借形）：①凭据管理（listAppSecrets 键视图——has_value/value_source/hot/审计
// note 直渲，值永不回传；update 须 note 必填审计径）②盈亏清零（resetPnlAnchor
// 双确认 armed 钮）。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { AppSecretKey } from "../gen/dashboardv1/app_secrets_pb";
import { Chip } from "./Chip";
import { EmptyState } from "./EmptyState";
import { Modal } from "./Modal";

export function SecretsCard() {
  const [keys, setKeys] = useState<AppSecretKey[]>([]);
  const [edit, setEdit] = useState<AppSecretKey | null>(null);
  const [val, setVal] = useState("");
  const [note, setNote] = useState("");
  const [err, setErr] = useState("");
  const load = async () => {
    try { setKeys((await client.listAppSecrets({})).keys); setErr(""); } catch (e) { setErr(errText(e)); }
  };
  useEffect(() => { void load(); }, []);
  const save = async () => {
    if (!edit || !note.trim()) return;
    try {
      await client.updateAppSecret({ key: edit.key, value: val, note });
      setEdit(null); setVal(""); setNote("");
      await load();
    } catch (e) { setErr(errText(e)); }
  };
  return (
    <section>
      <h2>凭据管理</h2>
      {err && <div className="err">{err}</div>}
      {keys.length > 0 ? (
        <table className="rows">
          <thead><tr><th>键</th><th>有值</th><th>源</th><th>生效</th><th>说明</th><th>注</th><th></th></tr></thead>
          <tbody>
            {keys.map((k) => (
              <tr key={k.key}>
                <td>{k.key}</td>
                <td>{k.hasValue ? "✓" : "—"}</td>
                <td className="muted">{k.valueSource}</td>
                <td><Chip tone={k.hot ? "good" : "warn"}>{k.hot ? "即改即效" : "重启生效"}</Chip></td>
                <td className="muted">{k.desc}</td>
                <td className="muted" style={{ maxWidth: 200 }}>{k.note}</td>
                <td><a href="#" onClick={(e) => { e.preventDefault(); setEdit(k); }}>改/清</a></td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <EmptyState>零凭据键</EmptyState>
      )}
      {edit && (
        <Modal open onClose={() => setEdit(null)} title={`凭据 ${edit.key}`} width={420}>
          <p className="muted" style={{ fontSize: 12 }}>{edit.desc}（值永不回传——写入即覆盖/空串=清除）</p>
          <div style={{ display: "grid", gap: 8 }}>
            <input type="password" value={val} onChange={(e) => setVal(e.target.value)} placeholder="新值（空=清除）" />
            <input value={note} onChange={(e) => setNote(e.target.value)} placeholder="审计事由（必填）" />
            <div>
              <button disabled={!note.trim()} onClick={() => void save()}>写入</button>
              <button onClick={() => setEdit(null)}>取消</button>
            </div>
          </div>
        </Modal>
      )}
    </section>
  );
}

export function PnlResetCard() {
  const [armed, setArmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [err, setErr] = useState("");
  const doReset = async () => {
    setBusy(true);
    try { await client.resetPnlAnchor({}); setArmed(false); setDone(true); }
    catch (e) { setErr(errText(e)); }
    finally { setBusy(false); }
  };
  return (
    <section>
      <h2>盈亏统计起点</h2>
      {err && <div className="err">{err}</div>}
      <p className="muted">清零后「累计已实现」只统计清零时点之后的平仓；历史持仓不删。</p>
      {!armed ? (
        <button className="btn-close" disabled={busy} onClick={() => setArmed(true)}>清零累计起点…</button>
      ) : (
        <button className="btn-close armed" disabled={busy} onClick={() => void doReset()}>确认清零？</button>
      )}
      {armed && <button onClick={() => setArmed(false)} style={{ marginLeft: 8 }}>取消</button>}
      {done && <span className="muted" style={{ marginLeft: 8 }}>已清零</span>}
    </section>
  );
}
