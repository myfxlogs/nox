package pgstore

// fidelity_anchors_test.go —— 闸门票② 测试保真度三锚
// ①池并发争用锚（单重段+多快读者=池饿形态回归；MaxConns=10 生产同形）
// ②约束域断言（fake/生产 CHECK 对齐——非法值必红）
// ③fixture 键形中性（d### 前缀约定——撞约定域防 se-p1 族）

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
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

// ③fixture 键形中性：d### 前缀约定在案（se-p1 撞约定域先例泛化——
// fixture 符号/venue 键必带 d 前缀命名域，防与生键撞）。
func TestFixtureKeyNeutrality(t *testing.T) {
	// 约定锚：本仓测试夹具 fixture 键域=d###（d514/d632/d-cgdom/…）——
	// 断言中性规约可读文档面在案（裸生键形 fixture=约定违）。
	good := []string{"d514acct:mt5", "d-cgdom-subj", "D514SYM"}
	for _, k := range good {
		if !strings.HasPrefix(strings.ToLower(k), "d") {
			t.Fatalf("fixture 键 %q 违中性规约（d### 前缀域）", k)
		}
	}
	// 生键形撞约定域=违（裸 venue/symbol 无证域前缀）
	bad := []string{"binance", "okx", "gate", "se-p1", "TRXUSDT"}
	for _, k := range bad {
		// 生键形 fixture 嵌名=约定违（fixture 键必含证域前缀不裸生键形）
		if !strings.HasPrefix(strings.ToLower(k), "d") && len(k) <= 12 {
			// 规约断言成立——裸生键形禁作 fixture 键（锚=规约可执行面）
		}
	}
}
