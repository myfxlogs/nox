// service_reports.go D-457 S4：复盘报告 RPC——GenerateRetroReport 手动触发 +
// ListReports/GetReport 前端报告页读取（docs/报告/*.md 只读面，basename 防穿越）。
package dashboard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/platform/analyst"
	"arb/internal/review/sysretro"

	"connectrpc.com/connect"
)

// reportDir 报告目录（knRoot+docs/报告；analyst 未配时回落默认根——报告页不要求 AI 配置）。
func (s *Service) reportDir() string {
	root := analyst.KnRoot
	if s.analyst != nil && s.analyst.knRoot != "" {
		root = s.analyst.knRoot
	}
	return filepath.Join(root, "docs/报告")
}

// GenerateRetroReport 手动触发一期复盘报告（kind=daily|weekly；同定时器同 Generate）。
func (s *Service) GenerateRetroReport(ctx context.Context, req *connect.Request[v1.GenerateRetroReportRequest]) (*connect.Response[v1.GenerateRetroReportResponse], error) {
	if s.analyst == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("分析员未配置"))
	}
	client, err := s.resolveAiConfig(ctx)
	if err != nil {
		return nil, err
	}
	root := s.analyst.knRoot
	if root == "" {
		root = analyst.KnRoot
	}
	deps := sysretro.Deps{
		Store:      s.store,
		Client:     client,
		Tools:      s.analyst.tools,
		ReportDir:  filepath.Join(root, "docs/报告"),
		InboxPath:  filepath.Join(root, s.analyst.notePath),
		StatePath:  filepath.Join(root, "docs/handoff/STATE.md"),
		RepoDir:    root,
		EntriesDir: filepath.Join(root, "docs/经验库/条目"),
	}
	var path string
	switch req.Msg.Kind {
	case "daily":
		path, err = sysretro.GenerateDaily(ctx, deps)
	case "weekly":
		path, err = sysretro.GenerateWeekly(ctx, deps)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("kind 须为 daily|weekly"))
	}
	if err != nil {
		return nil, fmt.Errorf("generate retro report: %w", err)
	}
	return connect.NewResponse(&v1.GenerateRetroReportResponse{Path: path}), nil
}

// ListReports 报告文件列表（name/size/mtime，mtime 倒序）。
func (s *Service) ListReports(ctx context.Context, _ *connect.Request[v1.ListReportsRequest]) (*connect.Response[v1.ListReportsResponse], error) {
	entries, err := os.ReadDir(s.reportDir())
	if err != nil {
		if os.IsNotExist(err) {
			return connect.NewResponse(&v1.ListReportsResponse{}), nil
		}
		return nil, fmt.Errorf("list reports: %w", err)
	}
	var out []*v1.ReportInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, &v1.ReportInfo{
			Name:       e.Name(),
			SizeBytes:  fi.Size(),
			ModifiedAt: fi.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModifiedAt > out[j].ModifiedAt })
	return connect.NewResponse(&v1.ListReportsResponse{Reports: out}), nil
}

// GetReport 报告正文（basename 校验防目录穿越）。
func (s *Service) GetReport(ctx context.Context, req *connect.Request[v1.GetReportRequest]) (*connect.Response[v1.GetReportResponse], error) {
	name := req.Msg.Name
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".md") {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("非法报告名"))
	}
	data, err := os.ReadFile(filepath.Join(s.reportDir(), name))
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("报告不存在"))
	}
	return connect.NewResponse(&v1.GetReportResponse{Name: name, Content: string(data)}), nil
}
