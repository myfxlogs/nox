// QuoteStrip.tsx —— S1′ 移植裁剪（报价条形态：品种+价+新鲜度点）。
import { FreshDot as Dot } from "./FreshDot";
import type { FreshDot as Fd } from "./freshness";

export interface Quote { symbol: string; price?: number; venue?: string; fresh?: Fd }

export function QuoteStrip({ quotes }: { quotes: Quote[] }) {
  return (
    <div className="quote-strip">
      {quotes.map((q) => (
        <span key={q.symbol} className="quote-cell">
          <b>{q.symbol}</b>
          <span>{q.price ?? "—"}</span>
          {q.fresh ? <Dot dot={q.fresh} /> : null}
          <span className="muted">{q.venue ?? ""}</span>
        </span>
      ))}
    </div>
  );
}
