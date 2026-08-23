package engine

import (
	"context"
	"math"
	"testing"

	"arb/internal/store/pgstore"
)

// fakeVerifyStore 测试用 verificationStore 实现。
type fakeVerifyStore struct {
	samples int
	ratio   float64
	fresh   bool
	err     error
	calls   int
	lastKey string
}

func (f *fakeVerifyStore) VerificationFor(ctx context.Context, vkey string, windowDays int) (int, float64, bool, error) {
	f.calls++
	f.lastKey = vkey
	return f.samples, f.ratio, f.fresh, f.err
}

// TestShouldPublish D-086 验证阶梯发布门。
func TestShouldPublish(t *testing.T) {
	o := &pgstore.Opportunity{Type: "CROSS_VENUE", VenueBuy: "binance:spot", VenueSell: "okx:perp"}

	t.Run("verifyStore nil → 全量发布（向后兼容）", func(t *testing.T) {
		e := &Engine{}
		if !e.shouldPublish(context.Background(), o) {
			t.Error("verifyStore nil 应全量发布")
		}
	})

	t.Run("V1 通过（samples≥minSamples, R≥minRatio, fresh）", func(t *testing.T) {
		e := &Engine{
			verifyStore:      &fakeVerifyStore{samples: 3, ratio: 0.85, fresh: true},
			verifyMinSamples: 2,
			verifyMinRatio:   0.7,
		}
		if !e.shouldPublish(context.Background(), o) {
			t.Error("V1 应发布")
		}
	})

	t.Run("V0 不发布（样本不足）", func(t *testing.T) {
		e := &Engine{
			verifyStore:      &fakeVerifyStore{samples: 1, ratio: 0.9, fresh: true},
			verifyMinSamples: 2,
			verifyMinRatio:   0.7,
		}
		if e.shouldPublish(context.Background(), o) {
			t.Error("V0（样本不足）不应发布")
		}
	})

	t.Run("V0 不发布（R 低于门槛）", func(t *testing.T) {
		e := &Engine{
			verifyStore:      &fakeVerifyStore{samples: 3, ratio: 0.5, fresh: true},
			verifyMinSamples: 2,
			verifyMinRatio:   0.7,
		}
		if e.shouldPublish(context.Background(), o) {
			t.Error("V0（R<门槛）不应发布")
		}
	})

	t.Run("V0 不发布（不新鲜）", func(t *testing.T) {
		e := &Engine{
			verifyStore:      &fakeVerifyStore{samples: 3, ratio: 0.9, fresh: false},
			verifyMinSamples: 2,
			verifyMinRatio:   0.7,
		}
		if e.shouldPublish(context.Background(), o) {
			t.Error("V0（不新鲜）不应发布")
		}
	})

	t.Run("V0 不发布（ratio=NaN）", func(t *testing.T) {
		e := &Engine{
			verifyStore:      &fakeVerifyStore{samples: 3, ratio: math.NaN(), fresh: true},
			verifyMinSamples: 2,
			verifyMinRatio:   0.7,
		}
		if e.shouldPublish(context.Background(), o) {
			t.Error("V0（ratio=NaN）不应发布")
		}
	})

	t.Run("查询失败 → fail-safe V0 不发布", func(t *testing.T) {
		e := &Engine{
			verifyStore:      &fakeVerifyStore{err: context.DeadlineExceeded},
			verifyMinSamples: 2,
			verifyMinRatio:   0.7,
		}
		if e.shouldPublish(context.Background(), o) {
			t.Error("查询失败应 fail-safe V0 不发布")
		}
	})

	t.Run("vkey 派生正确", func(t *testing.T) {
		fvs := &fakeVerifyStore{samples: 3, ratio: 0.85, fresh: true}
		e := &Engine{
			verifyStore:      fvs,
			verifyMinSamples: 2,
			verifyMinRatio:   0.7,
		}
		e.shouldPublish(context.Background(), o)
		want := "CROSS_VENUE:binance:spot:okx:perp"
		if fvs.lastKey != want {
			t.Errorf("vkey = %q, want %q", fvs.lastKey, want)
		}
	})
}
