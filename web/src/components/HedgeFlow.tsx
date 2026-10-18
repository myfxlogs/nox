// HedgeFlow.tsx —— 统一流水卡（arb PositionsPageHedgeFlow 借形，D-212）：
// ListHedgeFlow 双源归一行（trx/cmc source filter+limit/offset 全下推后端，
// 前端零聚合零换算——amount_value 原生单位+unit 直渲）。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import type { HedgeFlowItem } from "../gen/dashboardv1/position_pb";
import { EmptyState } from "./EmptyState";

const PAGE = 50;

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
  const [src, setSrc] = useState("");
  const [hasMore, setHasMore] = useState(false);
  const [err, setErr] = useState("");

  const load = useCallback(async (source: string, offset: number, append: boolean) => {
    try {
      const r = await client.listHedgeFlow({ sourceFilter: source, limit: PAGE, offset });
      setItems((prev) => (append ? [...prev, ...r.txs] : r.txs));
      setHasMore(r.txs.length === PAGE);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  }, []);

  useEffect(() => {
    setItems([]);
    void load(src, 0, false);
  }, [src, load]);

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
      {items.length > 0 ? (
        <div className="table-scroll scroll-cap">
        <table className="rows">
          <thead><tr><th>时点</th><th>源</th><th>类型</th><th>金额</th><th>对手方/单据</th></tr></thead>
          <tbody>
            {items.map((t, i) => (
              <tr key={t.txId + i}>
                <td>{t.ts ? new Date(Number(t.ts.seconds) * 1000).toLocaleString() : "—"}</td>
                <td>{t.source}</td>
                <td>{kindCN(t.kind)}</td>
                <td>{t.amountValue.toFixed(t.amountUnit === "TRX" ? 4 : 2)} {t.amountUnit}</td>
                <td className="muted">{t.counterparty || t.txId}</td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      ) : (
        <EmptyState>零流水行</EmptyState>
      )}
      {hasMore && (
        <div style={{ marginTop: 8 }}>
          <a href="#" onClick={(e) => { e.preventDefault(); void load(src, items.length, true); }}>加载更多…</a>
        </div>
      )}
    </section>
  );
}
