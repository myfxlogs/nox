// QuoteStrip.tsx —— 总览实时报价条（S3′ 三件补：arb QuoteStrip 借形重写）。
// 数据=既有 StreamUpdates Quote 帧（stream.proto:22）——自持订阅建
// venue|symbol 最新帧表直渲；纯展示不喂判定、零聚合，符号/venue 集=
// 帧到达域驱动（禁硬编码清单）。零帧=如实缺态。
// 右端 UTC 钟=票面裁准的唯一合法 setInterval（非业务数据刷新）。
import { useEffect, useRef, useState } from "react";
import { onCase } from "../stream";
import type { Quote } from "../gen/dashboardv1/market_pb";
import { FreshDot as Dot } from "./FreshDot";
import { freshOf } from "./freshness";
import { WatchlistModal } from "./WatchlistModal";

// fmtPrice 保留 venue 原生精度——上限 12 位小数只是兜底不参与舍入，
// 千分位便于阅读（不设最小小数位、不补尾零）。
function fmtPrice(p: number): string {
  return p.toLocaleString("zh-CN", { maximumFractionDigits: 12 });
}

// UtcClock HH:MM:SS UTC——票面三件之一（展示件）。
function UtcClock() {
  const [t, setT] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setT(new Date()), 1000);
    return () => clearInterval(id);
  }, []);
  return <span className="quote-ts">{t.toISOString().slice(11, 19)} UTC</span>;
}

export function QuoteStrip() {
  const [q, setQ] = useState<Record<string, Quote>>({});
  const [edit, setEdit] = useState(false);
  // 涨跌闪（arb QuoteStrip 原件移植）：上一帧 bid/ask 基线 diff→up/down class 400ms。
  // 基线无条件更新（arb R3 顺手项——防回撤帧方向偶误）。
  const prevRef = useRef<Map<string, { bid: number; ask: number }>>(new Map());
  const [flash, setFlash] = useState<Map<string, "up" | "down">>(new Map());
  useEffect(() => {
    const next = new Map<string, "up" | "down">();
    for (const [k, f] of Object.entries(q)) {
      const prev = prevRef.current.get(k);
      if (prev && (prev.bid !== f.bid || prev.ask !== f.ask)) {
        next.set(k, f.bid > prev.bid || f.ask > prev.ask ? "up" : "down");
      }
      prevRef.current.set(k, { bid: f.bid, ask: f.ask });
    }
    if (next.size > 0) {
      setFlash(next);
      const t = setTimeout(() => setFlash(new Map()), 400);
      return () => clearTimeout(t);
    }
  }, [q]);
  useEffect(
    () =>
      onCase("quote", (v) => {
        const f = v as Quote;
        if (!f.venue || !f.symbol) return;
        setQ((m) => ({ ...m, [`${f.venue}|${f.symbol}`]: f }));
      }),
    [],
  );
  // venue 动态分组（帧域驱动）；组内符号按名排序=稳定排布非聚合。
  const venues = [...new Set(Object.values(q).map((f) => f.venue))].sort();
  return (
    <div className="quote-strip" aria-label="实时报价">
      {venues.length === 0 ? (
        <span className="muted">行情流待命——零帧如实</span>
      ) : (
        venues.flatMap((v) => [
          <span key={v} className="quote-venue">
            {v}
          </span>,
          ...Object.values(q)
            .filter((f) => f.venue === v)
            .sort((a, b) => a.symbol.localeCompare(b.symbol))
            .map((f) => (
              <span key={`${v}|${f.symbol}`} className="quote-tile">
                <b>{f.symbol}</b>
                <span className={`quote-bidask ${flash.get(`${v}|${f.symbol}`) ?? ""}`}>
                  <span className="num">
                    {fmtPrice(f.bid)}/{fmtPrice(f.ask)}
                  </span>
                </span>
                <Dot
                  dot={freshOf({
                    lastFactAt: f.time ? Number(f.time.seconds) * 1000 : 0,
                    name: f.symbol,
                  })}
                />
              </span>
            )),
        ])
      )}
      {/* watchlist 编辑钮（票面④——保存后清本地帧表：被过滤源的残帧不残存） */}
      <button
        style={{ fontSize: 11, padding: "1px 8px" }}
        title="自选报价条编辑"
        onClick={() => setEdit(true)}
      >
        自选✎
      </button>
      <UtcClock />
      <WatchlistModal
        open={edit}
        onClose={() => setEdit(false)}
        onSaved={() => setQ({})}
      />
    </div>
  );
}
