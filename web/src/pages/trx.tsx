// trx.tsx —— TRX 页 arb 原版式三卡重排（arb TrxPage/TrxYieldRadar/TrxHedgeCard
// 借形，业主令 10-19「直接用 arb 归档前端复制」）：收益率（6 格 grid+敞口计量）
// →对冲结构（镜像双腿表+净敞口徽章+carry 条+双账户摘要+↻ 流帧驱动刷新）
// →统一流水（HedgeFlow 组件）→监控提醒（watch_rule CRUD 页尾保留）。
// on-open trxAccountRefresh 双踢（D-254）+onCase("dataRefresh") trx 流帧重取——
// arb setTimeout 等待拍禁搬入。只读监控+提醒设置零自动执行面。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { WatchRule } from "../gen/dashboardv1/watch_rule_pb";
import { TrxYieldRadar } from "../components/TrxYieldRadar";
import { TrxHedgeCard } from "../components/TrxHedgeCard";
import { HedgeFlow } from "../components/HedgeFlow";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";
import { Modal } from "../components/Modal";

export default function TrxPage() {
  const [rules, setRules] = useState<WatchRule[]>([]);
  const [err, setErr] = useState("");
  const [ruleModal, setRuleModal] = useState(false);
  const [busy, setBusy] = useState(false);
  const [rf, setRf] = useState({ subject: "TRXUSDT", op: "lt", threshold: "", note: "" });
  const delRule = async (id: bigint) => {
    try { await client.deleteWatchRule({ id }); await load(); } catch (e) { setErr(errText(e)); }
  };
  const saveRule = async () => {
    setBusy(true);
    try {
      await client.upsertWatchRule({ rule: { subject: rf.subject, op: rf.op, threshold: Number(rf.threshold) || 0, note: rf.note, enabled: true, fireMode: "once" } });
      setRuleModal(false); setRf({ subject: "TRXUSDT", op: "lt", threshold: "", note: "" });
      await load();
    } catch (e) { setErr(errText(e)); } finally { setBusy(false); }
  };

  const load = useCallback(async () => {
    try {
      const r = await client.listWatchRules({});
      setRules(r.rules.filter((x) => x.subject === "TRX"));
      setErr("");
    } catch (e) { setErr(errText(e)); }
  }, []);
  useEffect(() => {
    void load();
    void client.trxAccountRefresh({}).catch(() => {}); // D-254 on-open 双踢体感件
    return onCase("dataRefresh", (v) => {
      const t = (v as { topic: string }).topic;
      if (t === "trx" || t === "watch_rules") void load();
    });
  }, [load]);

  return (
    <div className="page">
      <h1>TRX <small className="muted" style={{ fontSize: 12 }}>手动投资页·只读监控+提醒</small></h1>
      {err && <div className="err">{err}</div>}

      <TrxYieldRadar />
      <TrxHedgeCard />
      <HedgeFlow />

      {/* 监控提醒区：TRX 域 watch_rule CRUD（现成面，提醒设置非自动执行） */}
      <h2>监控提醒
        <button style={{ marginLeft: 12 }} onClick={() => setRuleModal(true)}>新增 TRX 规则</button>
      </h2>
      {rules.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {rules.map((r) => (
            <li key={Number(r.id)} className="opp-card" style={{ opacity: r.enabled ? 1 : 0.45 }}>
              <div className="opp-card-head">
                <span className="opp-card-kind">{r.subject}</span>
                <Chip tone={r.enabled ? "good" : "neutral"}>{r.enabled ? "启用" : "停用"}</Chip>
              </div>
              <div className="opp-card-narrative">
                mid {r.op} {r.threshold} · {r.fireMode}
                {r.note ? ` · ${r.note}` : ""}
              </div>
              <div className="opp-card-narrative">
                <a href="#" onClick={(e) => { e.preventDefault(); void client.upsertWatchRule({ rule: { ...r, enabled: !r.enabled } }).then(load).catch((e2) => setErr(errText(e2))); }}>
                  {r.enabled ? "停用" : "启用"}
                </a>{" "}
                <a href="#" onClick={(e) => { e.preventDefault(); void delRule(r.id); }}>删</a>
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零 TRX 域规则——新增即接现成盯盘引擎</EmptyState>
      )}

      <Modal open={ruleModal} onClose={() => setRuleModal(false)} title="新增 TRX 盯盘规则" width={420}>
        <div style={{ display: "grid", gap: 8 }}>
          <input value={rf.subject} onChange={(e) => setRf({ ...rf, subject: e.target.value })} placeholder="标的 canonical（如 TRXUSDT）" />
          <div>
            <select value={rf.op} onChange={(e) => setRf({ ...rf, op: e.target.value })}>
              {["lt", "le", "gt", "ge"].map((o) => <option key={o} value={o}>{o}</option>)}
            </select>{" "}
            <input style={{ width: 160 }} type="number" step="any" value={rf.threshold}
              onChange={(e) => setRf({ ...rf, threshold: e.target.value })} placeholder="阈值（mid 价）" />
          </div>
          <input value={rf.note} onChange={(e) => setRf({ ...rf, note: e.target.value })} placeholder="备注" />
          <div>
            <button disabled={busy} onClick={() => void saveRule()}>保存</button>{" "}
            <button onClick={() => setRuleModal(false)}>取消</button>
          </div>
        </div>
      </Modal>
    </div>
  );
}

