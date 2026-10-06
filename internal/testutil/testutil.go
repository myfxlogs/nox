// Package testutil 测试基础设施共享助手。
//
// TestDSN fail-closed 门禁：2026-08-25 生产 facts 表被测试 TRUNCATE 事故根因修复。
// 7 处测试助手原仅判「未设置→Skip」，不判「设置值指向生产」→ 施工方误指生产库时
// 测试直接清空生产表。TestDSN 统一为：未设置→Fatalf（D-207）/ 逃生门→Skip / db 名不含 _test→Fatalf 拒绝。
//
// D-207 加固：DSN 缺失从静默 SKIP 改为 loud-FAIL（「没跑」不得长得像「过了」=假绿之源，
// D-189/D-190 三轮返修实证）；DSN 规范化追加 lock_timeout options 参数
// （62 个 TRUNCATE 位点一处兜底，pgx 认 URL options 参数已实证）。
// v1.1 修订：不追加 pool_* 参数（pgx.Connect 单连接路径不认 pgxpool 专有参数）。
// D-351 三杠杆（测试连接稳定性）：S1 SharedStore 包进程级共享池（per-test 池=每包 dial
// 数百次是 D-350 两轮复审假红的连接层根因 amplifier）；S2 normalizeDSN 追加
// connect_timeout=5（连接类故障 120s 挂死→5s 快败）；S3 逐包库名（env 指向共享 *_test
// 基名时改写 <基名>_<包tag>+幂等建库——多包/多席并行零互踩，-p 1 从纪律降级为非必要；
// D-373 泛化：判定从 arb_test 精确名放宽为一切 _test 结尾基名，收编 service/exec 共享基名
// 漏网通道——本机并行跑时全消费方共库互踩；ci.yml 四显式建库行变冗余但无害零改动兼容）。
// D-016 席位隔离：D560 panic 实证=多席共享 arb_test_<包> 库竞争（非测试顺序依赖）——
// tag 再追加席位段 <包tag>_<seattag>（SeatTag 派生），两施工位并发跑测试各写各库；
// pgstore restore/migrate 固定名辅助库同席后缀（SeatTag 导出供其派生名额）。
package testutil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// defaultTestDSN 四个 TestDSN env 的缺省值（D-021：测试库独立实例 127.0.0.1:5434）——
// env 未设时回落缺省（基名固定映射），生产实例（5433）不再承载任何测试库。
// 缺省真跑不弱化 D-207（loud-FAIL 防「没跑像过了」的 SKIP）——测试照跑非跳过；
// 未知 envVar（无缺省映射）仍走原 Fatalf/Skip 口径。
var defaultTestDSN = map[string]string{
	"ARB_TEST_PG_DSN":           "postgres://arb:arb@127.0.0.1:5434/arb_test",
	"ARB_TEST_PG_SERVICE_DSN":   "postgres://arb:arb@127.0.0.1:5434/arb_service_test",
	"ARB_EXEC_TEST_PG_DSN":      "postgres://arb:arb@127.0.0.1:5434/arb_exec_test",
	"ARB_RECONCILE_TEST_PG_DSN": "postgres://arb:arb@127.0.0.1:5434/arb_reconcile_test",
}

