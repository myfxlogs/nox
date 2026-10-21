// investor/request_box.tsx —— 请求箱（第一眼层级·行动段：业主唯一动作面）。
// D-268 M4.6：按钮只发 id+二态，态机判权全后端（前端零信任）。
import type { RequestBoxItem } from "../../gen/dashboardv1/request_box_pb";
import { ResolveRequestState } from "../../gen/dashboardv1/request_box_pb";
import { fmtAge } from "../../components/fmt";

export function RequestBox(p: {
  items: RequestBoxItem[];
  busy: number | null;
  resolve: (id: number, state: ResolveRequestState) => void;
}) {
  return (
    <>
      <h2>请求箱{p.items.length > 0 ? `（${p.items.length}）` : ""}</h2>
      {p.items.length === 0 ? (
        <div className="muted">无待办请求</div>
      ) : (
        <table>
          <thead><tr><th>#</th><th>种类</th><th>模板</th><th>对象</th><th>请求</th><th>龄</th><th></th></tr></thead>
          <tbody>
            {p.items.map((r) => (
              <tr key={Number(r.id)}>
                <td>{Number(r.id)}</td>
                <td>{r.kind}</td>
                <td>{r.templateId}</td>
                <td>{r.subject}</td>
                <td className="muted">{r.requestText}</td>
                <td>{fmtAge(Number(r.ageSeconds))}</td>
                <td>
                  {r.status === "marked" ? (
                    // D-652-C：capital 回执 marked=已点击·到账核验中——
                    // 实测桶值达标自动收 fulfilled，悬空超 24h 系统告警，不再可点。
                    <span className="muted">待到账核验</span>
                  ) : (
                    <>
                      <button disabled={p.busy === Number(r.id)}
                        onClick={() => p.resolve(Number(r.id), ResolveRequestState.FULFILLED)}>已喂条件</button>
                      {" "}
                      <button disabled={p.busy === Number(r.id)}
                        onClick={() => p.resolve(Number(r.id), ResolveRequestState.WAIVED)}>放弃</button>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
