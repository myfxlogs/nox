// graduation.tsx —— 毕业授权段（投资人页挂段）：pending 表 + dry_run 试算 +
// 三模互斥批准 + decided 历史只读。前端零信任：折算/池映射/风险判全后端回显。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type {
  ApproveCarryGraduationResponse,
  CarryGraduationRow,
} from "../gen/dashboardv1/graduation_pb";
import { fmtUsd } from "./fmt";

type Mode = "pct" | "usd" | "explicit";

function covBadge(r: CarryGraduationRow): string {
  const c = r.coverageRatio;
  if (c == null) return "—";
  return (c * 100).toFixed(0) + "%" + (c < 0.5 ? "🔴" : c < 0.8 ? "🟡" : "");
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
    const why = window.prompt("拒绝理由（回写旗标 warn）:");
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
      <h2>毕业授权{pending.length > 0 ? `（${pending.length} 待批）` : ""}</h2>
      {err && <div className="err">{err}</div>}
      {pending.length === 0 && decided.length === 0 && <div className="muted">无毕业提案</div>}
      {pending.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>#</th><th>symbol</th><th>结构</th><th>venue 对</th><th>age</th>
              <th>accrued</th><th>net_all_in</th><th>funding_ann</th><th>coverage</th><th></th>
            </tr>
          </thead>
          <tbody>
            {pending.map((r) => (
              <tr key={Number(r.id)}>
                <td>{Number(r.id)}</td>
                <td>{r.symbol}{r.flagWarn ? <span className="warn"> ⚠{r.flagWarn}</span> : null}</td>
                <td>{r.structure}</td>
                <td>{r.venue}</td>
                <td>{r.ageDays.toFixed(1)}d</td>
                <td className={r.accruedUsd >= 0 ? "good" : "crit"}>{fmtUsd(r.accruedUsd)}</td>
                <td>{r.netAllInUsd != null ? fmtUsd(r.netAllInUsd) : "—"}</td>
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
          <div className="k">批准 #{openId} —— 三模互斥（先 dry_run 试算再批准）</div>
          <div style={{ margin: "8px 0" }}>
            {([["pct", "占比 %"], ["usd", "金额 USD"], ["explicit", "显式四字段"]] as [Mode, string][]).map(([m, label]) => (
              <label key={m} style={{ marginRight: "12px" }}>
                <input type="radio" checked={mode === m} onChange={() => { setMode(m); setPreview(null); }} /> {label}
              </label>
            ))}
          </div>
          {mode === "pct" && (
            <input placeholder="可用基准占比 %（如 5）" value={pct} onChange={(e) => setPct(e.target.value)} />
          )}
          {mode === "usd" && (
            <input placeholder="分配金额 USD（如 200）" value={usd} onChange={(e) => setUsd(e.target.value)} />
          )}
          {mode === "explicit" && (
            <div>
              <input placeholder="买腿 qty 上限" value={lq} onChange={(e) => setLq(e.target.value)} />{" "}
              <input placeholder="卖腿 qty 上限" value={sq} onChange={(e) => setSq(e.target.value)} />{" "}
              <input placeholder="双腿名义上限 USD" value={gross} onChange={(e) => setGross(e.target.value)} />{" "}
              <input placeholder="名义额 USD（可缺）" value={notional} onChange={(e) => setNotional(e.target.value)} />
            </div>
          )}
          <input placeholder="note（可缺）" value={note} onChange={(e) => setNote(e.target.value)}
            style={{ marginTop: "6px", width: "60%" }} />
          <div style={{ marginTop: "8px" }}>
            <button disabled={busy} onClick={() => submit(openId, true)}>dry_run 试算</button>{" "}
            <button disabled={busy || preview == null || !preview.ok} onClick={() => submit(openId, false)}>
              确认批准
            </button>
          </div>

          {preview && (
            <div className="muted" style={{ marginTop: "8px", fontSize: "12px" }}>
              {!preview.ok && <div className="err">{preview.error}</div>}
              {preview.ok && (
                <>
                  <div>est_gross={fmtUsd(preview.estGrossUsd)} ｜ 腿名义={fmtUsd(preview.estLegNotionalUsd)}
                    ｜ qty 双算 long={preview.estLongQty.toFixed(4)} short={preview.estShortQty.toFixed(4)}
                    ｜ alloc={fmtUsd(preview.estAllocUsd)}（{(preview.estAllocPct * 100).toFixed(2)}%）</div>
                  <div>基准 {fmtUsd(preview.allocBaseUsd)}{preview.allocBaseNote ? `（${preview.allocBaseNote}）` : ""}
                    ｜ risk={preview.riskLevel}{preview.suggestedAllocUsd > 0 ? ` 建议 ${fmtUsd(preview.suggestedAllocUsd)}` : ""}</div>
                  {preview.riskReasons.map((x, i) => <div key={i} className="warn">⚠ {x}</div>)}
                  {preview.legPools.map((lp) => (
                    <div key={lp.venueKey}>
                      {lp.venueKey}: {lp.found ? `${fmtUsd(lp.equity)} ${lp.currency}@${lp.poolVenue}:${lp.poolVenueType}${lp.isMin ? "（约束池）" : ""} src=${lp.source}` : "未采集"}
                    </div>
                  ))}
                  {preview.venueGaps.length > 0 && <div className="warn">venue 缺口: {preview.venueGaps.join("；")}</div>}
                  {preview.sizingNotes.map((x, i) => <div key={i}>· {x}</div>)}
                </>
              )}
            </div>
          )}
        </div>
      )}

      {decided.length > 0 && (
        <>
          <h3 className="muted">历史（只读）</h3>
          <table>
            <thead><tr><th>#</th><th>symbol</th><th>venue 对</th><th>状态</th><th>裁定人</th><th>combo</th><th>note</th></tr></thead>
            <tbody>
              {decided.map((r) => (
                <tr key={Number(r.id)}>
                  <td>{Number(r.id)}</td><td>{r.symbol}</td><td>{r.venue}</td>
                  <td>{r.status}</td><td>{r.decidedBy || "委员会"}</td>
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
