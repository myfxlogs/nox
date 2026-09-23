// d419_book_test.go —— D-419：授权保底观测缺口收口（S2 缺席分级 + S3 monitor 簿哨兵，先红后绿）。
// S2：缺席分级三态——无键行→「universe 未产行」/Note 剔行→「行被剔除」/干净行→开仓不 warn。
// S3：簿哨兵三态——簿无行→warn 且开仓仍进行/簿有行→不 warn/查询 err→fail-open 不 warn 不 panic。
package scan

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d419ErrStore 包装真库注入 HasXAssetMonitorRow 查询错（S3 fail-open 分支）。
type d419ErrStore struct {
	*pgstore.Store
}

func (s d419ErrStore) HasXAssetMonitorRow(_ context.Context, _, _, _ string) (bool, error) {
	return false, errors.New("d419 injected monitor query error")
}

// d419AlertCount 某级告警含子串的计数。
func d419AlertCount(t *testing.T, s *pgstore.Store, sub string) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '%'||$1||'%'`, sub).Scan(&n); err != nil {
		t.Fatalf("count alerts %q: %v", sub, err)
	}
	return n
}

// TestD419_MissGradingUniverseAbsent S2 态①：universe 未产行 → warn 含「universe 未产行」。
func TestD419_MissGradingUniverseAbsent(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "D419AUSD", "3001157:mt5", "D419AUSDT", "okx:perp", false)
	if _, _, err := StepPaperCfdCarry(ctx, s, nil, time.Now(), d410Opts()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if n := d419AlertCount(t, s, "universe 未产行"); n != 1 {
		t.Fatalf("want 1「universe 未产行」warn, got %d", n)
	}
	if n := d419AlertCount(t, s, "行被剔除"); n != 0 {
		t.Fatalf("不应含「行被剔除」, got %d", n)
	}
}

// TestD419_MissGradingDirty S2 态②：行在但 Note!=""（suspect 剔出）→ warn 含「行被剔除」+note。
func TestD419_MissGradingDirty(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "D419BUSD", "3001157:mt5", "D419BUSDT", "okx:perp", false)
	rows := []UniverseRow{d374Row("D419B", "3001157", "okx", "long_cfd_short_perp", 0, "suspect: funding 107.8%")}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("脏行不得开仓，opened=%v", opened)
	}
	if n := d419AlertCount(t, s, "行被剔除:suspect: funding 107.8%"); n != 1 {
		t.Fatalf("want 1「行被剔除」warn 带 note, got %d", n)
	}
}

// TestD419_MissGradingClean S2 态③：行干净 → 正常开仓，无缺席/被剔 warn。
func TestD419_MissGradingClean(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "D419CUSD", "3001157:mt5", "D419CUSDT", "okx:perp", false)
	rows := []UniverseRow{d374Row("D419C", "3001157", "okx", "long_cfd_short_perp", 2.0, "")}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts())
	if err != nil || len(opened) != 1 {
		t.Fatalf("干净行应开仓：opened=%v err=%v", opened, err)
	}
	if n := d419AlertCount(t, s, "宇宙行缺席"); n != 0 {
		t.Fatalf("不应有缺席 warn, got %d", n)
	}
}

// TestD419_MonitorBookAbsent S3 态①：簿三元组无行 → warn 出现且开仓仍进行（不阻断）。
func TestD419_MonitorBookAbsent(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "D419DUSD", "3001157:mt5", "D419DUSDT", "okx:perp", false)
	rows := []UniverseRow{d374Row("D419D", "3001157", "okx", "long_cfd_short_perp", 2.0, "")}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts())
	if err != nil || len(opened) != 1 {
		t.Fatalf("簿缺席不阻断开仓：opened=%v err=%v", opened, err)
	}
	if n := d419AlertCount(t, s, "monitor 簿缺席"); n != 1 {
		t.Fatalf("want 1 monitor 簿 warn, got %d", n)
	}
}

// TestD419_MonitorBookPresent S3 态②：簿有行 → 不 warn。
func TestD419_MonitorBookPresent(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "D419EUSD", "3001157:mt5", "D419EUSDT", "okx:perp", false)
	// pair 键=消费端口径 paperCfdPairKey("D419E")="D419EUSDT-D419E"；venue=okx:perp×3001157:mt5
	d410SeedMonitor(t, s, "D419EUSDT-D419E", time.Now().Add(-time.Minute), 5.0)
	rows := []UniverseRow{d374Row("D419E", "3001157", "okx", "long_cfd_short_perp", 2.0, "")}
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if n := d419AlertCount(t, s, "monitor 簿缺席"); n != 0 {
		t.Fatalf("簿有行不应 warn, got %d", n)
	}
}

// TestD419_MonitorBookQueryErr S3 态③：查询失败 fail-open——不 warn 不 panic、step 正常返回。
func TestD419_MonitorBookQueryErr(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d410SeedCombo(t, s, "D419FUSD", "3001157:mt5", "D419FUSDT", "okx:perp", false)
	rows := []UniverseRow{d374Row("D419F", "3001157", "okx", "long_cfd_short_perp", 2.0, "")}
	opened, _, err := StepPaperCfdCarry(ctx, d419ErrStore{s}, rows, time.Now(), d410Opts())
	if err != nil || len(opened) != 1 {
		t.Fatalf("查询错应 fail-open 且开仓仍进行：opened=%v err=%v", opened, err)
	}
	if n := d419AlertCount(t, s, "monitor 簿缺席"); n != 0 {
		t.Fatalf("查询错不得误报, got %d", n)
	}
}
