package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestD190_EmployeeRegistry：装配员工全在册（幂等）+心跳刷新+离线标离线（
// 节拍型超 3×cadence、常驻型 running=false）+退出累计。
func TestD190_EmployeeRegistry(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	s.pool.Exec(ctx, `DELETE FROM fin_employee_registry WHERE name LIKE 'd190test%'`)

	// 装配登记幂等。
	e := &Employee{Name: "d190test:loop", Role: "测试员", Dept: "测", Assembly: "test.go", CadenceSec: 60}
	if err := s.RegisterEmployee(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.Role = "测试员v2"
	if err := s.RegisterEmployee(ctx, e); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListEmployees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *Employee
	for i := range rows {
		if rows[i].Name == e.Name {
			got = &rows[i]
		}
	}
	if got == nil || got.Role != "测试员v2" {
		t.Fatalf("登记幂等 upsert 败: %+v", got)
	}
	if got.EmployeeStatus(time.Now()) != "离线" { // 注册未心跳=不在岗=离线
		t.Fatalf("未心跳节拍型应离线: %s", got.EmployeeStatus(time.Now()))
	}
	// 心跳→在职。
	if err := s.EmployeeBeat(ctx, e.Name); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListEmployees(ctx)
	for i := range rows {
		if rows[i].Name == e.Name {
			got = &rows[i]
		}
	}
	if got.EmployeeStatus(time.Now()) != "在职" || got.BeatTs == nil || !got.Running {
		t.Fatalf("心跳后应在职: %+v", got)
	}
	// 退出→离线+exits 累计。
	if err := s.EmployeeExit(ctx, e.Name); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListEmployees(ctx)
	for i := range rows {
		if rows[i].Name == e.Name {
			got = &rows[i]
		}
	}
	if got.Exits != 1 || got.Running || got.EmployeeStatus(time.Now()) != "离线" {
		t.Fatalf("退出面错: %+v", got)
	}
	// 未登记员工 Beat 自动补册 role=未登记（装配漏登零静默）。
	if err := s.EmployeeBeat(ctx, "d190test:ghost"); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListEmployees(ctx)
	var ghost *Employee
	for i := range rows {
		if rows[i].Name == "d190test:ghost" {
			ghost = &rows[i]
		}
	}
	if ghost == nil || ghost.Role != "未登记" || ghost.EmployeeStatus(time.Now()) != "在职" {
		t.Fatalf("幽灵员工补册错: %+v", ghost)
	}
	s.pool.Exec(ctx, `DELETE FROM fin_employee_registry WHERE name LIKE 'd190test%'`)
}
