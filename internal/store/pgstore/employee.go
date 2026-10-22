// employee.go —— D-190：员工花名册 fin_employee_registry。
// 语义：Register=装配点显式入职（幂等 upsert）；Beat=心跳（挂现有 loop 节拍）；
// Exit=协程退出（running 置假、exits 累计）；Task=当前任务摘要更新。
// 在岗判定：cadence_sec>0 节拍型→超 3×cadence 未心跳=滞跳；cadence_sec=0 常驻型
// →running=false 即离线（fn 退出为唯一真相，GoLoop 已知）；running+节拍滞→滞跳。
package pgstore

import (
	"context"
	"time"
)

// Employee 花名册行（编制实况）。
type Employee struct {
	Name        string
	Role        string
	Dept        string
	Assembly    string
	CadenceSec  int
	EventDriven bool // 事件驱动型（任务型 spawn——跑完即退正常，不入 running 判生死域）
	Running     bool
	Exits       int
	Task        string
	BeatTs      *time.Time
	Enabled     bool
}

// EmployeeStatus 看板态：在职 | 滞跳 | 离线 | 离职。
func (e Employee) EmployeeStatus(now time.Time) string {
	if !e.Enabled {
		return "离职"
	}
	if e.EventDriven {
		return "事件驱动" // 非永驻 spawn——跑完即退属正常，不判离线/滞跳
	}
	if !e.Running {
		return "离线"
	}
	if e.CadenceSec > 0 &&
		(e.BeatTs == nil || now.Sub(*e.BeatTs) > 3*time.Duration(e.CadenceSec)*time.Second) {
		return "滞跳"
	}
	return "在职"
}

// RegisterEmployee 入职登记（幂等 upsert；name/role/dept/assembly/cadence 更新面）。
func (s *Store) RegisterEmployee(ctx context.Context, e *Employee) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_employee_registry (name, role, dept, assembly, cadence_sec, event_driven)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (name) DO UPDATE SET role=$2, dept=$3, assembly=$4,
		    cadence_sec=$5, event_driven=$6, updated_at=now()`,
		e.Name, e.Role, e.Dept, e.Assembly, e.CadenceSec, e.EventDriven)
	return err
}

// EmployeeBeat 心跳：D-246 v3 §4.4 改只 UPDATE——未注册名不再隐式 INSERT
// 未登记行（台账只收装配层 Register 实名 daemon）；found=false 由调用方转告警。
func (s *Store) EmployeeBeat(ctx context.Context, name string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE fin_employee_registry
		SET running=true, beat_ts=now(), updated_at=now()
		WHERE name=$1`,
		name)
	return tag.RowsAffected() > 0, err
}

// EmployeeTask 当前任务摘要更新（无行不建——任务属已注册员工）。
func (s *Store) EmployeeTask(ctx context.Context, name, task string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_employee_registry SET task=$2, updated_at=now() WHERE name=$1`,
		name, task)
	return err
}

// EmployeeExit 协程退出：running=false、exits+1（退出史）。
func (s *Store) EmployeeExit(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_employee_registry SET running=false, exits=exits+1, updated_at=now()
		WHERE name=$1`, name)
	return err
}

// ListEmployees 全员编制实况（name 序）。
func (s *Store) ListEmployees(ctx context.Context) ([]Employee, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, role, dept, assembly, cadence_sec, running, exits,
		       task, beat_ts, enabled, event_driven
		FROM fin_employee_registry ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Employee
	for rows.Next() {
		var e Employee
		if err := rows.Scan(&e.Name, &e.Role, &e.Dept, &e.Assembly, &e.CadenceSec,
			&e.Running, &e.Exits, &e.Task, &e.BeatTs, &e.Enabled, &e.EventDriven); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RetireEmployee 条件门注销：gate 未开（env/cfg 缺）的注册行→enabled=f
// （离职态如实，非假离线；无行=零影响天然幂等）。
func (s *Store) RetireEmployee(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_employee_registry SET enabled=false, updated_at=now() WHERE name=$1`, name)
	return err
}
