// org.tsx —— 运行面页（D-246 v3 §4.4）：daemon 台账×运行状态（六资金域+平台横切）。status 后端算定，零前端计算。
// case14 topic="employees" 接线后自动刷新；未接线如实显示 beat_ts。
// S3′-final 批迁：第一眼=部门 daemon 活态卡阵（状态徽标+心跳 FreshDot），
// 部门分组结构原位。纯 JSX/CSS 重排零逻辑动。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { Employee } from "../gen/dashboardv1/employee_pb";
import { tsUnix } from "../components/fmt";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";
import { FreshDot as Dot } from "../components/FreshDot";
import { freshOf } from "../components/freshness";

const DEPT_ORDER = ["数据", "研究", "投资", "风控", "交易", "账本", "平台"];
const STATUS_TONE: Record<string, "good" | "warn" | "critical" | "neutral"> = {
  在职: "good",
  滞跳: "warn",
  离线: "critical",
  离职: "neutral",
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
      <h1>运行面</h1>
      <div className="muted">夜班枭编制——心跳=枭眼（beat_ts 拍即当班证）</div>
      {err && <div className="err">{err}</div>}
      <div className="muted">daemon 运行态由后端算定（在职/滞跳/离线/离职）；共 {emps.length} 项。</div>
      {depts.length === 0 && <EmptyState>零 daemon 行</EmptyState>}
      {depts.map((d) => (
        <section key={d}>
          <h2>
            {d} <span className="cnt">{byDept.get(d)!.length}项</span>
          </h2>
          <ul className="opp-cards opp-grid">
            {byDept.get(d)!.map((e) => (
              <li key={e.name} className="opp-card">
                <div className="opp-card-head">
                  <Dot
                    dot={freshOf({
                      name: e.name,
                      status: e.status === "在职" ? "live" : e.status === "滞跳" ? "stale" : "down",
                      lastFactAt: Number(e.beatTs) * 1000,
                    })}
                  />
                  <span className="opp-card-kind">{e.name}</span>
                  <Chip tone={STATUS_TONE[e.status] ?? "neutral"}>{e.status}</Chip>
                </div>
                <div className="opp-card-narrative">
                  {e.role} · {e.assembly} · {e.cadenceSec || "—"}s
                </div>
                <div className="opp-card-narrative">
                  <span className={e.running ? "good" : "muted"}>run={String(e.running)}</span>
                  {" · "}{e.task || "—"}
                </div>
                <div className="opp-card-narrative muted">心跳 {tsUnix(e.beatTs)}</div>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
