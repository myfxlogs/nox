// derive.go —— binance/exness 数据拉取层（D-582 模板：类型/seam/venue fetch/陈旧守门）。
// 派生入参层在 derive_inputs.go（300 行棘轮拆件，纯搬移零逻辑变更）。

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// S2/S3/S5 读库三件套（模板=D-582 fetchBinanceGold；reuse-existing-stream-before-new-path）。

// goldStaleTicks ticks 陈旧守门（S5 定标：6h 实测行间隔 binance p100=118s/gate p99=74s/
// okx p99=61s——120s 覆盖正常节奏；flap 窗真停顿报错=应有行为，禁为少报错放宽）。
const goldStaleTicks = 120 * time.Second

// readGoldTicks 读最新 perp tick（venue×symbol 隔离；venue_type='perp' 过滤——perp/spot 行混存）。
func readGoldTicks(ctx context.Context, pool *pgxpool.Pool, venue, symbol string) (bid, ask float64, err error) {
	var ts time.Time
	// stale/无行 → *staleDataErr 类型化（上游分级簿面静止 vs 真断流）。
	err = pool.QueryRow(ctx, `
		SELECT bid, ask, ts FROM ticks
		WHERE venue=$1 AND venue_type='perp' AND symbol=$2
		ORDER BY ts DESC LIMIT 1
	`, venue, symbol).Scan(&bid, &ask, &ts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, &staleDataErr{kind: "ticks", venue: venue, symbol: symbol}
		}
		return 0, 0, fmt.Errorf("ticks %s: %w", symbol, err)
	}
	if age := time.Since(ts); age > goldStaleTicks {
		return 0, 0, &staleDataErr{kind: "ticks", venue: venue, symbol: symbol, age: age}
	}
	return bid, ask, nil
}

// goldStaleInterval funding_interval 守门——日轮/boot 级近静态写源定标 25h（D-591 R1：
// gate_contract_meta 日轮 Interval=24h，15m 守门部署即间歇 fail-closed 实证）。
const goldStaleInterval = 25 * time.Hour

// readGoldFunding 读 funding 年化%+interval 秒并换算：单期小数费率=annualPct×ivlSec/
// (365×100×86400)（消费端 ×periodsPerDay×365×100 还原年化同语义）、periodsPerDay=86400/ivlSec
// （S4：fundingInfo REST 退役，interval 真值已由 WS/日批落 facts）。funding 按 src 白名单锚定
// （WS 原生源+D-109 死流窗 REST 看门狗源）；
// interval 落点 src 异构（okx=okx_ws_v5/gate=gate_v4_contract_meta/binance=binance_ws_markprice）
// 故不过滤 src——venue×symbol 隔离已足够（KB cross-venue-data-borrowing-pollution 禁跨所借用）。
// 缺行/超龄 15min=error 不猜（stale-data-propagates-wrong-judgments）。
func readGoldFunding(ctx context.Context, pool *pgxpool.Pool, venue string, srcs []string, symbol string) (fundingRate float64, periodsPerDay int, err error) {
	var annualPct, ivlSec float64
	var fts, its time.Time
	err = pool.QueryRow(ctx, `
		SELECT value, ts FROM facts
		WHERE kind='funding' AND venue=$1 AND src=ANY($2) AND symbol=$3
		ORDER BY ts DESC LIMIT 1
	`, venue, srcs, symbol).Scan(&annualPct, &fts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, &staleDataErr{kind: "funding", venue: venue, symbol: symbol}
		}
		return 0, 0, fmt.Errorf("facts funding %s: %w", symbol, err)
	}
	if age := time.Since(fts); age > 15*time.Minute {
		return 0, 0, &staleDataErr{kind: "funding", venue: venue, symbol: symbol, age: age}
	}
	err = pool.QueryRow(ctx, `
		SELECT value, ts FROM facts
		WHERE kind='funding_interval' AND venue=$1 AND symbol=$2
		ORDER BY ts DESC LIMIT 1
	`, venue, symbol).Scan(&ivlSec, &its)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, &staleDataErr{kind: "funding_interval", venue: venue, symbol: symbol}
		}
		return 0, 0, fmt.Errorf("facts funding_interval %s: %w", symbol, err)
	}
	// interval=近静态元数据（gate 日轮+各所 boot/帧内写），守门按写源节奏定标 25h；
	// funding rate 动态数据守门 15m 不动（stale-data-propagates-wrong-judgments 两边各安其位）。
	if age := time.Since(its); age > goldStaleInterval {
		return 0, 0, &staleDataErr{kind: "funding_interval", venue: venue, symbol: symbol, age: age}
	}
	if ivlSec <= 0 {
		return 0, 0, fmt.Errorf("facts funding_interval %s: invalid ivlSec=%v", symbol, ivlSec)
	}
	if periodsPerDay = int(math.Round(86400 / ivlSec)); periodsPerDay <= 0 {
		return 0, 0, fmt.Errorf("facts funding_interval %s: periodsPerDay=%d", symbol, periodsPerDay)
	}
	return annualPct * ivlSec / (365 * 100 * 86400), periodsPerDay, nil
}

