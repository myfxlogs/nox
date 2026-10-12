// Package storetest D-351 S1：DSN 测试共享 Store（包进程级单例）。
// 独立于 testutil 主包的原因：pgstore 自身的 in-package 测试引用本包会构成
// pgstore(test)→storetest→pgstore import cycle（Go 禁止）——故 pgstore 包内就地
// 单例（机制同款），scan/dashboard 等外部测试方消费本包。
package storetest

import (
	"context"
	"sync"
	"testing"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

var (
	mu    sync.Mutex
	cache = map[string]*pgstore.Store{}
)

// SharedStore D-351 S1：包进程级共享 Store——首次调用建池+一次 Migrate，同 DSN
// 重复调用返回同一指针（dial 数从每测试 O(n) 降进程级 O(1)；D-350 两轮复审假红的
// 连接层 amplifier=dashboard 14 文件 per-test pgstore.New）。
// 数据隔离不变：各测试既有前缀 DELETE/TRUNCATE 自持（包内串行不受池共享影响）；
// 池不随单个测试关闭（进程退出回收），故消费方禁再注册 t.Cleanup(st.Close)。
func SharedStore(t *testing.T, envVar string) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, envVar)
	mu.Lock()
	defer mu.Unlock()
	if st, ok := cache[dsn]; ok {
		return st
	}
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("SharedStore 建池失败: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("SharedStore 首次 Migrate 失败: %v", err)
	}
	cache[dsn] = st
	return st
}
