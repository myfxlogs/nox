// CarryGraduationSection.tsx —— carry 毕业建议队列（arb PositionsPageCarryGraduation
// 借形，D-494 裁决面）：pending 行=待授权行动面直展（纸面验证达标→组合白名单
// 转正授权）；decided 行=历史台账。流接=onCase("dataRefresh") topic
// carry_graduation 重取（arb 原型零轮询先例已守）。
// 执法语义=后端字：venue_gaps/flag_warn/coverage_ratio/net_all_in 全字段直渲。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { CarryGraduationRow } from "../gen/dashboardv1/graduation_pb";
import { Chip } from "./Chip";
import { EmptyState } from "./EmptyState";
import { Modal } from "./Modal";
import { fmtUsd } from "./fmt";

export function CarryGraduationSection() {
  const [rows, setRows] = useState<CarryGraduationRow[]>([]);
  const [err, setErr] = useState("");
  const [rej, setRej] = useState<CarryGraduationRow | null>(null);
  const [reason, setReason] = useState("");

  const load = useCallback(async () => {
    try { setRows((await client.listCarryGraduations({})).rows); setErr(""); }
    catch (e) { setErr(errText(e)); }
  }, []);

  useEffect(() => {
    void load();
    return onCase("dataRefresh", (v) => {
      if ((v as { topic: string }).topic === "carry_graduation") void load();
    });
  }, [load]);

  const pending = rows.filter((r) => r.status === "pending");
  const decided = rows.filter((r) => r.status !== "pending");

  const doReject = async () => {
    if (!rej || !reason.trim()) return;
    try {
      await client.rejectCarryGraduation({ graduationId: rej.id, note: reason, writeFlag: true, flagLevel: "warn" });
      setRej(null); setReason("");
      await load();
    } catch (e) { setErr(errText(e)); }
  };

  return (
    <section>
      <h2>毕业建议 <Chip tone="warn">{pending.length} 待授权</Chip></h2>
      {err && <div className="err">{err}</div>}
      {pending.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {pending.map((r) => (
            <li key={String(r.id)} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">{r.venue}/{r.symbol} · {r.structure}</span>
                <Chip tone={r.flagWarn ? "warn" : "good"}>{r.flagWarn || "验证达标"}</Chip>
              </div>
              <div className="opp-card-narrative">
                存续 {r.ageDays.toFixed(1)}d · 实结 {fmtUsd(r.accruedUsd)}
                {r.coverageRatio !== undefined && ` · 覆盖率 ${(r.coverageRatio * 100).toFixed(0)}%`}
                {r.netAllInUsd !== undefined && ` · 全账净额 ${fmtUsd(r.netAllInUsd)}`}
                {r.lastFundingAnn !== undefined && ` · 年化 ${(r.lastFundingAnn * 100).toFixed(1)}%`}
              </div>
              {r.venueGaps.length > 0 && (
                <div className="opp-card-narrative warn">缺口：{r.venueGaps.join("、")}</div>
              )}
              <div className="opp-card-narrative" style={{ display: "flex", gap: 8 }}>
                <a href="#" onClick={(e) => { e.preventDefault(); setRej(r); }}>拒绝</a>
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零待授权毕业项（验证未达标者不入列——后端判据）</EmptyState>
      )}
      {decided.length > 0 && (
        <table className="rows">
          <thead><tr><th>venue/品种</th><th>结构</th><th>裁定</th><th>注</th><th>时刻</th></tr></thead>
          <tbody>
            {decided.map((r) => (
              <tr key={String(r.id)}>
                <td>{r.venue}/{r.symbol}</td><td>{r.structure}</td>
                <td><Chip tone={r.status === "approved" ? "good" : "critical"}>{r.status}</Chip></td>
                <td className="muted">{r.decidedNote}</td>
                <td className="muted">{r.decidedAt.slice(0, 16)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {rej && (
        <Modal open onClose={() => setRej(null)} title={`拒绝毕业 ${rej.venue}/${rej.symbol}`} width={420}>
          <div style={{ display: "grid", gap: 8 }}>
            <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="拒绝理由（落 decided_note+warn 旗标回写，必填）" />
            <div>
              <button disabled={!reason.trim()} onClick={() => void doReject()}>确认拒绝</button>
              <button onClick={() => setRej(null)}>取消</button>
            </div>
          </div>
        </Modal>
      )}
    </section>
  );
}
