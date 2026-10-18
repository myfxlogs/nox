// reports.tsx —— 报告中心（业主视窗两件套 A）：部门日报 + 复盘/KB 报告历史翻阅 +
// 手动补跑。五 RPC 全消费：ListDeptReports/ListReports/GetReport/GenerateKbReport/
// GenerateRetroReport。markdown 极简渲染（标题/列表/表格线/加粗）——只渲不运算。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import type { DeptReport } from "../gen/dashboardv1/dept_report_pb";

// mdRender 极简 markdown：#/##/### 标题、- 列表、| 表格行（pre 原排）、**加粗**。
function mdRender(body: string) {
  const lines = body.split("\n");
  const out: React.JSX.Element[] = [];
  for (let i = 0; i < lines.length; i++) {
    const ln = lines[i];
    const text = ln.replace(/\*\*(.+?)\*\*/g, "$1");
    if (ln.startsWith("### ")) out.push(<h4 key={i}>{text.slice(4)}</h4>);
    else if (ln.startsWith("## ")) out.push(<h3 key={i}>{text.slice(3)}</h3>);
    else if (ln.startsWith("# ")) out.push(<h3 key={i}>{text.slice(2)}</h3>);
    else if (ln.startsWith("- ") || ln.startsWith("* "))
      out.push(
        <div key={i} style={{ paddingLeft: 14 }}>
          · {text.slice(2)}
        </div>
      );
    else if (ln.startsWith("|"))
      out.push(
        <pre key={i} style={{ margin: 0 }}>
          {ln}
        </pre>
      );
    else if (ln.trim() === "") out.push(<div key={i} style={{ height: 6 }} />);
    else out.push(<div key={i}>{text}</div>);
  }
  return out;
}

export default function ReportsPage() {
  const [dates, setDates] = useState<string[]>([]);
  const [date, setDate] = useState("");
  const [depts, setDepts] = useState<DeptReport[]>([]);
  const [picked, setPicked] = useState<DeptReport | null>(null);
  const [files, setFiles] = useState<{ name: string; sizeBytes: bigint | number; modifiedAt: string }[]>([]);
  const [fileBody, setFileBody] = useState<{ name: string; content: string } | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");

  const loadDept = async (d: string) => {
    const r = await client.listDeptReports({ reportDate: d });
    setDepts(r.reports);
    setDates(r.dates);
    setDate(r.reportDate);
    setPicked(null);
  };
  const loadFiles = async () => {
    const r = await client.listReports({});
    setFiles(r.reports);
  };

  useEffect(() => {
    (async () => {
      try {
        await Promise.all([loadDept(""), loadFiles()]);
        setErr("");
      } catch (e) {
        setErr(errText(e));
      }
    })();
  }, []);

  const openFile = async (name: string) => {
    try {
      const r = await client.getReport({ name });
      setFileBody({ name: r.name, content: r.content });
      setErr("");
    } catch (e) {
      setErr(errText(e));
    }
  };

  const generate = async (kind: string) => {
    setBusy(kind);
    try {
      if (kind === "kb") await client.generateKbReport({});
      else await client.generateRetroReport({ kind });
      await loadFiles();
      setErr("");
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy("");
    }
  };

  return (
    <div>
      <h1>报告</h1>
      {err && <div className="err">{err}</div>}
      {/* 第一眼=最新部门日报（报告主体）；文件台账居后 */}
      <section>
        <h2>部门日报</h2>
        <div className="muted" style={{ marginBottom: 6 }}>
          {dates.map((d) => (
            <a
              key={d}
              href="#"
              style={{ marginRight: 10, color: d === date ? "#ddd" : "#888" }}
              onClick={(e) => {
                e.preventDefault();
                void loadDept(d);
              }}
            >
              {d}
            </a>
          ))}
        </div>
        <table>
          <tbody>
            {depts.map((r) => (
              <tr key={r.dept}>
                <td style={{ width: 140 }}>
                  <a href="#" onClick={(e) => { e.preventDefault(); setPicked(r); }}>
                    {r.dept}
                  </a>
                </td>
                <td className="muted">
                  {r.elapsedMs}ms · {r.promptTokens}+{r.completionTokens} tok ·{" "}
                  {new Date(Number(r.createdTs) * 1000).toLocaleString()}
                </td>
              </tr>
            ))}
            {depts.length === 0 && (
              <tr><td className="muted">该日无报</td></tr>
            )}
          </tbody>
        </table>
      </section>
      {picked && (
        <section>
          <h3>
            {picked.dept} · {picked.reportDate}
            <a href="#" style={{ marginLeft: 12, fontSize: 12 }} onClick={(e) => { e.preventDefault(); setPicked(null); }}>
              收起
            </a>
          </h3>
          {mdRender(picked.body)}
        </section>
      )}
      <section>
        <h2>
          报告文件
          <button style={{ marginLeft: 12 }} disabled={busy !== ""} onClick={() => void generate("daily")}>
            补跑日报
          </button>
          <button style={{ marginLeft: 6 }} disabled={busy !== ""} onClick={() => void generate("weekly")}>
            补跑周报
          </button>
          <button style={{ marginLeft: 6 }} disabled={busy !== ""} onClick={() => void generate("kb")}>
            补跑 KB 报告
          </button>
        </h2>
        <table>
          <tbody>
            {files.map((f) => (
              <tr key={f.name}>
                <td>
                  <a href="#" onClick={(e) => { e.preventDefault(); void openFile(f.name); }}>
                    {f.name}
                  </a>
                </td>
                <td className="muted">
                  {Number(f.sizeBytes)}B · {f.modifiedAt}
                </td>
              </tr>
            ))}
            {files.length === 0 && (
              <tr><td className="muted">无报告文件</td></tr>
            )}
          </tbody>
        </table>
      </section>
      {fileBody && (
        <section>
          <h3>
            {fileBody.name}
            <a href="#" style={{ marginLeft: 12, fontSize: 12 }} onClick={(e) => { e.preventDefault(); setFileBody(null); }}>
              收起
            </a>
          </h3>
          {mdRender(fileBody.content)}
        </section>
      )}
    </div>
  );
}
