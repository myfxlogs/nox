// org.tsx —— 运行面页（D-246 v3 §4.4）：daemon 台账×运行状态（六资金域+平台横切）。status 后端算定，零前端计算。
// case14 topic="employees" 接线后自动刷新；未接线如实显示 beat_ts。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { Employee } from "../gen/dashboardv1/employee_pb";
import { tsUnix } from "../components/fmt";

const DEPT_ORDER = ["数据", "研究", "投资", "风控", "交易", "账本", "平台"];
const STATUS_CLS: Record<string, string> = {
  在职: "st-run",
  滞跳: "st-lag",
  离线: "st-dead",
  离职: "st-off",
};

export default function OrgPage() {
  const [emps, setEmps] = useState<Employee[]>([]);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await client.listEmployees({});
      setEmps(r.employees);
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  }, []);

  useEffect(() => {
    void load();
    const off = onCase("dataRefresh", (v) => {
      if ((v as { topic: string }).topic === "employees") void load();
    });
    return off;
  }, [load]);

  const byDept = new Map<string, Employee[]>();
  for (const e of emps) {
    const l = byDept.get(e.dept) ?? [];
    l.push(e);
    byDept.set(e.dept, l);
  }
  const depts = DEPT_ORDER.filter((d) => byDept.has(d)).concat(
    [...byDept.keys()].filter((d) => !DEPT_ORDER.includes(d))
  );

  return (
    <div>
      {err && <div className="err">{err}</div>}
      <div className="muted">daemon 运行态由后端算定（在职/滞跳/离线/离职）；共 {emps.length} 项。</div>
      {depts.map((d) => (
        <div className="dept" key={d}>
          <h3>
            {d}<span className="cnt">{byDept.get(d)!.length}项</span>
          </h3>
          <table>
            <thead><tr><th>daemon</th><th>角色</th><th>装配点</th><th>节拍s</th><th>running</th><th>任务</th><th>心跳</th><th>状态</th></tr></thead>
            <tbody>
              {byDept.get(d)!.map((e) => (
                <tr key={e.name}>
                  <td>{e.name}</td><td>{e.role}</td><td>{e.assembly}</td>
                  <td>{e.cadenceSec || "—"}</td>
                  <td className={e.running ? "good" : "muted"}>{String(e.running)}</td>
                  <td style={{ whiteSpace: "normal" }}>{e.task || "—"}</td>
                  <td>{tsUnix(e.beatTs)}</td>
                  <td><span className={"tag " + (STATUS_CLS[e.status] ?? "st-off")}>{e.status}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ))}
    </div>
  );
}