// TestDSN 测试用 DSN 门禁：envVar 未设→缺省独立测试实例 5434（D-021；缺省映射见
// defaultTestDSN，未知 envVar 无缺省→Fatalf D-207 loud-FAIL / 显式逃生门
// ARB_TEST_ALLOW_SKIP_DSN=1 才 Skipf）；指向非测试库→Fatalf 拒绝。
// envVar 如 "ARB_TEST_PG_DSN" / "ARB_EXEC_TEST_PG_DSN"。
//
// 返回值经 DSN 规范化（追加而非覆盖，已有则跳过）：
//   - 无 options= → 追加 options=-c%20lock_timeout=5s（62 个 TRUNCATE 位点一处兜底，
//     pgx 认 URL options 参数已实证：SHOW lock_timeout=3s 实测通过）
//   - 追加 connect_timeout=5（D-351 S2：连接类故障从 pgx 回退链 120s 挂死变 5s 快败）
//   - db 名以 _test 结尾的共享基名（arb_test/arb_service_test/arb_exec_test/任意基名）→
//     改写 <基名>_<调用方包tag> 并幂等建库（D-351 S3 逐包隔离；D-373 泛化——此前仅 arb_test
//     精确名改写，service/exec 共享基名漏网=本机并行跑时全消费方共库 TRUNCATE 互踩；显式命名
//     的库（不以 _test 结尾）=调用方自担隔离责任。ci.yml 四显式建库行变冗余但无害，零改动兼容）
//
// D-207 v1.1 修订：不再追加 pool_max_conns/pool_min_conns——TestDSN 消费方含
// pgx.Connect（单连接）路径（internal/exec/execute_guard_test.go），pool_* 是
// pgxpool 专有参数，pgx.Connect 报 unrecognized configuration parameter。
// 行为中性：pgxpool 默认 defaultMaxConns=4 / min=0（pgx pool.go:19），与原追加值相同。
func TestDSN(t *testing.T, envVar string) string {
	t.Helper()
	dsn := os.Getenv(envVar)
	if dsn == "" {
		def, ok := defaultTestDSN[envVar]
		if !ok {
			if os.Getenv("ARB_TEST_ALLOW_SKIP_DSN") == "1" {
				t.Skipf("%s 未设置（ARB_TEST_ALLOW_SKIP_DSN=1 逃生门启用，自觉跳过 DB 测试）", envVar)
			}
			t.Fatalf("D-207：%s 未设置且无缺省映射（假绿之源：D-189/D-190 三轮返修实证「没跑」长得像「过了」）。设 %s=<测试库 DSN> 或显式 ARB_TEST_ALLOW_SKIP_DSN=1 自觉跳过", envVar, envVar)
		}
		dsn = def // D-021：缺省独立测试实例 5434（基名固定映射）
	}
	if !isTestDB(dsn) {
		t.Fatalf("%s 指向非测试库（db 名不含 _test），测试会 TRUNCATE 生产表，拒绝执行", envVar)
	}
	if isProdInstance(dsn) {
		t.Fatalf("D-091：%s 指向生产实例端口 %d（D-021 生产实例零测试库；5433 复燃 22 库实证——库名含 _test 不足以放行，实例级端口为硬闸），拒绝执行", envVar, prodInstancePort())
	}
	out := normalizeDSN(dsn)
	tag := callerPackageTag()
	if seat := seatTag(); seat != "" {
		if tag == "" {
			tag = seat
		} else {
			tag = tag + "_" + seat // D-016：席位段追加——多席并发同包测试各写各库
		}
	}
	if rewritten, changed := perPkgDSN(out, tag); changed {
		if err := ensureDB(rewritten); err != nil {
			t.Fatalf("D-351 S3 逐包建库失败（fail-closed 不吞）：%v", err)
		}
		out = rewritten
	}
	return out
}

// normalizeDSN 追加缺失的连接层参数（D-207 v1.1：不追加 pool 参数）。
//   - lock_timeout：无 options= 时追加 options=-c%20lock_timeout=5s（TRUNCATE 一处兜底）；
//     已有 options= 则跳过（不能二次追加，pgx 拒重名 query 参数语义）。
//   - connect_timeout=5（D-351 S2）：无该参数时追加——连接类故障（dial 超时/回退链）
//     从 OS 默认 ~130s 挂死变 5s 快败+清晰报错；已有自定义值原样保留不覆盖。
//   - pool_max_conns/pool_min_conns 不追加——TestDSN 消费方含 pgx.Connect 单连接路径，
//     pool_* 是 pgxpool 专有参数。pgxpool 默认 defaultMaxConns=4/min=0 与原追加值相同（行为中性）。
func normalizeDSN(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	if !strings.Contains(dsn, "options=") {
		dsn += sep + "options=-c%20lock_timeout=5s"
		sep = "&"
	}
	if !strings.Contains(dsn, "connect_timeout=") {
		dsn += sep + "connect_timeout=5"
	}
	return dsn
}

// perPkgDSN D-351 S3 + D-373 泛化 + D-016 席位段：db 名以 _test 结尾的共享基名
// （arb_test / arb_service_test / arb_exec_test / 任意未来基名）→ 改写 <基名>_<tag>（逐包隔离）
// ——tag 可含席位段（<包tag>_<seattag>，TestDSN 组装），多席并发同包测试各写各库。
// 已 tag 名（arb_test_scan，后缀非 _test）幂等不再追加；显式命名的库（不以 _test 结尾，
// 如 arb_test_local）=调用方自担隔离责任；空 tag（调用方推导失败）原样不动。
// Postgres 标识符 63 字节上限：超长时先截 tag 段保基名完整（防服务端静默截断致建库/连库名错位）。
func perPkgDSN(dsn, tag string) (string, bool) {
	u, err := url.Parse(dsn)
	if err != nil || u.Path == "" || tag == "" {
		return dsn, false
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !strings.HasSuffix(name, "_test") {
		return dsn, false
	}
	full := name + "_" + tag
	if len(full) > 63 {
		allowed := 63 - len(name) - 1
		if allowed <= 0 {
			return dsn, false // 基名本身过长无隔离空间=原样（显式命名自担责任口径）
		}
		full = name + "_" + tag[:allowed]
	}
	u.Path = "/" + full
	return u.String(), true
}

// callerPackageTag 从调用方测试文件路径派生包 tag（如 scan/pgstore/dashboard/core）——
// 跳过 testutil 自身非测试帧（TestDSN/SharedStore 等包装链）后取首个外部帧。
func callerPackageTag() string {
	for skip := 1; skip < 16; skip++ {
		pc, file, _, ok := runtime.Caller(skip)
		if !ok {
			return ""
		}
		if fn := runtime.FuncForPC(pc); fn != nil &&
			strings.HasPrefix(fn.Name(), "arb/internal/testutil") &&
			!strings.HasSuffix(file, "_test.go") {
			continue // testutil/testutil-storetest 内部包装链帧（非测试源）
		}
		return sanitizeTag(filepath.Base(filepath.Dir(file)))
	}
	return ""
}

// sanitizeTag 包目录名 → 合法 DB 名后缀段（防怪目录名注入库名）。
func sanitizeTag(tag string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, tag)
}

