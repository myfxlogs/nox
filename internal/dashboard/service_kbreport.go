// service_kbreport.go D-442 S4：GenerateKbReport 手动补跑 RPC（周 ticker 之外调试/验证入口）。
// client 走 resolveAiConfig（DB 优先 → env 回落），tools/knRoot 复用分析员装配。
package dashboard

import (
	"context"
	"fmt"
	"path/filepath"

	"arb/internal/analyst"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/kbreport"

	"connectrpc.com/connect"
)

// GenerateKbReport 手动触发一期 KB 元分析报告（同周报器同 Generate，独立限流不占前端配额）。
func (s *Service) GenerateKbReport(ctx context.Context, req *connect.Request[v1.GenerateKbReportRequest]) (*connect.Response[v1.GenerateKbReportResponse], error) {
	deps := s.analyst
	if deps == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("分析员未配置"))
	}
	client, err := s.resolveAiConfig(ctx)
	if err != nil {
		return nil, err
	}
	root := deps.knRoot
	if root == "" {
		root = analyst.KnRoot
	}
	path, err := kbreport.Generate(ctx, kbreport.Deps{
		Client:     client,
		Tools:      deps.tools,
		EntriesDir: filepath.Join(root, "docs/经验库/条目"),
		ReportDir:  filepath.Join(root, "docs/报告"),
		InboxPath:  filepath.Join(root, deps.notePath),
		Alert: func(level, msg string) {
			_ = s.store.InsertAlert(context.Background(), nil, level, msg)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("generate kb report: %w", err)
	}
	return connect.NewResponse(&v1.GenerateKbReportResponse{Path: path}), nil
}
