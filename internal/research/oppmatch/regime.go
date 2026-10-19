// regime.go —— D-625 P2：regime 标签件+armed 组合前提塌自盯。
// ③三指标→calm|trending|volatile|illiquid 标签（armed 环读标签不适配不产机会）；
// ⑤armed 组合前提=簿行活性（死引擎病灶实证）——塌→warn+实施席令（暂停非 kill）。
// 判据=app_config 驱动（票禁线：禁硬编码——键缺→fail-closed 整轮跳过并 log）。
// 会话闸（D-170 共享 marketstate）：MT 会话已知且闭市→不产行属正常，
// 前提塌豁免（否则会话制品种每夜/每周末误报=锚死伪警同族病灶）；
// 会话表缺席=无证据不误赦，照常判。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/data/marketstate"
	"nox/internal/platform/alerttext"
	"nox/internal/store/pgstore"
)

const regimeWindow = 24 * time.Hour

// regimeCfg 判据集（app_config 装载；一键缺=不可用→整轮 fail-closed）。
type regimeCfg struct {
	volBps   float64
	trendBps float64
	minRows  float64
	staleMax time.Duration
}

// loadRegimeCfg 判据装载（fail-closed：键缺→nil 调用方跳过本轮）。
func loadRegimeCfg(ctx context.Context, st *pgstore.Store) *regimeCfg {
	get := func(key string) (float64, bool) {
		v, ok, err := st.AppConfigFloat(ctx, key)
		if err != nil || !ok {
			return 0, false
		}
		return v, true
	}
	vol, ok1 := get("regime_vol_bps")
	trend, ok2 := get("regime_trend_bps")
	minR, ok3 := get("regime_min_rows")
	staleMin, ok4 := get("prec_stale_max_min")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		log.Printf("[oppmatch] regime 判据配置缺（regime_* / prec_stale_max_min）——本轮 fail-closed 跳过")
		return nil
	}
	return &regimeCfg{volBps: vol, trendBps: trend, minRows: minR, staleMax: time.Duration(staleMin) * time.Minute}
}

// RegimeLabel 三指标→regime 标签（机读）。
func (c regimeCfg) RegimeLabel(st pgstore.XAssetSpreadStats) string {
	switch {
	case float64(st.Rows) < c.minRows:
		return "illiquid"
	case st.Stdev >= c.volBps:
		return "volatile"
	case st.Delta >= c.trendBps || st.Delta <= -c.trendBps:
		return "trending"
	default:
		return "calm"
	}
}

// comboBookTriple 组合→簿三元组键（pair=cexSym-mtSym 实据形；venue 键=
// combo 行 venue_key 原值（typed `bare:type` 已在库——禁重建硬编 type）。
func comboBookTriple(c pgstore.XAssetAllowRow) (pair, cex, mt, mtCanon string) {
	switch {
	case keydomain.IsMTKey(c.VenueBuy) && !keydomain.IsMTKey(c.VenueSell):
		return c.CanonicalSell + "-" + c.CanonicalBuy, c.VenueSell, c.VenueBuy, c.CanonicalBuy
	case keydomain.IsMTKey(c.VenueSell) && !keydomain.IsMTKey(c.VenueBuy):
		return c.CanonicalBuy + "-" + c.CanonicalSell, c.VenueBuy, c.VenueSell, c.CanonicalSell
	}
	return "", "", "", ""
}

// preconditionWatch 每拍 armed/trial 组合前提自盯（RunOnce 尾挂）：
// 前提=簿行活性（行龄>阈=塌）→warn+令；regime 标签读面机读（log+alert 注）。
func preconditionWatch(ctx context.Context, st *pgstore.Store) {
	cfg := loadRegimeCfg(ctx, st)
	if cfg == nil {
		return
	}
	combos, err := st.LoadXAssetAllowCombos(ctx, "real")
	if err != nil {
		return
	}
	sessTbl := marketstate.LoadSessionTable(ctx, st)
	now := time.Now()
	for _, c := range combos {
		pair, cexV, mtV, mtCanon := comboBookTriple(c)
		if cexV == "" || mtV == "" {
			continue
		}
		// 会话闸：MT 腿已知闭市→簿停产属正常，前提塌豁免；表缺席照常判。
		if open, known := sessTbl.OpenAt(keydomain.BareOf(mtV), mtCanon, now); known && !open {
			continue
		}
		stats, serr := st.XAssetSpreadStats(ctx, pair, cexV, mtV, now.Add(-regimeWindow))
		label := "unknown"
		if serr == nil && stats.Rows > 0 {
			label = cfg.RegimeLabel(stats)
		}
		if serr != nil {
			log.Printf("[oppmatch] 前提检验读面败 %s: %v（fail-closed 视同塌报）", pair, serr)
		}
		if stats.Rows == 0 || now.Sub(stats.LastTs) > cfg.staleMax {
			age := "无行"
			if !stats.LastTs.IsZero() {
				age = now.Sub(stats.LastTs).Round(time.Minute).String()
			}
			msg := fmt.Sprintf("[前提塌] %s %s→%s 簿行活性失（最新行龄 %s>%.0fmin——暂停级非 kill，恢复可回来；regime=%s）%s",
				pair, c.VenueBuy, c.VenueSell, age, cfg.staleMax.Minutes(), label,
				alerttext.SeatDispatchLine("决策", "前提塌处置（读 STATE 本行——venue 供源恢复 or 降级裁定）"))
			if aerr := st.InsertAlertDedup(ctx, "warn", msg, 6*time.Hour); aerr != nil {
				log.Printf("[oppmatch] 前提塌告警写败: %v", aerr)
			}
		}
	}
}
