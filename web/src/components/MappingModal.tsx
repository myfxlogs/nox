import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { Modal } from "./Modal";
import { Chip } from "./Chip";
import type { SymbolMapping, SymbolMappingSuggestion } from "../gen/dashboardv1/broker_admin_pb";

// MappingModal.tsx —— D-090 族：符号映射管理浮层（arb BrokersPageMappingPage
// 借形——从 accounts.tsx 拆出，棘轮线守）。已映射行+建议行+手工加映射+删
// ——全 RPC 直驱零前端聚合。：符号映射管理浮层（arb BrokersPageMappingPage 借形）。
// 已映射行+建议行（auto/conflict）+手工加映射+删——全 RPC 直驱零前端聚合。
export function MappingModal(p: { venue: string; onClose: () => void; onErr: (m: string) => void }) {
  const [maps, setMaps] = useState<SymbolMapping[]>([]);
  const [sugs, setSugs] = useState<SymbolMappingSuggestion[]>([]);
  const [form, setForm] = useState({ brokerSymbol: "", canonical: "" });
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    try {
      const [m, g] = await Promise.all([
        client.listSymbolMappings({ venue: p.venue }),
        client.listMappingSuggestions({ venue: p.venue }),
      ]);
      setMaps(m.mappings);
      setSugs(g.suggestions);
    } catch (e) {
      p.onErr(errText(e));
    }
  }, [p]);
  useEffect(() => { void load(); }, [load]);

  const save = async () => {
    setBusy(true);
    try {
      await client.saveSymbolMapping({ venue: p.venue, brokerSymbol: form.brokerSymbol, canonical: form.canonical });
      setForm({ brokerSymbol: "", canonical: "" });
      await load();
    } catch (e) { p.onErr(errText(e)); } finally { setBusy(false); }
  };
  const del = async (bs: string) => {
    setBusy(true);
    try {
      await client.deleteSymbolMapping({ venue: p.venue, brokerSymbol: bs });
      await load();
    } catch (e) { p.onErr(errText(e)); } finally { setBusy(false); }
  };
  const autoMap = async () => {
    setBusy(true);
    try {
      await client.autoMapBroker({ venue: p.venue });
      await load();
    } catch (e) { p.onErr(errText(e)); } finally { setBusy(false); }
  };
  return (
    <Modal open onClose={p.onClose} title={`${p.venue} 符号映射`} width={680}>
      <div style={{ marginBottom: 8 }}>
        <button disabled={busy} onClick={() => void autoMap()}>自动映射</button>
      </div>
      <h4>已映射（{maps.length}）</h4>
      <ul className="opp-cards" style={{ maxHeight: 200, overflowY: "auto" }}>
        {maps.map((m) => (
          <li key={m.brokerSymbol} className="opp-card" style={{ cursor: "default" }}>
            <div className="opp-card-head">
              <b>{m.brokerSymbol}</b> → <span className="opp-card-who">{m.canonical}</span>
              {m.contractSize !== undefined && <span className="muted"> ×{m.contractSize}</span>}
              <span className="opp-card-friction">
                <a href="#" onClick={(e) => { e.preventDefault(); void del(m.brokerSymbol); }}>删</a>
              </span>
            </div>
          </li>
        ))}
      </ul>
      <h4>建议（{sugs.length}）</h4>
      <ul className="opp-cards" style={{ maxHeight: 160, overflowY: "auto" }}>
        {sugs.map((g) => (
          <li key={g.brokerSymbol} className="opp-card" style={{ cursor: "default" }}>
            <div className="opp-card-head">
              <b>{g.brokerSymbol}</b>
              <Chip tone={g.status === "auto" ? "good" : "warn"}>{g.status}</Chip>
              <span className="opp-card-who">→ {g.canonical || "—"} {g.reason}</span>
              {g.status === "auto" && g.canonical && (
                <span className="opp-card-friction">
                  <a href="#" onClick={(e) => { e.preventDefault(); setForm({ brokerSymbol: g.brokerSymbol, canonical: g.canonical }); }}>采纳</a>
                </span>
              )}
            </div>
          </li>
        ))}
      </ul>
      <div style={{ display: "flex", gap: 6, marginTop: 8 }}>
        <input placeholder="broker 符号" value={form.brokerSymbol} onChange={(e) => setForm({ ...form, brokerSymbol: e.target.value })} />
        <input placeholder="canonical" value={form.canonical} onChange={(e) => setForm({ ...form, canonical: e.target.value })} />
        <button disabled={busy || !form.brokerSymbol || !form.canonical} onClick={() => void save()}>存映射</button>
      </div>
    </Modal>
  );
}
