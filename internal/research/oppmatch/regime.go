// regime.go —— D-625 P2：regime 标签件+armed 组合前提塌自盯。
// ③三指标→calm|trending|volatile|illiquid 标签（armed 环读标签不适配不产机会）；
// ⑤armed 组合前提=簿行活性（死引擎病灶实证）——塌→warn+实施席令（暂停非 kill）。
package oppmatch

import (
	"context"
	"fmt"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/platform/alerttext"
	"nox/internal/store/pgstore"
)

const (
	regimeWindow   = 24 * time.Hour
	regimeVolBps   = 8.0 // stdev 阈（bps——volatile 判定）
	regimeTrendBps = 6.0 // |delta| 阈（bps——trending 判定）
	regimeMinRows  = 12  // 流动性代理：窗内行数<12=illiquid
	precStaleMax   = 15 * time.Minute
)

// RegimeLabel 三指标→regime 标签（机读）。
func RegimeLabel(st pgstore.XAssetSpreadStats) string {
	switch {
	case st.Rows < regimeMinRows:
		return "illiquid"
	case st.Stdev >= regimeVolBps:
		return "volatile"
	case st.Delta >= regimeTrendBps || st.Delta <= -regimeTrendBps:
		return "trending"
	default:
		return "calm"
	}
}

// comboBookTriple 组合→簿三元组键（pair=cexSym-mtSym 实据形；venue 键=
// typed key `bare:type`——MT 腿由 keydomain.IsMTKey 判非裸名猜）。
func comboBookTriple(c pgstore.XAssetAllowRow) (pair, cex, mt string) {
	switch {
	case keydomain.IsMTKey(c.VenueBuy) && !keydomain.IsMTKey(c.VenueSell):
		return c.CanonicalSell + "-" + c.CanonicalBuy,
			keydomain.Join(keydomain.BareOf(c.VenueSell), "perp"),
			keydomain.Join(keydomain.BareOf(c.VenueBuy), "mt5")
	case keydomain.IsMTKey(c.VenueSell) && !keydomain.IsMTKey(c.VenueBuy):
		return c.CanonicalBuy + "-" + c.CanonicalSell,
			keydomain.Join(keydomain.BareOf(c.VenueBuy), "perp"),
			keydomain.Join(keydomain.BareOf(c.VenueSell), "mt5")
	}
	return "", "", ""
}

// preconditionWatch 每拍 armed/trial 组合前提自盯（RunOnce 尾挂）：
// 前提=簿行活性（行龄>15min=塌）→warn+令；regime 标签读面机读（log+alert 注）。
func preconditionWatch(ctx context.Context, st *pgstore.Store) {
	combos, err := st.LoadXAssetAllowCombos(ctx, "real")
	if err != nil {
		return
	}
	for _, c := range combos {
		pair, cexV, mtV := comboBookTriple(c)
		if cexV == "" || mtV == "" {
			continue
		}
		stats, serr := st.XAssetSpreadStats(ctx, pair, cexV, mtV, time.Now().Add(-regimeWindow))
		label := "unknown"
		if serr == nil && stats.Rows > 0 {
			label = RegimeLabel(stats)
		}
		if serr == nil && (stats.Rows == 0 || time.Since(stats.LastTs) > precStaleMax) {
			age := "无行"
			if !stats.LastTs.IsZero() {
				age = time.Since(stats.LastTs).Round(time.Minute).String()
			}
			msg := fmt.Sprintf("[前提塌] %s %s→%s 簿行活性失（最新行龄 %s>15min——暂停级非 kill，恢复可回来；regime=%s）%s",
				pair, c.VenueBuy, c.VenueSell, age, label,
				alerttext.SeatDispatchLine("决策", "前提塌处置（读 STATE 本行——venue 供源恢复 or 降级裁定）"))
			if aerr := st.InsertAlertDedup(ctx, "warn", msg, 6*time.Hour); aerr != nil {
				_ = aerr
			}
		}
	}
}
