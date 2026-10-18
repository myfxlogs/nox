// ProbeProposals.tsx —— 探针提案待审卡（arb PositionsPageProbeProposals 借形，D-176①）。
// ListProbeProposals 读面；批准/拒绝接既有 ApproveProbe/RejectProbe RPC
// （与飞书「批准#N/拒绝#N」口令同路由同语义）。前端零信任：校验全后端，本件只渲染。
// arb 原型 setInterval(30s) 轮询——票面明令改 onCase("dataRefresh") 流接（D-004）。
// 禁浏览器默认弹窗：decision_ref/reason 走 Modal 表单非 window.prompt。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { ProbeProposalCard } from "../gen/dashboardv1/position_pb";
import { Modal } from "./Modal";
import { Chip } from "./Chip";
import { ts } from "./fmt";

const DECIDE_REF = /^D-\d{3,}$/;

export function ProbeProposals({ onChanged }: { onChanged?: () => void }) {
  const [rows, setRows] = useState<ProbeProposalCard[]>([]);
  const [all, setAll] = useState(false);
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  const [busyId, setBusyId] = useState(0);
  const [decide, setDecide] = useState<{ id: number; verb: "approve" | "reject" } | null>(null);
  const [arg, setArg] = useState("");

  const load = useCallback(async (includeAll: boolean) => {
    try {
      const r = await client.listProbeProposals({ includeDecided: includeAll });
      setRows(r.proposals);
      setErr("");
    } catch (e) { setErr(errText(e)); }
  }, []);

  useEffect(() => {
    void load(all);
    return onCase("dataRefresh", () => void load(all));
  }, [load, all]);

  if (rows.length === 0 && !err) return null;

  const submit = async () => {
    if (!decide) return;
    const a = arg.trim();
    if (decide.verb === "approve" && !DECIDE_REF.test(a)) { setErr("决策引用须为 D-xxx 形式"); return; }
    if (decide.verb === "reject" && a === "") { setErr("拒绝理由必填（落审计）"); return; }
    setBusyId(decide.id); setErr(""); setMsg("");
    try {
      const r = decide.verb === "approve"
        ? await client.approveProbe({ proposalId: BigInt(decide.id), decisionRef: a })
        : await client.rejectProbe({ proposalId: BigInt(decide.id), reason: a });
      setMsg(`#${decide.id} → ${r.status}${r.message ? `（${r.message}）` : ""}`);
      setDecide(null); setArg("");
      void load(all); onChanged?.();
    } catch (e) { setErr(errText(e)); } finally { setBusyId(0); }
  };

  return (
    <section>
      <h2>探针提案待审 <a href="#" className="muted" style={{ fontSize: 12 }} onClick={(e) => { e.preventDefault(); setAll(!all); }}>{all ? "仅待审" : "含已裁定"}</a></h2>
      {err && <div className="err">{err}</div>}
      {msg && <div className="muted">{msg}</div>}
      {rows.length === 0 ? <div className="muted" style={{ fontSize: 12 }}>零待审提案</div> : (
        <ul className="opp-cards opp-grid">
          {rows.map((p) => (
            <li key={String(p.proposalId)} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">#{String(p.proposalId)} {p.canonical} {p.side}</span>
                <Chip tone="neutral">{p.venueKey}</Chip>
              </div>
              <div className="opp-card-narrative">
                数量 {p.qty} · 名义 ${p.notionalUsd.toFixed(0)} · 预估成本 ${p.expectedCostUsd.toFixed(2)}
              </div>
              <div className="opp-card-narrative">
                目的：{p.purpose || "—"} · 证伪：{p.falsify || "—"}
              </div>
              {p.basis && <div className="opp-card-narrative muted">依据：{p.basis}</div>}
              {p.gateNote && <div className="opp-card-narrative muted">机审：{p.gateNote}</div>}
              <div className="opp-card-narrative muted" style={{ display: "flex", gap: 10 }}>
                <span>提案 {ts(p.proposedAt)}</span>
                <span>自动平仓 {ts(p.autoCloseAt)}</span>
                <a href="#" onClick={(e) => { e.preventDefault(); setDecide({ id: Number(p.proposalId), verb: "approve" }); setArg("D-"); }}>批准</a>
                <a href="#" onClick={(e) => { e.preventDefault(); setDecide({ id: Number(p.proposalId), verb: "reject" }); setArg(""); }}>拒绝</a>
              </div>
            </li>
          ))}
        </ul>
      )}
      <Modal open={decide != null} onClose={() => setDecide(null)} width={420}
        title={decide?.verb === "approve" ? `批准提案 #${decide.id}` : `拒绝提案 #${decide?.id}`}>
        <div style={{ display: "grid", gap: 8 }}>
          <label>{decide?.verb === "approve" ? "决策引用（D-xxx，落审计）" : "拒绝理由（落审计）"}
            <input autoFocus value={arg} onChange={(e) => setArg(e.target.value)}
              placeholder={decide?.verb === "approve" ? "D-048" : "理由"} />
          </label>
          <div>
            <button disabled={busyId !== 0} onClick={() => void submit()}>{busyId !== 0 ? "…" : "提交"}</button>{" "}
            <button onClick={() => setDecide(null)}>取消</button>
          </div>
        </div>
      </Modal>
    </section>
  );
}
