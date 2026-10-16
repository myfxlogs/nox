// graduation.tsx —— 毕业授权段（投资人页挂段）：待批表 + 试算预览 +
// 三种额度方式互斥批准 + 已裁定历史只读。前端零信任：折算/池映射/风险判全后端回显。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type {
  ApproveCarryGraduationResponse,
  CarryGraduationRow,
} from "../gen/dashboardv1/graduation_pb";
import { fmtUsd } from "./fmt";

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

export default function GraduationPanel() {
  const [rows, setRows] = useState<CarryGraduationRow[]>([]);
  const [err, setErr] = useState("");
  const [openId, setOpenId] = useState<number | null>(null);
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
  const decided = rows.filter((r) => r.status !== "pending");

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

  const reject = async (id: number) => {
    const why = window.prompt("拒绝理由（将记入旗标 warn）:");
    if (why == null) return;
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
        <table>
          <thead>
            <tr>
              <th>#</th><th>品种</th><th>结构</th><th>场所对</th><th>天数</th>
              <th>已计息</th><th>收敛浮盈（现值）</th><th>费估</th><th>全费净额（现值/提案时）</th><th>资金费年化</th><th>覆盖度</th><th></th>
            </tr>
          </thead>
          <tbody>
            {pending.map((r) => (
              <tr key={Number(r.id)}>
                <td>{Number(r.id)}</td>
                <td>{r.symbol}{r.flagWarn ? <span className="warn"> ⚠{r.flagWarn}</span> : null}</td>
                <td>{structCN(r.structure)}</td>
                <td>{r.venue}</td>
                <td>{r.ageDays.toFixed(1)}天</td>
                <td className={r.accruedUsd >= 0 ? "good" : "crit"}>{fmtUsd(r.accruedUsd)}</td>
                <td className={(r.liveConvergenceUsd ?? 0) >= 0 ? "" : "crit"}>
                  {r.liveConvergenceUsd != null ? fmtUsd(r.liveConvergenceUsd) : "—"}</td>
                <td>{r.estFeeUsd != null ? fmtUsd(-r.estFeeUsd) : "—"}</td>
                <td className={(r.liveNetAllInUsd ?? r.netAllInUsd ?? 0) >= 0 ? "good" : "crit"}>
                  {r.liveNetAllInUsd != null ? fmtUsd(r.liveNetAllInUsd) : "—"}
                  {r.netAllInUsd != null ? <span className="muted">（提案 {fmtUsd(r.netAllInUsd)}）</span> : null}</td>
                <td>{r.lastFundingAnn != null ? r.lastFundingAnn.toFixed(2) + "%" : "—"}</td>
                <td>{covBadge(r)}</td>
                <td>
                  <button onClick={() => { setOpenId(openId === Number(r.id) ? null : Number(r.id)); setPreview(null); }}>
                    {openId === Number(r.id) ? "收起" : "批准"}
                  </button>{" "}
                  <button disabled={busy} onClick={() => reject(Number(r.id))}>拒绝</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {openId != null && (
        <div className="card" style={{ marginTop: "8px" }}>
          <div className="k">批准 #{openId} —— 三种额度方式任选其一，先试算再批准</div>
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
        </div>
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
                  <td>{statusCN(r.status)}</td><td>{r.decidedBy || "委员会"}</td>
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
