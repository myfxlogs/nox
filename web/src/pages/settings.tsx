// settings.tsx —— 设置页（AI 设置板块）：ai_settings 表单走 GetAiSettings/SetAiSettings
// 既有 RPC——api_key 掩码尾4回显、留空提交=不动（后端 UPSERT 语义），零前端运算。
import { useEffect, useState } from "react";
import { client, errText } from "../api";
import { SecretsCard, PnlResetCard } from "../components/SettingsCards";

export default function SettingsPage() {
  const [baseURL, setBaseURL] = useState("");
  const [model, setModel] = useState("");
  const [apiKey, setApiKey] = useState(""); // 永不回显明文——输入框留空=不改动
  const [masked, setMasked] = useState("");
  const [enabled, setEnabled] = useState(false);
  const [msg, setMsg] = useState("");
  const [err, setErr] = useState("");

  useEffect(() => {
    (async () => {
      try {
        const r = await client.getAiSettings({});
        setBaseURL(r.baseUrl);
        setModel(r.model);
        setMasked(r.apiKeyMasked);
        setEnabled(r.enabled);
        setErr("");
      } catch (e) {
        setErr(errText(e));
      }
    })();
  }, []);

  const save = async () => {
    try {
      await client.setAiSettings({
        baseUrl: baseURL,
        model,
        apiKey, // 空串=后端不改动
        enabled,
      });
      const r = await client.getAiSettings({});
      setMasked(r.apiKeyMasked);
      setApiKey("");
      setMsg("已保存（api_key 留空=未改动）");
      setErr("");
    } catch (e) {
      setErr(errText(e));
      setMsg("");
    }
  };

  return (
    <div>
      <h1>设置</h1>
      {err && <div className="err">{err}</div>}
      {msg && <div className="muted">{msg}</div>}
      {/* S3′-final 批迁：表单段壳卡化（绑径原样） */}
      <section className="card" style={{ padding: 12 }}>
        <h2>AI 设置</h2>
        <table>
          <tbody>
            <tr>
              <td style={{ width: 120 }}>base_url</td>
              <td>
                <input
                  style={{ width: 420 }}
                  value={baseURL}
                  onChange={(e) => setBaseURL(e.target.value)}
                  placeholder="https://…/v1（Anthropic 兼容端点）"
                />
              </td>
            </tr>
            <tr>
              <td>model</td>
              <td>
                <input
                  style={{ width: 420 }}
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                />
              </td>
            </tr>
            <tr>
              <td>api_key</td>
              <td>
                <input
                  style={{ width: 420 }}
                  type="password"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder={masked ? `当前 ${masked}（留空不改动）` : "未设"}
                />
              </td>
            </tr>
            <tr>
              <td>enabled</td>
              <td>
                <label>
                  <input
                    type="checkbox"
                    checked={enabled}
                    onChange={(e) => setEnabled(e.target.checked)}
                  />{" "}
                  启用 analyst/kb 报告 LLM 调用
                </label>
              </td>
            </tr>
            <tr>
              <td />
              <td>
                <button onClick={() => void save()}>保存</button>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <SecretsCard />
      <PnlResetCard />
    </div>
  );
}