// fetchBinanceGold D-582 建/D-591 S4-S5 校准：symbol=binance 原生（=facts/ticks 键形）。
func fetchBinanceGold(ctx context.Context, pool *pgxpool.Pool, symbol string) (bid, ask, fundingRate float64, periodsPerDay int, err error) {
	if bid, ask, err = readGoldTicks(ctx, pool, "binance", symbol); err != nil {
		return 0, 0, 0, 0, err
	}
	// 修补：binance funding src 白名单——WS 原生+binance_rest_markprice（死流窗
	// REST 看门狗落点）。只认这两源：backfill/他 src 仍不借用（cross-source 纪律不变）。
	if fundingRate, periodsPerDay, err = readGoldFunding(ctx, pool, "binance", []string{"binance_ws_markprice", "binance_rest_markprice"}, symbol); err != nil {
		return 0, 0, 0, 0, err
	}
	return bid, ask, fundingRate, periodsPerDay, nil
}

// okxNativeToDBKey instId（XAUT-USDT-SWAP）→facts/ticks 键形（XAUTUSDT，与 okx_ws 落库同构）。
func okxNativeToDBKey(instID string) string {
	return strings.ReplaceAll(strings.TrimSuffix(instID, "-SWAP"), "-", "")
}

// gateNativeToDBKey contract（XAUT_USDT）→键形（XAUTUSDT，与 canonicalFromGate 同构）。
func gateNativeToDBKey(contract string) string { return strings.ReplaceAll(contract, "_", "") }

// fetchOkxGold D-591 S2：okx 腿读库（REST ticker+funding-rate 退役）。
func fetchOkxGold(ctx context.Context, pool *pgxpool.Pool, instID string) (bid, ask, fundingRate float64, periodsPerDay int, err error) {
	sym := okxNativeToDBKey(instID)
	if bid, ask, err = readGoldTicks(ctx, pool, "okx", sym); err != nil {
		return 0, 0, 0, 0, err
	}
	if fundingRate, periodsPerDay, err = readGoldFunding(ctx, pool, "okx", []string{"okx_ws_v5"}, sym); err != nil {
		return 0, 0, 0, 0, err
	}
	return bid, ask, fundingRate, periodsPerDay, nil
}

// fetchGateGold D-591 S3：gate 腿读库（REST tickers+contracts 退役）。
func fetchGateGold(ctx context.Context, pool *pgxpool.Pool, contract string) (bid, ask, fundingRate float64, periodsPerDay int, err error) {
	sym := gateNativeToDBKey(contract)
	if bid, ask, err = readGoldTicks(ctx, pool, "gate", sym); err != nil {
		return 0, 0, 0, 0, err
	}
	if fundingRate, periodsPerDay, err = readGoldFunding(ctx, pool, "gate", []string{"gate_ws_futures"}, sym); err != nil {
		return 0, 0, 0, 0, err
	}
	return bid, ask, fundingRate, periodsPerDay, nil
}

// latestExnessTick D-335R1 S1：读 Exness 指定符号最新 tick（XAUUSD 泛化参数化）。
// 行龄 >10min 或无行 → error（调用方该对 skip+warn，禁止顶替腿）。
func latestExnessTick(ctx context.Context, pool *pgxpool.Pool, venue, symbol string) (bid, ask float64, err error) {
	var ts time.Time
	err = pool.QueryRow(ctx, `
		SELECT bid, ask, ts FROM ticks WHERE venue=$1 AND symbol=$2 ORDER BY ts DESC LIMIT 1
	`, venue, symbol).Scan(&bid, &ask, &ts)
	if err != nil {
		return 0, 0, fmt.Errorf("latest tick: %w", err)
	}
	if time.Since(ts) > 10*time.Minute {
		return 0, 0, fmt.Errorf("stale tick age=%v > 10m", time.Since(ts).Round(time.Second))
	}
	return bid, ask, nil
}
