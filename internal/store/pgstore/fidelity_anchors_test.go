package pgstore

// fidelity_anchors_test.go —— 闸门票② 测试保真度三锚
// ①池并发争用锚（单重段+多快读者=池饿形态回归；MaxConns=10 生产同形）
// ②约束域断言（fake/生产 CHECK 对齐——非法值必红）
// ③fixture 键形中性（d### 前缀约定——撞约定域防 se-p1 族）

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"nox/internal/data/keydomain"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ①池并发争用：单重聚合×8 快读者并发——全过不饿不锁（D-636 实证形态回归）。
func TestPoolContentionFastReaders(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 9)
	// 单重段（跨 facts 大窗聚合——生产 true_net 级同形重查）
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 3; i++ {
			var n int
			if err := s.pool.QueryRow(ctx,
				`SELECT COUNT(*) FROM facts WHERE ts > now()-interval '30 days'`).Scan(&n); err != nil {
				errCh <- err
				return
			}
		}
	}()
	// 8 快读者（逐请求小查——争用谱面）
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				var v int
				if err := s.pool.QueryRow(ctx, `SELECT 1`).Scan(&v); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("争用面败（池饿/死锁形态回归）: %v", err)
	}
}

// ②约束域断言：fin_condition_gap.status 非法值必红（CHECK 域——
// fakeStore 收任意值假绿面用真约束锚钉死，alerts level 同族）。
func TestConstraintDomainConditionGapStatus(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 合法域行过（open/requested/fulfilled/waived）
	for _, st := range []string{"open", "requested", "fulfilled", "waived"} {
		var id int64
		if err := s.pool.QueryRow(ctx,
			`INSERT INTO fin_condition_gap (template_id, kind, subject, status, request_text)
			 VALUES ('d-cgdom','constraint','d-cgdom-`+st+`',$1,'t') RETURNING id`, st).Scan(&id); err != nil {
			t.Fatalf("合法域 status=%s 应过: %v", st, err)
		}
	}
	// 非法域必红（CHECK 拒）
	_, err := s.pool.Exec(ctx,
		`INSERT INTO fin_condition_gap (template_id, kind, subject, status, request_text)
		 VALUES ('d-cgdom','constraint','d-cgdom-bad','bogus_status','t')`)
	if err == nil {
		t.Fatal("非法 status 应被 CHECK 约束拒（约束域断言锚）")
	}
	s.pool.Exec(ctx, `DELETE FROM fin_condition_gap WHERE subject LIKE 'd-cgdom-%'`)
}

// ③错键形真边界：keydomain.Parse 无 type 段=裸键如实回（非错但语义边界在案）+
// 写面裸键列 CHECK 拒复合形（bsm_venue_bare_chk：venue !~ '%:%'）——
// 「:perp」复合形误入裸 venue 列=键域错位第 N 例同族真拒锚。
func TestWrongKeyformRejectedAtBoundary(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 边界①：解析层——裸键 Type=”（无 type 段=写面谓词 venue_key 分支禁用判据）
	if k := keydomain.Parse("badkey"); k.Type != "" {
		t.Fatalf("Parse(badkey) Type 应为空（裸键无 type 段）got %q", k.Type)
	}
	if k := keydomain.Parse("binance:perp"); k.Type != "perp" || k.Venue != "binance" {
		t.Fatalf("Parse 复合形域乱: %+v", k)
	}
	// 边界②：写面真 CHECK——复合形 venue 误入裸键列必拒
	// （broker_accounts_venue_bare_chk：venue !~ '%:%'；FK 零干扰=venue 主键列）。
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO broker_accounts (venue, host, port, password, venue_type)
		 VALUES ('badkey:perp', 'h', 443, 'x', 'mt5')`); err == nil {
		s.pool.Exec(ctx, `DELETE FROM broker_accounts WHERE venue='badkey:perp'`)
		t.Fatal("复合形 venue 写裸键列应被 broker_accounts_venue_bare_chk 拒")
	}
	// 对版：裸键形过。
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO broker_accounts (venue, host, port, password, venue_type)
		 VALUES ('dkeyformacct', 'h', 443, 'x', 'mt5')`); err != nil {
		t.Fatalf("裸键形应过: %v", err)
	}
	s.pool.Exec(ctx, `DELETE FROM broker_accounts WHERE venue='dkeyformacct'`)
}

// ④死锁对抗构形：MaxConns=1 小池+持连未还+第二连短 ctx 请求→
// 断言干净 ctx 超时败非悬挂（wedge 形态被逮——生产 MaxConns=10
// 持连跨 Acquire 死锁构形的可控复刻）。
func TestPoolWedgeDeadline(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	// 池 MaxConns=10——先占满 10 连构 wedge（持连不还），再短 ctx 第 11 连请求。
	held := make([]*pgxpool.Conn, 0, 9)
	for i := 0; i < 9; i++ {
		c, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
		held = append(held, c)
	}
	defer func() {
		for _, c := range held {
			c.Release()
		}
	}()
	// 第 11 连短 ctx——若悬挂=测试超时被杀（必红判据=干净 deadline 返回）。
	short, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err = s.pool.Acquire(short)
	if err == nil {
		t.Fatal("池满时第 11 连应 ctx 超时败——悬挂=死锁形态未逮")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("应干净 deadline 败: %v", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("超时返回拖 %v（悬挂嫌疑）", el)
	}
}
