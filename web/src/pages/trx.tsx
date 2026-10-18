// trx.tsx —— TRX 页三区移植（arb-archive TrxPage 借形重写：赚多少/安全吗/发生了什么）。
// 数据=既有 RPC 面：GetTrxYieldRadar+GetTrxYieldCurve+TrxAccountRefresh（on-open
// 双踢 D-254）+ListPersonalPositions（short_hedge）+新 ListTrxTxs 读面。
// 监控提醒=watch_rule CRUD 现成面（TRX 域主题过滤下推前端展示——规则 CRUD 是
// 页面级提醒设置非业务判定）。只读监控+提醒设置，零自动执行面（D-512 语义族）。
// CMC 域=爬虫件待移植（零 fin_cmc_* 表/零装配件实锤——业主指正非 API 凭据缺口）。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { TrxAccountRefreshResponse, TrxYieldCurvePoint, TrxTxRow } from "../gen/dashboardv1/trx_cex_pb";
import type { PersonalPositionRow } from "../gen/dashboardv1/dci_panel_pb";
import type { WatchRule } from "../gen/dashboardv1/watch_rule_pb";
import { StatTile } from "../components/StatTile";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";
import { Modal } from "../components/Modal";

const fmtPct = (v: number) => v.toFixed(2) + "%";
const fmtTrx = (v: number) => v.toLocaleString("en-US", { maximumFractionDigits: 4 });

// kind 中文映射（显示映射非判据——fin_trx_txs.kind 域内枚举）。
function kindCN(k: string): string {
  switch (k) {
    case "rental_income": return "租金收入";
    case "vote_claim": return "奖励提取";
    case "deposit": return "充值";
    case "withdraw": return "提币";
    case "stake": return "质押";
    case "unstake": return "解押";
    case "vote": return "投票";
    case "delegate": return "资源委托";
    case "undelegate": return "解除委托";
    case "permission_update": return "权限变更";
    case "unstake_cancel": return "解押撤销";
    default: return k;
  }
}

