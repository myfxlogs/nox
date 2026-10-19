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
import { client } from "../api";

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
  // F5：渲染序=watchlist 后端配置序（「排序全后端」——禁前端 sort）；
  // canonical→序位映射，外帧按到达序插尾。
  const [orderIdx, setOrderIdx] = useState<Map<string, number>>(new Map());
  const seqRef = useRef<Map<string, number>>(new Map());
  const seqNext = useRef(0);
  const loadWatchlist = () => {
    void client.getWatchlist({}).then((w) => {
      setOrderIdx(new Map(w.items.map((x, i) => [x.canonical, i] as const)));
    }).catch(() => {});
  };
  useEffect(loadWatchlist, []);
  // 涨跌闪（arb QuoteStrip 原件移植）：上一帧 bid/ask 基线 diff→up/down class 400ms。
  // 基线无条件更新（arb R3 顺手项——防回撤帧方向偶误）。
  const prevRef = useRef<Map<string, { bid: number; ask: number }>>(new Map());
  const [flash, setFlash] = useState<Map<string, "up" | "down">>(new Map());
  useEffect(() => {
    const next = new Map<string, "up" | "down">();
    for (const [k, f] of Object.entries(q)) {
      const prev = prevRef.current.get(k);
      if (prev && (prev.bid !== f.bid || prev.ask !== f.ask)) {
        // F6：mid 判据（bid+ask 和比较——bid↑ask↓ 价差变宽帧方向不再偶误）。
        next.set(k, f.bid + f.ask > prev.bid + prev.ask ? "up" : "down");
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
        setQ((m) => {
          const k = `${f.venue}|${f.symbol}`;
          if (!seqRef.current.has(k)) seqRef.current.set(k, seqNext.current++);
          return { ...m, [k]: f };
        });
      }),
    [],
  );
  // F5：帧序=watchlist canonical 序位→无配置帧按到达序插尾；venue 组序=
  // 组内最小序位（组标签仍帧域驱动，排序源全后端零自定）。
  const posOf = (f: Quote) => {
    const i = orderIdx.get(f.symbol);
    return i !== undefined ? i : 1e9 + (seqRef.current.get(`${f.venue}|${f.symbol}`) ?? 0);
  };
  const venues = [...new Set(Object.values(q).map((f) => f.venue))].sort(
    (a, b) =>
      Math.min(...Object.values(q).filter((f) => f.venue === a).map(posOf)) -
      Math.min(...Object.values(q).filter((f) => f.venue === b).map(posOf)),
  );
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
            .sort((a, b) => posOf(a) - posOf(b))
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
        onSaved={() => {
          setQ({});
          seqRef.current.clear();
          seqNext.current = 0;
          loadWatchlist();
        }}
      />
    </div>
  );
}
