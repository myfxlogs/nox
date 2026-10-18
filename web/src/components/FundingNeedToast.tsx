// FundingNeedToast.tsx —— 资金注入提醒弹窗（arb 同名件借形，D-072）。
// onCase("fundingNeed") 流帧直收累积；置顶堆叠不自动消失（无 setTimeout——区别于
// 渐隐 toast）；同 key 去重防复活；点击跳持仓页。仅本会话可见（断线不回放不持久化）。
// venue 显示=alias 后端权威直给→空回落 venue key（NOX 无 splitVenue——双段回退链）。
import { useCallback, useEffect, useRef, useState } from "react";
import { onCase } from "../stream";
import type { FundingNeedNotice } from "../gen/dashboardv1/stream_pb";
import { fmtUsd } from "./fmt";

const keyOf = (n: FundingNeedNotice) => `${n.canonical}|${n.venueLong}|${n.venueShort}`;
const venueLabel = (vk: string, alias: string) => alias || vk;

export function FundingNeedToast() {
  const [items, setItems] = useState<FundingNeedNotice[]>([]);
  const seenRef = useRef(new Set<string>());

  useEffect(() => onCase("fundingNeed", (v) => {
    const n = v as FundingNeedNotice;
    const k = keyOf(n);
    if (seenRef.current.has(k)) return;
    seenRef.current.add(k);
    setItems((prev) => [n, ...prev]);
  }), []);

  const dismiss = useCallback((k: string) => {
    setItems((prev) => prev.filter((n) => keyOf(n) !== k));
  }, []);

  if (items.length === 0) return null;

  return (
    <div className="fund-need-stack" role="alert" aria-live="assertive">
      {items.map((n) => {
        const k = keyOf(n);
        return (
          <div key={k} className="fund-need-toast fund-need-clickable" role="alertdialog"
            title="点击查看持仓"
            onClick={() => { location.hash = "#/positions"; dismiss(k); }}>
            <div className="fund-need-head">
              <span className="fund-need-type">swap 套利资金不足</span>
              <button type="button" className="fund-need-close" aria-label="关闭提醒" title="关闭"
                onClick={(e) => { e.stopPropagation(); dismiss(k); }}>×</button>
            </div>
            <div className="fund-need-canonical">
              {n.canonical} {venueLabel(n.venueLong, n.aliasLong)}↔{venueLabel(n.venueShort, n.aliasShort)}
            </div>
            <div className="fund-need-reason">{n.reason}</div>
            <div className="fund-need-action">
              请注入资金{n.needUsd > 0 ? `（约需 ${fmtUsd(n.needUsd)}）` : ""}
            </div>
          </div>
        );
      })}
    </div>
  );
}
