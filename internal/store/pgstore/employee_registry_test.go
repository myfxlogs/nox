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
	found, err := s.EmployeeBeat(ctx, e.Name)
	if err != nil || !found {
		t.Fatalf("已登记名心跳应命中: found=%v err=%v", found, err)
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
	// 未登记员工 Beat：D-246 v3 §4.4 只 UPDATE——不落行、报 found=false 由调用方转告警。
	found, err = s.EmployeeBeat(ctx, "d190test:ghost")
	if err != nil || found {
		t.Fatalf("未登记名应 found=false: found=%v err=%v", found, err)
	}
	rows, _ = s.ListEmployees(ctx)
	for i := range rows {
		if rows[i].Name == "d190test:ghost" {
			t.Fatalf("未登记心跳不应落行: %+v", rows[i])
		}
	}
	s.pool.Exec(ctx, `DELETE FROM fin_employee_registry WHERE name LIKE 'd190test%'`)
}

// roster 建模错位修①锚：事件驱动型 spawn 注册后 running=f 不判离线
// （feishu_query:handle/confirmSelfTx/reviewRun/purge-acked/trxvote 型）。
func TestEmployeeEventDriven(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	e := &Employee{Name: "d190test:event", Role: "事件员", Dept: "测", Assembly: "test.go", EventDriven: true}
	if err := s.RegisterEmployee(ctx, e); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM fin_employee_registry WHERE name='d190test:event'`)
	rows, _ := s.ListEmployees(ctx)
	var got *Employee
	for i := range rows {
		if rows[i].Name == e.Name {
			got = &rows[i]
		}
	}
	if got == nil || !got.EventDriven {
		t.Fatalf("event_driven 应落库读回: %+v", got)
	}
	if st := got.EmployeeStatus(time.Now()); st != "事件驱动" {
		t.Fatalf("事件驱动型 running=f 不判离线: %s", st)
	}
	if err := s.RetireEmployee(ctx, e.Name); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListEmployees(ctx)
	for i := range rows {
		if rows[i].Name == e.Name {
			got = &rows[i]
		}
	}
	if got.Enabled || got.EmployeeStatus(time.Now()) != "离职" {
		t.Fatalf("Retire 应翻离职: %+v", got)
	}
}
