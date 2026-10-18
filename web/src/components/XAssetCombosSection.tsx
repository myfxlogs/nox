// XAssetCombosSection.tsx —— X-asset 已授权实盘组合（arb BrokersPageXAssetCombos
// 借形，D-557 S3 列重排）：组合|单笔|累计上限|资金状态徽标|操作。
// fundBadge 三态=后端字段直读（capConflict→资金不足/poolVerdict→待定/enabled→停用/ok→就绪）。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { XAssetAllowComboRow } from "../gen/dashboardv1/graduation_pb";
import { Chip } from "./Chip";
import { EmptyState } from "./EmptyState";
import { ConfirmDialog, Modal } from "./Modal";
import { ts } from "./fmt";

function fundBadge(r: XAssetAllowComboRow): { text: string; tone: "good" | "warn" | "critical" | "neutral" } {
  if (r.capConflict) return { text: "资金不足", tone: "critical" };
  if (r.poolVerdict === "unknown") return { text: "待定", tone: "warn" };
  if (!r.enabled) return { text: "停用", tone: "neutral" };
  return { text: "就绪", tone: "good" };
}

export function XAssetCombosSection() {
  const [rows, setRows] = useState<XAssetAllowComboRow[]>([]);
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  const [cfm, setCfm] = useState<{ text: string; run: () => void } | null>(null);
  const [marginRow, setMarginRow] = useState<XAssetAllowComboRow | null>(null);
  const [marginIn, setMarginIn] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await client.listXAssetAllowCombos({});
      setRows(r.rows);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  }, []);
  useEffect(() => {
    void load();
    return onCase("dataRefresh", () => void load());
  }, [load]);

  const act = async (fn: () => Promise<unknown>) => {
    try {
      await fn();
      await load();
    } catch (e) {
      setErr(errText(e));
    }
  };

  return (
    <section>
      <h2>已授权实盘组合</h2>
      {err && <div className="err">{err}</div>}
      {msg && <div className="warn">{msg}</div>}
      {rows.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {rows.map((r) => {
            const b = fundBadge(r);
            return (
              <li key={String(r.id)} className="opp-card">
                <div className="opp-card-head">
                  <span className="opp-card-kind">{r.canonicalBuy}({r.venueBuy})×{r.canonicalSell}({r.venueSell})</span>
                  <Chip tone={b.tone}>{b.text}</Chip>
                </div>
                <div className="opp-card-narrative">
                  单笔 {r.notionalUsd !== undefined ? `$${r.notionalUsd.toFixed(0)}` : "—"} · 保证金 {r.marginUsd !== undefined ? `$${r.marginUsd.toFixed(0)}` : "—"} · 累计上限 ${r.maxGrossUsd.toFixed(0)} · 在仓 ${(r.activeNotionalUsd ?? 0).toFixed(0)}
                </div>
                <div className="opp-card-narrative">
                  {r.breakerPaused && <span className="warn">熔断{`×${r.breakerCount}`}至 {ts(r.breakerResumeAt)} · </span>}
                  {r.recentFundingReject && <span className="muted">近期拒：{r.recentRejectReason} · </span>}
                  买池 ${(r.buyPoolUsd ?? 0).toFixed(0)} / 卖池 ${(r.sellPoolUsd ?? 0).toFixed(0)}{r.poolSource ? ` (${r.poolSource})` : ""}
                </div>
                <div className="opp-card-narrative" style={{ display: "flex", gap: 8 }}>
                  <a href="#" onClick={(e) => { e.preventDefault(); setCfm({
                    text: `${r.enabled ? "停用" : "启用"}组合 ${r.canonicalBuy}×${r.canonicalSell}？`,
                    run: () => act(() => client.upsertXAssetAllowCombo({
                      id: r.id, canonicalBuy: r.canonicalBuy, venueBuy: r.venueBuy,
                      canonicalSell: r.canonicalSell, venueSell: r.venueSell,
                      maxLongQty: r.maxLongQty, maxShortQty: r.maxShortQty, maxGrossUsd: r.maxGrossUsd,
                      notionalUsd: r.notionalUsd, enabled: !r.enabled, note: r.note, confirm: true,
                    })),
                  }); }}>{r.enabled ? "停用" : "启用"}</a>
                  {r.breakerPaused && (
                    <a href="#" onClick={(e) => { e.preventDefault(); act(() => client.resumeXAssetCombo({ comboId: r.id })); }}>解除熔断</a>
                  )}
                  <a href="#" onClick={(e) => { e.preventDefault(); setMarginRow(r); setMarginIn(r.marginUsd !== undefined ? String(r.marginUsd) : ""); }}>改保证金</a>
                </div>
              </li>
            );
          })}
        </ul>
      ) : (
        <EmptyState>零授权组合</EmptyState>
      )}
      <Modal open={marginRow != null} onClose={() => setMarginRow(null)} width={400}
        title={`保证金预算 ${marginRow?.canonicalBuy}×${marginRow?.canonicalSell}`}>
        <div style={{ display: "grid", gap: 8 }}>
          <label>双腿保证金总预算 USD（空=清除，与单笔名义互斥）
            <input type="number" min="0" step="1" value={marginIn} onChange={(e) => setMarginIn(e.target.value)} />
          </label>
          <div>
            <button onClick={() => {
              const r = marginRow; if (!r) return;
              const m = marginIn.trim() === "" ? undefined : Number(marginIn);
              if (m !== undefined && (!Number.isFinite(m) || m <= 0)) { setErr("保证金须为正数或留空清除"); return; }
              setErr(""); setMsg("");
              act(async () => {
                const resp = await client.upsertXAssetAllowCombo({
                  id: r.id, canonicalBuy: r.canonicalBuy, venueBuy: r.venueBuy,
                  canonicalSell: r.canonicalSell, venueSell: r.venueSell,
                  maxLongQty: r.maxLongQty, maxShortQty: r.maxShortQty, maxGrossUsd: r.maxGrossUsd,
                  notionalUsd: r.notionalUsd, enabled: r.enabled, note: r.note,
                  marginUsd: m, confirm: true,
                });
                if (resp.overSubscribedWarn) setMsg(resp.overSubscribedWarn);
                if (!resp.ok && resp.error) setErr(resp.error);
              });
              setMarginRow(null);
            }}>保存</button>{" "}
            <button onClick={() => setMarginRow(null)}>取消</button>
          </div>
        </div>
      </Modal>
      <ConfirmDialog open={cfm != null} text={cfm?.text}
        onCancel={() => setCfm(null)}
        onConfirm={() => { const r = cfm?.run; setCfm(null); r?.(); }} />
    </section>
  );
}
