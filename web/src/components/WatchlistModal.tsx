// WatchlistModal.tsx —— 自选报价条编辑浮层（arb-archive OverviewPageWatchlistModal
// 借形重写，D-090 双维范式）：canonical 候选=ListLatestQuotes 打开时全量拉取
// 派生实时源（venueKey=venue:venueType 双维 chip）；已选清单=↑↓排序+✕删+venue
// chips（「全部」=清空 venues 限）。保存=SetWatchlist 全量替换（顺序即 sort）；
// 服务端 watchlistHitQuote 按单过滤流帧——保存即生效零额外接线。
// 纯展示面编辑：搜索过滤=展示面过滤非业务判据；候选源/已选序全后端字段直渲。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import { Modal } from "./Modal";
import { Chip } from "./Chip";
import { create } from "@bufbuild/protobuf";
import { WatchlistItemSchema } from "../gen/dashboardv1/watchlist_analyst_pb";
import type { WatchlistItem } from "../gen/dashboardv1/watchlist_analyst_pb";

export function WatchlistModal({
  open,
  onClose,
  onSaved,
}: {
  open: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [items, setItems] = useState<WatchlistItem[]>([]);
  // srcMap: canonical → 实时 venueKey 源集（ListLatestQuotes 派生）
  const [srcMap, setSrcMap] = useState<Map<string, string[]>>(new Map());
  const [query, setQuery] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    (async () => {
      try {
        const [w, lq] = await Promise.all([
          client.getWatchlist({}),
          client.listLatestQuotes({}),
        ]);
        setItems(w.items.map((x) => create(WatchlistItemSchema, { canonical: x.canonical, venues: [...x.venues] })));
        const m = new Map<string, Set<string>>();
        for (const q of lq.quotes) {
          const k = `${q.venue}:${q.venueType}`;
          const s = m.get(q.symbol) ?? new Set<string>();
          s.add(k);
          m.set(q.symbol, s);
        }
        setSrcMap(new Map([...m].map(([k, v]) => [k, [...v].sort()])));
        setErr("");
      } catch (e) {
        setErr(errText(e));
      }
    })();
  }, [open]);

  const picked = new Set(items.map((i) => i.canonical));
  const cands = [...srcMap.keys()]
    .filter((s) => !picked.has(s))
    .filter((s) => !query || s.toLowerCase().includes(query.toLowerCase()))
    .sort();

  const move = (i: number, d: -1 | 1) =>
    setItems((l) => {
      const n = [...l];
      const j = i + d;
      if (j < 0 || j >= n.length) return n;
      [n[i], n[j]] = [n[j], n[i]];
      return n;
    });
  const toggleVenue = (ci: number, vk: string) =>
    setItems((l) => {
      const n = [...l];
      const vs = n[ci].venues.includes(vk)
        ? n[ci].venues.filter((x) => x !== vk)
        : [...n[ci].venues, vk];
      n[ci] = create(WatchlistItemSchema, { canonical: n[ci].canonical, venues: vs });
      return n;
    });

  const save = async () => {
    setBusy(true);
    try {
      await client.setWatchlist({ items });
      onSaved();
      onClose();
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title="自选报价条编辑" width={640}>
      {err && <div className="err">{err}</div>}
      <input
        placeholder="搜索 canonical…"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        style={{ width: "100%", marginBottom: 8 }}
      />
      <h4>已选（{items.length}）</h4>
      <ul className="opp-cards" style={{ maxHeight: 260, overflowY: "auto" }}>
        {items.map((it, i) => (
          <li key={it.canonical} className="opp-card" style={{ cursor: "default" }}>
            <div className="opp-card-head">
              <b>{it.canonical}</b>
              {(srcMap.get(it.canonical) ?? []).map((vk) => (
                <span key={vk} onClick={() => toggleVenue(i, vk)} style={{ cursor: "pointer" }}>
                  <Chip tone={it.venues.length === 0 || it.venues.includes(vk) ? "good" : "neutral"}>
                    {vk}
                  </Chip>
                </span>
              ))}
              <span className="opp-card-friction">
                <a href="#" onClick={(e) => { e.preventDefault(); move(i, -1); }}>↑</a>{" "}
                <a href="#" onClick={(e) => { e.preventDefault(); move(i, 1); }}>↓</a>{" "}
                <a href="#" onClick={(e) => { e.preventDefault(); setItems((l) => l.filter((_, j) => j !== i)); }}>✕</a>
              </span>
            </div>
            {it.venues.length === 0 && <div className="muted" style={{ fontSize: 12 }}>全部源</div>}
          </li>
        ))}
      </ul>
      <h4>候选</h4>
      <ul className="opp-cards" style={{ maxHeight: 220, overflowY: "auto" }}>
        {cands.map((s) => (
          <li key={s} className="opp-card" style={{ cursor: "pointer" }}
            onClick={() => setItems((l) => [...l, create(WatchlistItemSchema, { canonical: s })])}>
            <div className="opp-card-head">
              <b>{s}</b>
              <span className="opp-card-who">{(srcMap.get(s) ?? []).join(" ")}</span>
              <span className="opp-card-friction">+加入</span>
            </div>
          </li>
        ))}
        {cands.length === 0 && <li className="muted">无匹配候选</li>}
      </ul>
      <div style={{ marginTop: 10, display: "flex", gap: 8 }}>
        <button disabled={busy} onClick={() => void save()}>保存（全量替换生效）</button>
        <button onClick={onClose}>取消</button>
      </div>
    </Modal>
  );
}
