// D-270 CN-FI 工作日判断（S1）。
// 封装 cnfi_calendar 查询，日历缺失=保守 false（与 collector.go 同策略）。
package pgstore

import (
	"context"
	"time"
)

// CNFICalendarIsBusinessDay 判断指定日期是否 CN 工作日。
// 日历缺失=保守返回 false（与 collector.go 同策略：宁可漏判不可误判）。
func (s *Store) CNFICalendarIsBusinessDay(ctx context.Context, day time.Time) (bool, error) {
	has, err := s.CNFICalendarHasDate(ctx, day)
	if err != nil {
		return false, err
	}
	if !has {
		return false, nil // 日历缺失=保守 false
	}
	var business bool
	err = s.pool.QueryRow(ctx,
		`SELECT is_business_day FROM cnfi_calendar WHERE cal_date = $1`,
		day.Format("2006-01-02")).Scan(&business)
	if err != nil {
		return false, err
	}
	return business, nil
}