export default function TrxPage() {
  const [radar, setRadar] = useState<{
    energy7d: number; energy30d: number; voteCur: number; vote30d: number;
    voteMax: number; voteMaxSr: string; voteSpread: number; floor: number;
    s7d: number; s30d: number; transition: boolean;
  } | null>(null);
  const [curve, setCurve] = useState<TrxYieldCurvePoint[]>([]);
  const [ann7d, setAnn7d] = useState(0);
  const [ann30d, setAnn30d] = useState(0);
  const [acct, setAcct] = useState<TrxAccountRefreshResponse | null>(null);
  const [hedges, setHedges] = useState<PersonalPositionRow[]>([]);
  const [txs, setTxs] = useState<TrxTxRow[]>([]);
  const [kindF, setKindF] = useState("");
  const [rules, setRules] = useState<WatchRule[]>([]);
  const [ruleModal, setRuleModal] = useState(false);
  const [rf, setRf] = useState({ subject: "TRXUSDT", op: "ge", threshold: "", note: "" });
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const loadTxs = useCallback(async (kind: string) => {
    const r = await client.listTrxTxs({ limit: 100, kind });
    setTxs(r.txs);
  }, []);

  const load = useCallback(async () => {
    try {
      const [rd, cv, p, ru] = await Promise.all([
        client.getTrxYieldRadar({}),
        client.getTrxYieldCurve({ days: 30 }),
        client.listPersonalPositions({ status: "OPEN" }),
        client.listWatchRules({}),
      ]);
      setRadar({
        energy7d: rd.energyApr7d, energy30d: rd.energyApr30d,
        voteCur: rd.voteAprCurrent, vote30d: rd.voteApr30dAvg,
        voteMax: rd.voteAprMax, voteMaxSr: rd.voteAprMaxSr, voteSpread: rd.voteAprSpread,
        floor: rd.floorPct, s7d: rd.sampleDays7d, s30d: rd.sampleDays30d,
        transition: rd.transition,
      });
      setCurve(cv.points);
      setAnn7d(cv.annualized7d);
      setAnn30d(cv.annualized30d);
      setHedges(p.positions.filter((x) => x.kind === "short_hedge"));
      setRules(ru.rules.filter((r) => r.subject.includes("TRX")));
      await loadTxs(kindF);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  }, [kindF, loadTxs]);

  useEffect(() => {
    // on-open 双踢（D-254）——静默败不挡页
    void client.trxAccountRefresh({}).then(setAcct).catch(() => undefined);
    void load();
    const off = onCase("dataRefresh", () => void load());
    return off;
  }, [load]);

  const saveRule = async () => {
    setBusy(true);
    try {
      await client.upsertWatchRule({
        rule: {
          id: 0n, kind: "price", subject: rf.subject, venueKey: "",
          op: rf.op, threshold: Number(rf.threshold) || 0,
          fireMode: "repeat", cooldownSec: 300, note: rf.note, enabled: true,
        } as unknown as WatchRule,
      });
      setRuleModal(false);
      await load();
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };
  const delRule = async (id: bigint) => {
    try {
      await client.deleteWatchRule({ id });
      await load();
    } catch (e) {
      setErr(errText(e));
    }
  };

  return (
    <div>
      <h1>TRX <small style={{ fontSize: 12, color: "#777" }}>手动投资页·只读监控+提醒（CMC 爬虫面待移植）</small></h1>
      {err && <div className="err">{err}</div>}

      {/* 区① 赚多少：收益率雷达+曲线（后端算定直渲） */}
      <h2>收益率</h2>
      {radar ? (
        <div className="stats">
          <StatTile label="能量租金 7d" value={fmtPct(radar.energy7d)} sub={`样本 ${radar.s7d}d`} />
          <StatTile label="能量租金 30d" value={fmtPct(radar.energy30d)} sub={`样本 ${radar.s30d}d`} />
          <StatTile label="投票 APR 当前" value={fmtPct(radar.voteCur)} />
          <StatTile label="投票 APR 30d 均值" value={fmtPct(radar.vote30d)} />
          <StatTile label="市场最高" value={fmtPct(radar.voteMax)} sub={radar.voteMaxSr} />
          <StatTile label="floor 阈" value={fmtPct(radar.floor)} />
          <StatTile label="合成 7d" value={fmtPct(ann7d)} />
          <StatTile label="合成 30d" value={fmtPct(ann30d)} />
        </div>
      ) : (
        <EmptyState>收益率读模未供</EmptyState>
      )}
      {radar?.transition && <div className="warn">扩容过渡期——读数=旧租金÷新在押，floor 判定无效</div>}
      {curve.length > 0 && (
        <ul className="opp-cards" style={{ maxHeight: 180, overflowY: "auto" }}>
          {[...curve].slice(-14).reverse().map((p, i) => (
            <li key={i} className="opp-card" style={{ cursor: "default" }}>
              <div className="opp-card-head">
                <b>{p.day ? p.day.seconds ? new Date(Number(p.day.seconds) * 1000).toISOString().slice(0, 10) : "—" : "—"}</b>
                <span className="opp-card-who">
                  租金 {fmtTrx(p.energyTrx)} TRX · 投票 {fmtTrx(p.votingTrx)} TRX · 合计 ${p.dailyTotalUsd.toFixed(2)}
                </span>
              </div>
            </li>
          ))}
        </ul>
      )}

      {/* 区② 安全吗：账户资源快照+对冲仓结构（refresh 全字段直渲） */}
      <h2>对冲结构</h2>
      {acct ? (
        <div className="stats">
          <StatTile label="TRX 余额" value={fmtTrx(acct.trxBalance)} />
          <StatTile label="可领奖励" value={fmtTrx(acct.trxAllowance)} />
          <StatTile label="能量委托" value={fmtTrx(acct.energyDelegated)} />
          <StatTile label="票数" value={fmtTrx(acct.votes)} />
          <StatTile label="能量用量" value={`${fmtTrx(acct.energyUsed)}/${fmtTrx(acct.energyLimit)}`}
            sub={`带宽 ${acct.bandwidthStatus}`} />
          <StatTile label="在途解押" value={acct.unfrozen ? "有" : "无"} sub={acct.unfrozen || undefined} />
        </div>
      ) : (
        <EmptyState>账户快照未供</EmptyState>
      )}
      {hedges.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {hedges.map((p) => (
            <li key={String(p.id)} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">{p.asset} 对冲腿</span>
                <Chip tone="warn">{p.kind}</Chip>
              </div>
              <div className="opp-card-narrative">
                量 {fmtTrx(p.qty)} · 入场 {p.entryPrice} · 开 {tsUnixN(p.openedAt)}
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <div className="muted" style={{ fontSize: 12 }}>零对冲仓行</div>
      )}

      {/* 区③ 发生了什么：fin_trx_txs 流水（kind 过滤下推后端） */}
      <h2>流水
        <span style={{ fontSize: 12, marginLeft: 10 }}>
          {["", "rental_income", "vote_claim", "deposit", "withdraw", "stake", "unstake", "delegate", "undelegate", "vote"].map((k) => (
            <a key={k} href="#" style={{ marginRight: 8, color: kindF === k ? "#ddd" : "#888" }}
              onClick={(e) => { e.preventDefault(); setKindF(k); void loadTxs(k); }}>
              {k === "" ? "全部" : kindCN(k)}
            </a>
          ))}
        </span>
      </h2>
      {txs.length > 0 ? (
        <ul className="opp-cards" style={{ maxHeight: 300, overflowY: "auto" }}>
          {txs.map((t) => (
            <li key={String(t.id)} className="opp-card" style={{ cursor: "default" }}>
              <div className="opp-card-head">
                <b>{kindCN(t.kind)}</b>
                <span className="opp-card-who">
                  {fmtTrx(t.amountTrx)} TRX
                  {t.counterparty ? ` · ${t.counterparty}` : ""}
                  {t.cheatStatus && <span className="crit"> 恶意标</span>}
                </span>
                <span className="opp-card-friction">
                  {t.ts ? new Date(Number(t.ts.seconds) * 1000).toLocaleString() : "—"}
                </span>
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零流水行</EmptyState>
      )}

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

// tsUnixN bigint unix → 本地串（显示件）。
function tsUnixN(u: bigint | number): string {
  const n = Number(u);
  return n > 0 ? new Date(n * 1000).toLocaleString() : "—";
}
