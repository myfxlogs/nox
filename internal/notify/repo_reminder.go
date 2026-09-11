// D-270 周四逆回购操作提醒服务（S2 → R2 重构）。
// 独立 goroutine + time.Ticker(1h)，不接入 collect.Scheduler（Collector 接口返回 []fact.Fact 不匹配）。
// R2 变更：去 warnPct 硬阈值改 demandDepositRatePct=0.2% 活期基准；config 每小时从 DB 读（免重启）。
// 飞书提醒通过 InsertAlert(level="warn", msg) 走 D-264 alertHook 链路（main.go 已配置 warn/critical 自动推飞书）。
package notify

import (
	"context"
	"fmt"
	"log"
	"time"
)

// demandDepositRatePct 活期存款年化利率基准（%），GC001 > 此值即值得操作。
// 硬编码常量（非配置）——活期基准是客观值，不应由业主调。
const demandDepositRatePct = 0.2

// RepoRateRow 是提醒服务用的利率行（notify 包本地类型，避免 notify→pgstore 依赖）。
type RepoRateRow struct {
	Code    string
	Name    string
	RatePct float64
}

// RepoOpRow 是提醒服务用的操作行（notify 包本地类型）。
type RepoOpRow struct {
	ID             int64
	ReminderCount  int
	LastReminderAt *time.Time
	Status         string
}

// MaturedRepoTrade 是到期提醒用的 trade 行（notify 包本地类型，避免 notify→pgstore 依赖）。
type MaturedRepoTrade struct {
	ID             int64
	InstrumentCode string
	PrincipalCny   float64
}

// RepoConfigRow 是提醒服务用的配置行（notify 包本地类型，避免 notify→pgstore 依赖）。
type RepoConfigRow struct {
	IdleCny      float64
	MinReturnCny float64
	ReminderHour int
	Enabled      bool
}

// RepoReminderStore 提醒服务存储接口（pgstore 实现，避免 notify→pgstore 依赖）。
type RepoReminderStore interface {
	// R2 新增
	GetRepoConfig(ctx context.Context) (*RepoConfigRow, error)
	RepoRate30dPercentile(ctx context.Context, code string, currentRate float64) (float64, bool, error)
	// 保留
	LatestRepoRates(ctx context.Context) ([]RepoRateRow, error)
	RepoRate30dAvg(ctx context.Context, code string) (float64, bool, error)
	CNFICalendarIsBusinessDay(ctx context.Context, day time.Time) (bool, error)
	CreateRepoOp(ctx context.Context, week, code, name string, ratePct, expectedReturn float64, holdingDays int, signalSnapshot string, triggerDate time.Time) (int64, error)
	GetPendingRepoOp(ctx context.Context, week string) (*RepoOpRow, error)
	IncrementRepoReminder(ctx context.Context, id int64) error
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	// D-272 到期提醒
	ListMaturedRepoTrades(ctx context.Context, today time.Time) ([]MaturedRepoTrade, error)
	UpdateMaturityReminded(ctx context.Context, id int64) error
}

// AlertFn 是告警注入面（main 装配 st.InsertAlert 包装，复用 D-264 飞书链路）。
// 签名与 brokerMgr.AlertSink 一致（func(level, msg string)），错误在包装内 log 不阻断。
type AlertFn func(level, msg string)

// RepoReminderService 周四逆回购操作提醒（D-270 R2）。
// R2 变更：去掉 warnPct/idleCny/minReturn/reminderHour 字段，改为每小时 Check 时从 DB 读 config。
type RepoReminderService struct {
	store   RepoReminderStore
	alertFn AlertFn
	nowFn   func() time.Time
}

// NewRepoReminderService 创建提醒服务（R2：去掉 warnPct/idleCny/minReturn/reminderHour 参数）。
func NewRepoReminderService(store RepoReminderStore, alertFn AlertFn) *RepoReminderService {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600) // 容器无 tzdata 兜底
	}
	return &RepoReminderService{
		store:   store,
		alertFn: alertFn,
		nowFn:   func() time.Time { return time.Now().In(loc) },
	}
}

