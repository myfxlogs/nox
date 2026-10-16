// freshness.ts —— S1′ 移植（裁剪借形 arb 同款，源健康行结构松化：调用方
// 给什么取什么，缺省=idle 如实标）。零 gen 依赖。
export interface SourceHealthLike {
  name?: string;
  status?: string;           // live | stale | down | "" 
  lastFactAt?: number;       // ms epoch
}
export interface FreshDot { status: "live" | "stale" | "down"; title: string }

// 新鲜度判：live=有 ts 且 status 活；stale=ts 陈旧或 status=stale；down=其余。
export function freshOf(h: SourceHealthLike, now = Date.now()): FreshDot {
  const st = (h.status ?? "").toLowerCase();
  const ts = h.lastFactAt ?? 0;
  if (st === "down") return { status: "down", title: h.name ?? "" };
  if (st === "stale" || (ts > 0 && now - ts > 300_000)) return { status: "stale", title: h.name ?? "" };
  if (st === "live" || ts > 0) return { status: "live", title: h.name ?? "" };
  return { status: "down", title: h.name ?? "" };
}
export function freshnessMap(rows: SourceHealthLike[]): Map<string, FreshDot> {
  const m = new Map<string, FreshDot>();
  for (const r of rows) m.set(r.name ?? "", freshOf(r));
  return m;
}
