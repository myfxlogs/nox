package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	v1 "nox/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// TestReportsRPC_D457：ListReports/GetReport 三档报告列表+正文+basename 防穿越。
func TestReportsRPC_D457(t *testing.T) {
	dir := t.TempDir()
	repDir := filepath.Join(dir, "docs/报告")
	if err := os.MkdirAll(repDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(repDir, "sysretro-daily-2026-09-24.md"), []byte("# 日结\n"), 0o644)
	os.WriteFile(filepath.Join(repDir, "kb-meta-2026-09-24.md"), []byte("# KB\n"), 0o644)
	os.WriteFile(filepath.Join(repDir, "note.txt"), []byte("x"), 0o644) // 非 md 不列
	s := &Service{analyst: &analystDeps{knRoot: dir}}

	lr, err := s.ListReports(context.Background(), connect.NewRequest(&v1.ListReportsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(lr.Msg.Reports) != 2 {
		t.Fatalf("reports=%v", lr.Msg.Reports)
	}
	gr, err := s.GetReport(context.Background(), connect.NewRequest(&v1.GetReportRequest{Name: "sysretro-daily-2026-09-24.md"}))
	if err != nil || gr.Msg.Content != "# 日结\n" {
		t.Fatalf("get report: %v %q", err, gr.Msg.Content)
	}
	for _, bad := range []string{"../STATE.md", "x/../a.md", "", "a.txt"} {
		if _, err := s.GetReport(context.Background(), connect.NewRequest(&v1.GetReportRequest{Name: bad})); err == nil {
			t.Fatalf("bad name %q accepted", bad)
		}
	}
	if _, err := s.GenerateRetroReport(context.Background(), connect.NewRequest(&v1.GenerateRetroReportRequest{Kind: "bogus"})); err == nil {
		t.Fatal("bogus kind accepted")
	}
}
