// HedgeFlow.tsx —— 统一流水卡（arb PositionsPageHedgeFlow 借形，D-212）：
// 页码式真分页（PAGE=10 每页 RPC limit/offset 全下推后端，total 后端 COUNT——
// 「加载更多」追加式撤除；前端零聚合零换算——amount_value 原生单位直渲）。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { HedgeFlowItem } from "../gen/dashboardv1/position_pb";
import { EmptyState } from "./EmptyState";

const PAGE = 10;

function kindCN(k: string): string {
  switch (k) {
    case "rental_income": return "租金收入";
    case "vote_claim": return "奖励提取";
    case "deposit": return "充值";
    case "withdraw": return "提币";
    case "stake": return "质押";
    case "unstake": return "解押";
    case "overnight": return "过夜结算";
    case "close_trade": return "平仓结算";
    case "sell_order": return "卖单";
    case "order_cancelled": return "撤单";
    default: return k;
  }
}

export function HedgeFlow() {
  const [items, setItems] = useState<HedgeFlowItem[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [src, setSrc] = useState("");
  const [err, setErr] = useState("");

  const load = useCallback(async (source: string, pg: number) => {
    try {
      const r = await client.listHedgeFlow({ sourceFilter: source, limit: PAGE, offset: (pg - 1) * PAGE });
      setItems(r.txs);
      setTotal(Number(r.total));
      setErr("");
    } catch (e) { setErr(errText(e)); }
  }, []);

  useEffect(() => { void load(src, page); }, [src, page, load]);
  // 切 source 回页1；流帧重取当前页不跳页（零轮询）。
  useEffect(() => { setPage(1); }, [src]);
  useEffect(() => onCase("dataRefresh", () => { void load(src, page); }), [src, page, load]);

  const totalPages = Math.max(1, Math.ceil(total / PAGE));

  return (
    <section>
      <h2>
        统一流水
        <span style={{ fontSize: 12, marginLeft: 10 }}>
          {["", "trx", "cmc"].map((v) => (
            <a key={v} href="#" style={{ marginRight: 8, color: src === v ? "#ddd" : "#888" }}
              onClick={(e) => { e.preventDefault(); setSrc(v); }}>
              {v === "" ? "全部" : v}
            </a>
          ))}
        </span>
      </h2>
      {err && <div className="err">{err}</div>}
      <div className="table-scroll scroll-cap">
        <table className="rows">
          <thead>
            <tr><th>时间</th><th>源</th><th>类型</th><th>金额</th><th>对手</th><th>tx</th></tr>
          </thead>
          <tbody>
            {items.map((t, i) => (
              <tr key={i}>
                <td>{t.ts ? new Date(Number(t.ts.seconds) * 1000).toISOString().replace("T", " ").slice(5, 19) : "—"}</td>
                <td>{t.source}</td>
                <td>{kindCN(t.kind)}</td>
                <td className="num">{t.amountValue} {t.amountUnit}</td>
                <td>{t.counterparty || "—"}</td>
                <td className="muted" style={{ maxWidth: 120, overflow: "hidden", textOverflow: "ellipsis" }}>{t.txId || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {items.length === 0 && <EmptyState>零流水行</EmptyState>}
      {totalPages > 1 && (
        <div className="pager">
          <button type="button" disabled={page <= 1} onClick={() => setPage(page - 1)}>上一页</button>
          <span>第 {page} / {totalPages} 页（共 {total} 条）</span>
          <button type="button" disabled={page >= totalPages} onClick={() => setPage(page + 1)}>下一页</button>
        </div>
      )}
    </section>
  );
}
