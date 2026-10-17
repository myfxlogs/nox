// graduation.tsx —— 毕业授权段（投资人页挂段）：待批表 + 试算预览 +
// 三种额度方式互斥批准 + 已裁定历史只读。前端零信任：折算/池映射/风险判全后端回显。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type {
  ApproveCarryGraduationResponse,
  CarryGraduationRow,
} from "../gen/dashboardv1/graduation_pb";
import { fmtUsd } from "./fmt";
import { Chip, type ChipTone } from "./Chip";
import { StatTile } from "./StatTile";
import { Modal } from "./Modal";

type Mode = "pct" | "usd" | "explicit";

const STRUCTURE_CN: Record<string, string> = {
  short_cfd_long_perp: "卖CFD·买永续",
  short_perp_long_cfd: "卖永续·买CFD",
  short_perp_long_spot: "卖永续·买现货",
};

const STATUS_CN: Record<string, string> = {
  approved: "已批准",
  rejected: "已拒绝",
  expired: "已过期",
};

function structCN(s: string): string {
  return STRUCTURE_CN[s] || s;
}

function statusCN(s: string): string {
  return STATUS_CN[s] || s;
}

function covBadge(r: CarryGraduationRow): string {
  const c = r.coverageRatio;
  if (c == null) return "—";
  const tag = c < 0.5 ? "（偏低）" : c < 0.8 ? "（一般）" : "";
  return (c * 100).toFixed(0) + "%" + tag;
}

// S2b 卡阵徽标：净额现值正+覆盖度达标=「可批」绿；覆盖度偏低或净额负=「观望」黄
// /「净负」红——显示映射非准入判据（判据全在 GraduationQualifiedReason）。
function gradTone(r: CarryGraduationRow): { tone: ChipTone; cn: string } {
  const net = r.liveNetAllInUsd ?? r.netAllInUsd;
  const cov = r.coverageRatio;
  if (net != null && net < 0) return { tone: "critical", cn: "净负" };
  if (cov != null && cov < 0.5) return { tone: "warn", cn: "覆盖偏低" };
  return { tone: "good", cn: "可批" };
}

