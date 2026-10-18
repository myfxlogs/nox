// HedgeFlow.tsx —— 统一流水卡（arb PositionsPageHedgeFlow 视觉层逐字借形，
// D-212 数据层=NOX 真分页（后端 total+offset 下推——超 arb 客户端切片件）：
// card 壳+card-head+hedge-flow-filter 按钮组+rows hedge-flow-table resp+
// data-label 六列+flowDirection→heat-pos/neg/neutral 三色+/-前缀+fmtNum/
// fmtUsd 单位格式化+kindText/sourceText 中文映射逐字集+tbody 空态行。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import { fmtNum, fmtUsd } from "./fmt";
import type { HedgeFlowItem } from "../gen/dashboardv1/position_pb";

const PAGE = 10;

// kindText 中文映射统一（arb 逐字集——设计文档 §2 口径+D-279 五新类+CMC 专类）。
const kindText = (k: string): string => {
  switch (k) {
    case "rental_income": return "租金收入";
    case "vote_claim": return "奖励提取";
    case "deposit": return "入金";
    case "withdraw": return "出金";
    case "stake": return "质押";
    case "unstake": return "解押";
    case "overnight": return "隔夜息";
    case "permission_update": return "权限变更";
    case "unstake_cancel": return "取消解冻";
    case "vote": return "投票";
    case "delegate": return "委托资源";
    case "undelegate": return "收回委托";
    case "close_trade": return "平仓";
    case "sell_order": return "卖出委托";
    case "order_cancelled": return "撤单";
    default: return "其他";
  }
};

// sourceText 中文映射（arb 逐字）。
const sourceText = (s: string): string => (s === "trx" ? "TRX" : s === "cmc" ? "CMC" : s);

// flowDirection 收支方向判定（D-280 业主裁定口径逐字借形）：
// CMC 按金额正负（正=收入绿+，负=支出红-，零=中性蓝——金额直表资金增减）；
// TRX 按 kind（fee 类 kind 标红支出与 fee 语义自洽；undelegate 蓝中性业主指定）。
const flowDirection = (kind: string, source: string, amountValue: number): "income" | "expense" | "neutral" => {
  if (source === "cmc") {
    if (amountValue > 0) return "income";
    if (amountValue < 0) return "expense";
    return "neutral";
  }
  switch (kind) {
    case "deposit": case "rental_income": case "vote_claim":
      return "income";
    case "withdraw": case "permission_update": case "unstake_cancel":
    case "vote": case "delegate":
      return "expense";
    case "stake": case "unstake": case "undelegate":
      return "neutral";
    default: return "neutral";
  }
};

export function HedgeFlow() {
  const [items, setItems] = useState<HedgeFlowItem[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [src, setSrc] = useState("");
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");

  const load = useCallback(async (source: string, pg: number) => {
    setLoading(true);
    try {
      const r = await client.listHedgeFlow({ sourceFilter: source, limit: PAGE, offset: (pg - 1) * PAGE });
      setItems(r.txs); setTotal(Number(r.total)); setErr("");
    } catch (e) { setErr(errText(e)); } finally { setLoading(false); }
  }, []);

  useEffect(() => { void load(src, page); }, [src, page, load]);
  useEffect(() => { setPage(1); }, [src]); // 切源回页1
  useEffect(() => onCase("dataRefresh", () => { void load(src, page); }), [src, page, load]);

  const totalPages = Math.max(1, Math.ceil(total / PAGE));
  const safePage = Math.min(page, totalPages);

  return (
    <section className="card" aria-labelledby="hedge-flow-title">
      <div className="card-head">
        <h2 id="hedge-flow-title">统一流水</h2>
        <div className="hedge-flow-filter" role="group" aria-label="来源筛选">
          {(["", "trx", "cmc"] as const).map((v) => (
            <button key={v} type="button"
              className={src === v ? "btn-primary btn-sm" : "btn-sm"}
              onClick={() => setSrc(v)}>
              {v === "" ? "全部" : sourceText(v)}
            </button>
          ))}
        </div>
      </div>

      {err && <div className="banner" role="alert"><span>{err}</span></div>}

      <div className="table-scroll scroll-cap">
        <table className="rows hedge-flow-table resp">
          <thead>
            <tr>
              <th scope="col">时间</th><th scope="col">来源</th><th scope="col">类型</th>
              <th scope="col">金额</th><th scope="col">对手方</th><th scope="col">交易号</th>
            </tr>
          </thead>
          <tbody>
            {items.map((t, i) => {
              const ts = t.ts ? new Date(Number(t.ts.seconds) * 1000) : null;
              const dir = flowDirection(t.kind, t.source, t.amountValue);
              return (
                <tr key={`${t.txId}-${i}`}>
                  <td data-label="时间">{ts ? ts.toLocaleString("zh-CN", { hour12: false }) : "—"}</td>
                  <td data-label="来源">{sourceText(t.source)}</td>
                  <td data-label="类型">{kindText(t.kind)}</td>
                  <td data-label="金额" className={
                    dir === "income" ? "heat-pos" : dir === "expense" ? "heat-neg" : "heat-neutral"}>
                    {t.amountValue > 0 && dir === "income" ? "+" :
                     t.amountValue > 0 && dir === "expense" ? "-" : ""}
                    {t.amountUnit === "TRX" ? fmtNum(t.amountValue) + " TRX" : fmtUsd(t.amountValue) + " USD"}
                  </td>
                  <td className="mono" data-label="对手方">{t.counterparty || "—"}</td>
                  <td className="mono" data-label="交易号">{t.txId || "—"}</td>
                </tr>
              );
            })}
            {items.length === 0 && (
              <tr><td colSpan={6} className="empty">{loading ? "加载中…" : "暂无流水"}</td></tr>
            )}
          </tbody>
        </table>
      </div>

      {totalPages > 1 && (
        <div className="pager">
          <button type="button" disabled={safePage <= 1} onClick={() => setPage(safePage - 1)}>上一页</button>
          <span>第 {safePage} / {totalPages} 页（共 {total} 条）</span>
          <button type="button" disabled={page >= totalPages} onClick={() => setPage(safePage + 1)}>下一页</button>
        </div>
      )}
    </section>
  );
}
