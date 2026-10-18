// knowledge.tsx —— 经验库页（arb KnowledgePage 借形）：复盘事实（统计回填
// 条目 status/verdict 后端字直渲）+踩坑库（KB 文件件 listKBEntries/getKBEntry
// 详情浮层）。D 修复语义保留：只读展示零人工复核表单。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { KnowledgeEntry, KBEntry } from "../gen/dashboardv1/account_pb";
import { StatTile } from "../components/StatTile";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";
import { Modal } from "../components/Modal";
import { ts } from "../components/fmt";

const VERDICT_CN: Record<string, string> = {
  spot_on: "符合预期", better_than_expected: "优于预期",
  worse_than_expected: "差于预期", tuning: "调参建议",
};

function KnowledgeTable({ rows }: { rows: KnowledgeEntry[] }) {
  return (
    <table className="rows">
      <thead><tr><th>时点</th><th>签名</th><th>venue/品种</th><th>verdict</th><th>rationale</th><th>状态</th></tr></thead>
      <tbody>
        {rows.map((e) => (
          <tr key={String(e.id)}>
            <td className="num">{ts(e.ts)}</td>
            <td className="muted">{e.signature}</td>
            <td>{e.venue}/{e.symbol}</td>
            <td>{VERDICT_CN[e.verdict] ?? e.verdict}</td>
            <td className="muted" style={{ maxWidth: 320 }}>{e.rationale}</td>
            <td><Chip tone={e.status === "active" ? "good" : e.status === "retracted" ? "critical" : "neutral"}>{e.status}</Chip></td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export default function KnowledgePage() {
  const [entries, setEntries] = useState<KnowledgeEntry[]>([]);
  const [pits, setPits] = useState<KBEntry[]>([]);
  const [pitBody, setPitBody] = useState<{ id: string; body: string } | null>(null);
  const [err, setErr] = useState("");
  const load = async () => {
    try {
      const [k, b] = await Promise.all([client.listKnowledgeEntries({}), client.listKBEntries({})]);
      setEntries(k.entries); setPits(b.entries); setErr("");
    } catch (e) { setErr(errText(e)); }
  };
  useEffect(() => {
    void load();
    return onCase("dataRefresh", () => void load());
  }, []);
  const openPit = async (id: string) => {
    try { const r = await client.getKBEntry({ id }); setPitBody({ id, body: r.content }); }
    catch (e) { setErr(errText(e)); }
  };
  return (
    <div className="page">
      <h1>经验库</h1>
      {err && <div className="err">{err}</div>}
      <div className="stats">
        <StatTile label="复盘事实" value={String(entries.length)} />
        <StatTile label="活跃" value={String(entries.filter((e) => e.status === "active").length)} />
        <StatTile label="踩坑条目" value={String(pits.length)} />
      </div>
      <section>
        <h2>复盘事实（机器产出）</h2>
        <p className="muted">统计复盘自动回填（source=review）；status 流转由 review 引擎驱动</p>
        {entries.length > 0 ? <KnowledgeTable rows={entries} /> : <EmptyState>暂无经验条目（平仓后自动产生）</EmptyState>}
      </section>
      <section>
        <h2>踩坑库（agent 经验）</h2>
        {pits.length > 0 ? (
          <table className="rows">
            <thead><tr><th>id</th><th>kind</th><th>关键词</th><th>摘要</th><th>risk</th><th>status</th></tr></thead>
            <tbody>
              {pits.map((p) => (
                <tr key={p.id}>
                  <td><a href="#" onClick={(e) => { e.preventDefault(); void openPit(p.id); }}>{p.id}</a></td>
                  <td>{p.kind}</td>
                  <td className="muted">{p.keywords}</td>
                  <td className="muted" style={{ maxWidth: 380 }}>{p.summary}</td>
                  <td><Chip tone={p.risk === "high" ? "critical" : p.risk === "medium" ? "warn" : "neutral"}>{p.risk}</Chip></td>
                  <td>{p.status}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <EmptyState>踩坑库零条目</EmptyState>
        )}
      </section>
      {pitBody && (
        <Modal open onClose={() => setPitBody(null)} title={`KB ${pitBody.id}`} width={640}>
          <pre style={{ whiteSpace: "pre-wrap", fontSize: 12 }}>{pitBody.body}</pre>
        </Modal>
      )}
    </div>
  );
}