// SetNowFn 替换当前时间函数（测试注入周四/周五/周一等）。
func (s *RepoReminderService) SetNowFn(fn func() time.Time) { s.nowFn = fn }

// Run 启动 ticker 循环（main.go 用 safego.Go 调用）。
// 每小时触发一次 Check；ctx 取消时退出。
func (s *RepoReminderService) Run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	// 启动时立即检查一次（不等第一个 tick）。
	if err := s.Check(ctx); err != nil {
		log.Printf("[repo_reminder] check: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Check(ctx); err != nil {
				log.Printf("[repo_reminder] check: %v", err)
			}
		}
	}
}

// Check 是 ticker 调用入口（每小时一次）。
// R2：reminderHour 从 DB config 读（每小时刷新，免重启）。
// D-272：checkMaturity 在所有门控之前调用，与 Enabled/ReminderHour/weekday 完全解耦。
// 非目标时段/不满足条件 → return nil（空操作）。
func (s *RepoReminderService) Check(ctx context.Context) error {
	now := s.nowFn()
	// 1. 读 DB config（每小时刷新）
	cfg, err := s.store.GetRepoConfig(ctx)
	if err != nil {
		return fmt.Errorf("get repo config: %w", err)
	}

	// D-272 到期提醒：在所有 D-270 门控之前调用，完全解耦。
	// 到期是客观事件，不依赖 Enabled/ReminderHour/weekday。
	if err := s.checkMaturity(ctx, now); err != nil {
		log.Printf("[repo_reminder] maturity check: %v", err)
	}

	// 2. D-270 周四提醒门控（不影响到期提醒）
	if !cfg.Enabled || cfg.IdleCny <= 0 {
		return nil
	}
	// 3. 未到提醒时段 → return nil
	if now.Hour() < cfg.ReminderHour {
		return nil
	}
	wd := now.Weekday()
	switch wd {
	case time.Thursday:
		return s.checkThursday(ctx, now, cfg)
	case time.Friday, time.Monday:
		return s.checkReRemind(ctx, now)
	default:
		return nil // 非目标日
	}
}

// checkThursday 周四路径：创建机会 + 首次提醒（设计 §3.1 步骤 4-8）。
func (s *RepoReminderService) checkThursday(ctx context.Context, now time.Time, cfg *RepoConfigRow) error {
	// 5. 非工作日 → return nil
	biz, err := s.store.CNFICalendarIsBusinessDay(ctx, now)
	if err != nil {
		log.Printf("[repo_reminder] calendar: %v（保守跳过）", err)
		return nil
	}
	if !biz {
		return nil
	}
	// 6. GC001 最新利率 <= 活期基准（0.2%）→ return nil
	rates, err := s.store.LatestRepoRates(ctx)
	if err != nil {
		return fmt.Errorf("latest rates: %w", err)
	}
	var gc001 *RepoRateRow
	for i := range rates {
		if rates[i].Code == "204001" {
			gc001 = &rates[i]
			break
		}
	}
	if gc001 == nil || gc001.RatePct <= demandDepositRatePct {
		return nil
	}
	// 7. 预期收益 < 最小收益 → return nil
	holdingDays := 3 // GC001 周四=3 天利息（T+1 结算享周末）
	expectedReturn := gc001.RatePct * cfg.IdleCny * float64(holdingDays) / 365
	if expectedReturn < cfg.MinReturnCny {
		return nil
	}
	// 8. 本周未确认 → 创建/再提醒
	week := isoWeek(now)
	pending, err := s.store.GetPendingRepoOp(ctx, week)
	if err != nil {
		return fmt.Errorf("get pending: %w", err)
	}
	// 分位数（展示用，不阻塞）
	pct, _, _ := s.store.RepoRate30dPercentile(ctx, "204001", gc001.RatePct)
	avg30d, _, _ := s.store.RepoRate30dAvg(ctx, "204001")
	if pending == nil {
		// 无 pending → 创建机会（signal_snapshot=JSON 快照，含分位数）
		snap := fmt.Sprintf(`{"GC001":%.2f,"avg30d":%.2f,"pct30d":%.0f,"idle":%.0f,"expected":%.2f}`,
			gc001.RatePct, avg30d, pct, cfg.IdleCny, expectedReturn)
		if _, err := s.store.CreateRepoOp(ctx, week, "204001", "GC001",
			gc001.RatePct, expectedReturn, holdingDays, snap, now); err != nil {
			// 唯一约束冲突=本周已有 op（已确认 operated/skipped）→ 静默跳过；
			// 其他错误=优雅降级（reminder 服务不应因 DB 错误崩溃）。
			log.Printf("[repo_reminder] create repo op: %v（可能本周已确认）", err)
			return nil
		}
		s.sendReminder(ctx, gc001.RatePct, avg30d, pct, expectedReturn, cfg.IdleCny, 1)
		return nil
	}
	// 有 pending 且今天未提醒 → 再提醒
	if pending.LastReminderAt != nil && sameDay(*pending.LastReminderAt, now) {
		return nil // 今天已提醒
	}
	if err := s.store.IncrementRepoReminder(ctx, pending.ID); err != nil {
		return fmt.Errorf("increment reminder: %w", err)
	}
	s.sendReminder(ctx, gc001.RatePct, avg30d, pct, expectedReturn, cfg.IdleCny, pending.ReminderCount+1)
	return nil
}

