// intra_observe.go —— D-441 S3/S4：日内轨观测面（栅栏事件落库+critical 告警钩子+步进心跳）。
package scan

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// SetAlertFn 注入告警钩子（D-441 S3；level=critical 语义由注入方定）。
func (e *IntraEngine) SetAlertFn(f func(ctx context.Context, level, msg string)) { e.alertFn = f }

// D-441 S4：步进心跳（Step 成功完成时间，unixnano）。dashboard 经 IntraLastStepAt 读面。
var intraLastStep atomic.Int64

// IntraNoteStepDone 记录一次成功步进心跳。
func IntraNoteStepDone() { intraLastStep.Store(time.Now().UnixNano()) }

// IntraLastStepAt 最近一次成功步进时间（零值=未跑过）。
func IntraLastStepAt() time.Time {
	if ns := intraLastStep.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// emitEvent D-441 S3：栅栏事件落库 + critical 告警；两者失败均只记日志不断步进。
// 去重=当日同 kind 首触发才落+告（kill/circuit 持续态下每 5min 步进不重发=噪音纪律）。
func (e *IntraEngine) emitEvent(ctx context.Context, kind, detail string) {
	dup, err := e.st.HasIntraEventToday(ctx, kind)
	if err != nil {
		slog.Error("intra event dedup check fail", "kind", kind, "err", err)
		return
	}
	if dup {
		return
	}
	if err := e.st.InsertIntraEvent(ctx, kind, "", detail); err != nil {
		slog.Error("intra event insert fail", "kind", kind, "err", err)
		return
	}
	if e.alertFn != nil {
		e.alertFn(ctx, "critical", fmt.Sprintf("intra %s: %s", kind, detail))
	}
}
