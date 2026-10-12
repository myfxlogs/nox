package settlement

import "context"

// PostSettler D-032 ① 可选钩子：Deps 实现者提供时，结算行写入成功后回调。
// settlement 层不认识 ControlPlane/风控线——钩子在 autotrade 侧装配注入
// （import 方向 autotrade→settlement 不破）。回调错误只许内部消化（结算已落账不回滚）。
type PostSettler interface {
	PostSettled(ctx context.Context, posID int64)
}