// checkReRemind 周五/周一再提醒路径（设计 §3.1 步骤 9-11）。
func (s *RepoReminderService) checkReRemind(ctx context.Context, now time.Time) error {
	// 9. 非工作日 → return nil
	biz, err := s.store.CNFICalendarIsBusinessDay(ctx, now)
	if err != nil {
		log.Printf("[repo_reminder] calendar: %v（保守跳过）", err)
		return nil
	}
	if !biz {
		return nil
	}
	// 10. 无 pending → return nil
	week := isoWeek(now)
	pending, err := s.store.GetPendingRepoOp(ctx, week)
	if err != nil {
		return fmt.Errorf("get pending: %w", err)
	}
	if pending == nil {
		return nil
	}
	// 11. 今天已提醒 → return nil；否则 IncrementRepoReminder + InsertAlert
	if pending.LastReminderAt != nil && sameDay(*pending.LastReminderAt, now) {
		return nil
	}
	if err := s.store.IncrementRepoReminder(ctx, pending.ID); err != nil {
		return fmt.Errorf("increment reminder: %w", err)
	}
	// 再提醒不查利率（设计 §3.1 注：利率可能已变，但既然周四已判定值得操作，pending 未确认就继续提醒）
	s.sendReRemind(ctx, pending.ReminderCount+1)
	return nil
}

// sendReminder 发飞书提醒（InsertAlert → alertHook → 飞书）。
// R2：消息含 30 日分位数。
func (s *RepoReminderService) sendReminder(ctx context.Context, ratePct, avg30d, percentile, expectedReturn, idleCny float64, count int) {
	if s.alertFn == nil {
		return
	}
	msg := fmt.Sprintf("[CNY逆回购] 周四操作提醒 #%d\n\nGC001: %.2f%%（30日均值 %.2f%%，分位 %.0f%%）\n闲置资金: ¥%.0f\n预计收益: ¥%.2f（%.2f%% × ¥%.0f × 3天 / 365）\n\n回复格式：\n  操作: OK GC001 50000 2.85\n  跳过: SKIP 原因",
		count, ratePct, avg30d, percentile, idleCny, expectedReturn, ratePct, idleCny)
	s.alertFn("warn", msg)
}

// sendReRemind 发再提醒（周五/周一，不查利率）。
func (s *RepoReminderService) sendReRemind(ctx context.Context, count int) {
	if s.alertFn == nil {
		return
	}
	msg := fmt.Sprintf("[CNY逆回购] 操作提醒 #%d（本周仍有待确认操作）\n\n回复格式：\n  操作: OK GC001 50000 2.85\n  跳过: SKIP 原因", count)
	s.alertFn("warn", msg)
}

// isoWeek 返回 ISO 周字符串 "2026-W37"。
func isoWeek(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

// sameDay 判断两个时间是否同一天（按上海时区）。
func sameDay(a, b time.Time) bool {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	a = a.In(loc)
	b = b.In(loc)
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
