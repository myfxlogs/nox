// service_employees.go —— D-190：员工花名册看板 RPC（编制实况=在职/死亡/任务中，
// 状态后端算定，前端零计算）。
package dashboard

import (
	"context"
	"time"

	"connectrpc.com/connect"

	dashv1 "arb/internal/gen/dashboardv1"
)

func (s *Service) ListEmployees(ctx context.Context, _ *connect.Request[dashv1.ListEmployeesRequest]) (*connect.Response[dashv1.ListEmployeesResponse], error) {
	rows, err := s.store.ListEmployees(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	now := time.Now()
	resp := &dashv1.ListEmployeesResponse{NowTs: now.Unix()}
	for _, e := range rows {
		var beat int64
		if e.BeatTs != nil {
			beat = e.BeatTs.Unix()
		}
		resp.Employees = append(resp.Employees, &dashv1.Employee{
			Name: e.Name, Role: e.Role, Dept: e.Dept, Assembly: e.Assembly,
			CadenceSec: int32(e.CadenceSec), Running: e.Running, Exits: int32(e.Exits),
			Task: e.Task, BeatTs: beat, Enabled: e.Enabled,
			Status: e.EmployeeStatus(now),
		})
	}
	return connect.NewResponse(resp), nil
}