export default function GraduationPanel() {
  const [rows, setRows] = useState<CarryGraduationRow[]>([]);
  const [err, setErr] = useState("");
  const [openId, setOpenId] = useState<number | null>(null);
  const [rejId, setRejId] = useState<number | null>(null);
  const [rejNote, setRejNote] = useState("");
  const [mode, setMode] = useState<Mode>("pct");
  const [pct, setPct] = useState("");
  const [usd, setUsd] = useState("");
  const [lq, setLq] = useState("");
  const [sq, setSq] = useState("");
  const [gross, setGross] = useState("");
  const [notional, setNotional] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<ApproveCarryGraduationResponse | null>(null);

  const load = async () => {
    try {
      const r = await client.listCarryGraduations({});
      setRows(r.rows);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  };
  useEffect(() => {
    void load();
  }, []);

  const pending = rows.filter((r) => r.status === "pending");
  // D-578 观察中：unqualified=摘牌待复活（引擎复活回牌语义非终态）；
  // approved/rejected/stale=终态。
  const watching = rows.filter((r) => r.status === "unqualified");
  const decided = rows.filter((r) => r.status !== "pending" && r.status !== "unqualified");

  const approveReq = (id: number, dryRun: boolean) => {
    const req = { graduationId: BigInt(id), dryRun, note };
    if (mode === "pct") return { ...req, fundingPct: parseFloat(pct) / 100 };
    if (mode === "usd") return { ...req, fundingUsd: parseFloat(usd) };
    return {
      ...req,
      maxLongQty: parseFloat(lq),
      maxShortQty: parseFloat(sq),
      maxGrossUsd: parseFloat(gross),
      ...(notional ? { notionalUsd: parseFloat(notional) } : {}),
    };
  };

  const submit = async (id: number, dryRun: boolean) => {
    setBusy(true);
    try {
      const r = await client.approveCarryGraduation(approveReq(id, dryRun));
      if (dryRun) {
        setPreview(r);
      } else {
        if (!r.ok) setErr(r.error || "批准失败");
        setPreview(null);
        setOpenId(null);
        await load();
      }
      if (dryRun && !r.ok) setErr(r.error || "试算失败");
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

  const reject = async (id: number, why: string) => {
    setBusy(true);
    try {
      const r = await client.rejectCarryGraduation({
        graduationId: BigInt(id),
        note: why,
        writeFlag: true,
        flagLevel: "warn",
      });
      if (!r.ok) setErr(r.error || "拒绝失败");
      await load();
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <h2>毕业授权{pending.length > 0 ? `（${pending.length} 条待批）` : ""}</h2>
      {err && <div className="err">{err}</div>}
      {pending.length === 0 && decided.length === 0 && <div className="muted">暂无毕业提案</div>}
      {pending.length > 0 && (
        /* S2b arb 式全候选卡阵（Opportunity.tsx 范式）：徽标/StatTile 阵/
           narrative 三区，卡顶=裁决徽标+品种+结构+场所对 */
        <ul className="opp-cards scroll-cap">
          {pending.map((r) => {
            const t = gradTone(r);
            const net = r.liveNetAllInUsd ?? r.netAllInUsd;
            const ann = r.lastFundingAnn;
            return (
              <li key={Number(r.id)} className="opp-card">
                <div className="opp-card-head">
                  <Chip tone={t.tone}>{t.cn}</Chip>
                  <span className="opp-card-kind">#{Number(r.id)} {r.symbol}</span>
                  <span className="opp-card-who">{structCN(r.structure)} · {r.venue}</span>
                  {r.flagWarn ? <Chip tone="warn">⚠{r.flagWarn}</Chip> : null}
                  {/* 坑签名命中（D-587④ 续段②）：后端匹配回显中文标签，零命中不造标 */}
                  {(r.pitHits ?? []).map((h) => <Chip key={h} tone="warn">{h}</Chip>)}
                  <span className="opp-card-friction">{r.ageDays.toFixed(1)}天</span>
                </div>
                <div className="stats opp-card-stats">
                  <StatTile label="已计息" value={fmtUsd(r.accruedUsd)} />
                  <StatTile label="收敛浮盈（现值）" value={r.liveConvergenceUsd != null ? fmtUsd(r.liveConvergenceUsd) : "—"} />
                  <StatTile label="费估" value={r.estFeeUsd != null ? fmtUsd(-r.estFeeUsd) : "—"} />
                  <StatTile label="全费净额" value={net != null ? fmtUsd(net) : "—"}
                    sub={r.netAllInUsd != null ? `提案 ${fmtUsd(r.netAllInUsd)}` : undefined} />
                  {/* 8% 线=毕业存活闸语义（last_funding_ann≥8 后端判据），
                      前端只标过线与否不裁准入 */}
                  <StatTile label="资金费年化" value={ann != null ? ann.toFixed(2) + "%" : "—"}
                    sub={ann != null ? (ann >= 8 ? "≥8% 存活线" : "<8% 存活线下") : undefined} />
                  <StatTile label="覆盖度" value={covBadge(r)} />
                </div>
                <p className="opp-card-narrative muted">
                  {r.decidedNote || "纸面沙箱达标待业主授权"}
                </p>
                <div>
                  <button onClick={() => { setOpenId(openId === Number(r.id) ? null : Number(r.id)); setPreview(null); }}>
                    批准
                  </button>{" "}
                  <button disabled={busy} onClick={() => { setRejId(Number(r.id)); setRejNote(""); }}>拒绝</button>
                </div>
              </li>
            );
          })}
        </ul>
      )}

      {openId != null && (
        <Modal open={openId != null} onClose={() => setOpenId(null)} title={<>批准 #{openId} —— 三种额度方式任选其一，先试算再批准</>} width={760}>
          <div style={{ margin: "8px 0" }}>
            {([["pct", "按可用资金占比 %"], ["usd", "按固定金额 USD"], ["explicit", "显式四字段（数量/额度上限）"]] as [Mode, string][]).map(([m, label]) => (
              <label key={m} style={{ marginRight: "12px" }}>
                <input type="radio" checked={mode === m} onChange={() => { setMode(m); setPreview(null); }} /> {label}
              </label>
            ))}
          </div>
          {mode === "pct" && (
            <input placeholder="可用资金占比 %（如 5 表示 5%）" value={pct} onChange={(e) => setPct(e.target.value)} />
          )}
          {mode === "usd" && (
            <input placeholder="分配金额 USD（如 200）" value={usd} onChange={(e) => setUsd(e.target.value)} />
          )}
          {mode === "explicit" && (
            <div>
              <input placeholder="买腿数量上限" value={lq} onChange={(e) => setLq(e.target.value)} />{" "}
              <input placeholder="卖腿数量上限" value={sq} onChange={(e) => setSq(e.target.value)} />{" "}
              <input placeholder="双腿总敞口上限 USD" value={gross} onChange={(e) => setGross(e.target.value)} />{" "}
              <input placeholder="名义金额 USD（可留空）" value={notional} onChange={(e) => setNotional(e.target.value)} />
            </div>
          )}
          <input placeholder="备注（可留空）" value={note} onChange={(e) => setNote(e.target.value)}
            style={{ marginTop: "6px", width: "60%" }} />
          <div style={{ marginTop: "8px" }}>
            <button disabled={busy} onClick={() => submit(openId, true)}>试算预览</button>{" "}
            <button disabled={busy || preview == null || !preview.ok} onClick={() => submit(openId, false)}>
              确认批准
            </button>
          </div>

          {preview && (
            <div className="muted" style={{ marginTop: "8px", fontSize: "12px" }}>
              {!preview.ok && <div className="err">{preview.error}</div>}
              {preview.ok && (
                <>
                  <div>预估总敞口={fmtUsd(preview.estGrossUsd)} ｜ 单腿名义={fmtUsd(preview.estLegNotionalUsd)}
                    ｜ 双腿数量 买={preview.estLongQty.toFixed(4)} 卖={preview.estShortQty.toFixed(4)}
                    ｜ 实际分配={fmtUsd(preview.estAllocUsd)}（{(preview.estAllocPct * 100).toFixed(2)}%）</div>
                  <div>可用基准 {fmtUsd(preview.allocBaseUsd)}{preview.allocBaseNote ? `（${preview.allocBaseNote}）` : ""}
                    ｜ 风险等级={preview.riskLevel}{preview.suggestedAllocUsd > 0 ? ` 建议额度 ${fmtUsd(preview.suggestedAllocUsd)}` : ""}</div>
                  {preview.riskReasons.map((x, i) => <div key={i} className="warn">⚠ {x}</div>)}
                  {preview.legPools.map((lp) => (
                    <div key={lp.venueKey}>
                      {lp.venueKey}: {lp.found ? `${fmtUsd(lp.equity)} ${lp.currency}@${lp.poolVenue}:${lp.poolVenueType}${lp.isMin ? "（约束池）" : ""} 来源=${lp.source}` : "未采集"}
                    </div>
                  ))}
                  {preview.venueGaps.length > 0 && <div className="warn">场所缺口: {preview.venueGaps.join("；")}</div>}
                  {preview.sizingNotes.map((x, i) => <div key={i}>· {x}</div>)}
                </>
              )}
            </div>
          )}
        </Modal>
      )}

      <Modal open={rejId != null} onClose={() => setRejId(null)} title={<>拒绝 #{rejId}</>} width={440}>
        <input placeholder="拒绝理由（将记入旗标 warn，可留空）" value={rejNote}
          onChange={(e) => setRejNote(e.target.value)} style={{ width: "100%" }} />
        <div style={{ marginTop: "10px", display: "flex", gap: 8, justifyContent: "flex-end" }}>
          <button onClick={() => setRejId(null)}>取消</button>
          <button className="danger" disabled={busy}
            onClick={() => { const id = rejId; const w = rejNote; setRejId(null); if (id != null) reject(id, w); }}>
            确认拒绝
          </button>
        </div>
      </Modal>

      {watching.length > 0 && (
        <>
          <h3 className="muted">观察中（摘牌待复活——引擎复活自动回牌）</h3>
          <table>
            <thead><tr><th>#</th><th>品种</th><th>场所对</th><th>存活率</th><th>现值净额</th><th>摘牌因</th></tr></thead>
            <tbody>
              {watching.map((r) => (
                <tr key={Number(r.id)}>
                  <td>{Number(r.id)}</td><td>{r.symbol}</td><td>{r.venue}</td>
                  <td className={(r.lastFundingAnn ?? 0) >= 8 ? "good" : "crit"}>
                    {r.lastFundingAnn != null ? r.lastFundingAnn.toFixed(2) + "%" : "—"}</td>
                  <td className={(r.liveNetAllInUsd ?? 0) >= 0 ? "good" : "crit"}>
                    {r.liveNetAllInUsd != null ? fmtUsd(r.liveNetAllInUsd) : "—"}</td>
                  <td className="muted">{r.decidedNote}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
      {decided.length > 0 && (
        <>
          <h3 className="muted">已裁定（只读）</h3>
          <table>
            <thead><tr><th>#</th><th>品种</th><th>场所对</th><th>状态</th><th>裁定方</th><th>组合</th><th>备注</th></tr></thead>
            <tbody>
              {decided.map((r) => (
                <tr key={Number(r.id)}>
                  <td>{Number(r.id)}</td><td>{r.symbol}</td><td>{r.venue}</td>
                  <td><Chip tone={r.status==="approved"?"good":r.status==="rejected"?"critical":"neutral"}>{statusCN(r.status)}</Chip></td><td>{r.decidedBy || "委员会"}</td>
                  <td>{r.comboId != null ? "#" + Number(r.comboId) : "—"}</td>
                  <td className="muted">{r.decidedNote}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  );
}