// seatTagDerived cwd 仓根目录名缓存（go test 每测试二进制 cwd 恒定，派生一次）。
var (
	seatTagOnce    sync.Once
	seatTagDerived string
)

// SeatTag D-016 测试库多席隔离的会话标签（导出供固定名辅助库派生名额——pgstore
// restore/migrate 临时库同席位后缀）。env ARB_TEST_SEAT 显式优先；缺省从测试进程
// cwd（go test 置为包目录）上溯 go.mod 取仓根目录名：worktree 名即天然席位
// （/opt/arb-wt/d016-test-green → d016_test_green；/opt/arb 部署树 → arb）。
// 同席跨 run 同名复用（不按 run 增殖）；推导失败=空串（回落共享名，与既有行为等价）。
func SeatTag() string {
	if s := strings.TrimSpace(os.Getenv("ARB_TEST_SEAT")); s != "" {
		return sanitizeTag(s)
	}
	seatTagOnce.Do(func() {
		dir, err := os.Getwd()
		if err != nil {
			return
		}
		for i := 0; i < 32; i++ {
			if dir == "" || dir == "." || dir == "/" {
				return
			}
			if fi, serr := os.Stat(filepath.Join(dir, "go.mod")); serr == nil && !fi.IsDir() {
				seatTagDerived = sanitizeTag(filepath.Base(dir))
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return
			}
			dir = parent
		}
	})
	return seatTagDerived
}

// seatTag 内部别名（TestDSN 组装 tag 用）。
func seatTag() string { return SeatTag() }

// ensuredDBs 进程级已建库缓存（避免同库重复 CREATE DATABASE 往返）。
var ensuredDBs sync.Map

// ensureDB D-351 S3 幂等建库：连同名实例的 postgres 库 CREATE DATABASE；
// 已存在（SQLSTATE 42P04 duplicate_database，两进程并发首建竞态容忍）跳过；
// 其他错误原样上抛（F6 建库失败不吞）。成功后入进程缓存。
func ensureDB(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("ensureDB 解析 DSN: %w", err)
	}
	dbname := strings.TrimPrefix(u.Path, "/")
	if dbname == "" {
		return fmt.Errorf("ensureDB：DSN 无库名")
	}
	if _, ok := ensuredDBs.Load(dbname); ok {
		return nil
	}
	admin := *u
	admin.Path = "/postgres"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin.String())
	if err != nil {
		return fmt.Errorf("ensureDB 连 postgres 库: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbname}.Sanitize()); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42P04" {
			return fmt.Errorf("ensureDB CREATE DATABASE %s: %w", dbname, err)
		}
	}
	ensuredDBs.Store(dbname, true)
	return nil
}

// TicksPartitioner ticks 月分区创建能力（pgstore.Store.EnsureMonthPartition 同形；
// 接口抽取避免 testutil→pgstore 反向依赖——pgstore 包内测试引 testutil 会成环）。
type TicksPartitioner interface {
	EnsureMonthPartition(ctx context.Context, year, month int) error
}

// EnsureTicksPartition InsertTicks 前置：补当前+前+后三月分区（迁移只建历史月，
// 种子 ts 近月界时单月不够——D-016 机检修绿，23514 no partition 根因修复）。
func EnsureTicksPartition(t *testing.T, s TicksPartitioner) {
	t.Helper()
	now := time.Now().UTC()
	for _, m := range []time.Time{now.AddDate(0, -1, 0), now, now.AddDate(0, 1, 0)} {
		if err := s.EnsureMonthPartition(context.Background(), m.Year(), int(m.Month())); err != nil {
			t.Fatalf("ensure ticks partition %04d-%02d: %v", m.Year(), int(m.Month()), err)
		}
	}
}
