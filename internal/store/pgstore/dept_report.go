// dept_report.go —— D-244：fin_dept_reports 部门日报→经理摘要存储面。
// dept='manager' 行=公司日报；dept=十一部中文名行=部报（账本行即财务专项）。
package pgstore

import (
	"context"
	"time"
)

// DeptReport fin_dept_reports 行。
type DeptReport struct {
	Dept             string
	ReportDate       time.Time // DATE（BJ 历日，调用方钉）
	Body             string
	ElapsedMs        int
	PromptTokens     int
	CompletionTokens int
	CreatedAt        time.Time
}

// UpsertDeptReport 当日重跑覆盖（PK 冲突更新正文与耗时/token）。
func (s *Store) UpsertDeptReport(ctx context.Context, r DeptReport) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_dept_reports (dept, report_date, body, elapsed_ms, prompt_tokens, completion_tokens)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (dept, report_date) DO UPDATE SET
		    body=$3, elapsed_ms=$4, prompt_tokens=$5, completion_tokens=$6, created_at=now()`,
		r.Dept, r.ReportDate, r.Body, r.ElapsedMs, r.PromptTokens, r.CompletionTokens)
	return err
}

// ListDeptReports 某日全部行（dept 升序——ASCII manager 居首，前端展示自排序）。
func (s *Store) ListDeptReports(ctx context.Context, date time.Time) ([]DeptReport, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT dept, report_date, body, elapsed_ms, prompt_tokens, completion_tokens, created_at
		FROM fin_dept_reports WHERE report_date=$1 ORDER BY dept`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeptReport
	for rows.Next() {
		var r DeptReport
		if err := rows.Scan(&r.Dept, &r.ReportDate, &r.Body, &r.ElapsedMs, &r.PromptTokens, &r.CompletionTokens, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListDeptReportDates 最近有报日期（看板日报列表侧栏；limit 调用方钉）。
func (s *Store) ListDeptReportDates(ctx context.Context, limit int) ([]time.Time, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT report_date FROM fin_dept_reports ORDER BY report_date DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
