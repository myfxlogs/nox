// engine_cex_paper.go —— D-051①：CEX 纸面证据面 Allow 前候选流合流件（自
// engine.go 拆出，文件行数闸）。执行链候选仍走 ScanAll 后闸门，零改动。
package engine

import (
	"context"

	"nox/internal/data/bus"
	"nox/internal/research/detector"
)

// unfilteredScanner detector 侧 Allow 前流可选口（FundingHedgeDetector 实现）。
type unfilteredScanner interface {
	ScanUnfiltered(ctx context.Context, snapshot map[string]map[string]bus.Quote, fp detector.FactProvider) []detector.Candidate
}

// paperCexUnfiltered 纸面证据面消费 detector Allow 前候选流（ScanUnfiltered 绕
// 白名单+净阈，保 stale/isReal/funding 存在性）。D-555 S1：hook 按 canonical
// 30s 采样节拍合流（纸面研究采样非判定，D-004 P3 举证；OOM pprof 实证帧率级
// 调用→paper_spot_carry 行风暴，节拍保留）。
// R1②：同扫产全量快照给 cexPaperSnap（开/退/计息同源=同一 unfiltered 流）——
// hook 到期子集开闸，轮 pass 吃全量集（不因采样节拍残缺误判候选缺席）。
func (e *Engine) paperCexUnfiltered(ctx context.Context, snapshot map[string]map[string]bus.Quote) {
	if e.cexPaperHook == nil && e.cexPaperSnap == nil {
		return
	}
	var all, fhs []detector.Candidate
	seenCanon := map[string]bool{}
	for _, d := range e.detectors {
		u, ok := d.(unfilteredScanner)
		if !ok {
			continue
		}
		for _, c := range u.ScanUnfiltered(ctx, snapshot, e.facts) {
			if c.Type != detector.TypeFundingHedge || seenCanon[c.Canonical] {
				continue
			}
			seenCanon[c.Canonical] = true
			all = append(all, c)
			if e.paperHookDue(c.Canonical) {
				fhs = append(fhs, c)
			}
		}
	}
	if e.cexPaperSnap != nil {
		e.cexPaperSnap(all) // 每扫一覆（slice 交换非累积；nil 集同样写=缺席语义透传）
	}
	if len(fhs) > 0 && e.cexPaperHook != nil {
		e.cexPaperHook(ctx, fhs)
	}
}
