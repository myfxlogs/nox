// employee.go —— D-190：员工花名册 fin_employee_registry。
// 语义：Register=装配点显式入职（幂等 upsert）；Beat=心跳（挂现有 loop 节拍）；
// Exit=协程退出（running 置假、exits 累计）；Task=当前任务摘要更新。
// 生死判定：cadence_sec>0 节拍型→超 3×cadence 未心跳=死亡；cadence_sec=0 常驻型
// →running=false 即死亡（fn 退出为唯一真相，GoLoop 已知）；running+节拍滞→滞跳。
package pgstore

import (
	"context"
	"time"
)

// Employee 花名册行（编制实况）。
type Employee struct {
	Name       string
	Role       string
	Dept       string
	Assembly   string
	CadenceSec int
	Running    bool
	Exits      int
	Task       string
	BeatTs     *time.Time
	Enabled    bool
}

// EmployeeStatus 看板态：在职 | 滞跳 | 死亡 | 停用。
func (e Employee) EmployeeStatus(now time.Time) string {
	if !e.Enabled {
		return "停用"
	}
	if !e.Running {
		return "死亡"
	}
	if e.CadenceSec > 0 &&
		(e.BeatTs == nil || now.Sub(*e.BeatTs) > 3*time.Duration(e.CadenceSec)*time.Second) {
		return "死亡"
	}
	return "在职"
}

// RegisterEmployee 入职登记（幂等 upsert；name/role/dept/assembly/cadence 更新面）。
func (s *Store) RegisterEmployee(ctx context.Context, e *Employee) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_employee_registry (name, role, dept, assembly, cadence_sec)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (name) DO UPDATE SET role=$2, dept=$3, assembly=$4,
		    cadence_sec=$5, updated_at=now()`,
		e.Name, e.Role, e.Dept, e.Assembly, e.CadenceSec)
	return err
}

// EmployeeBeat 心跳：beat_ts=now+running=true（未登记行自动补册 role='未登记'，
// 装配漏登员工也能浮出花名册——零静默）。
func (s *Store) EmployeeBeat(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_employee_registry (name, role, running, beat_ts)
		VALUES ($1,'未登记',true,now())
		ON CONFLICT (name) DO UPDATE SET running=true, beat_ts=now(), updated_at=now()`,
		name)
	return err
}

// EmployeeTask 当前任务摘要更新（无行不建——任务属已注册员工）。
func (s *Store) EmployeeTask(ctx context.Context, name, task string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_employee_registry SET task=$2, updated_at=now() WHERE name=$1`,
		name, task)
	return err
}

// EmployeeExit 协程退出：running=false、exits+1（死亡史）。
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
		       task, beat_ts, enabled
		FROM fin_employee_registry ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Employee
	for rows.Next() {
		var e Employee
		if err := rows.Scan(&e.Name, &e.Role, &e.Dept, &e.Assembly, &e.CadenceSec,
			&e.Running, &e.Exits, &e.Task, &e.BeatTs, &e.Enabled); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
