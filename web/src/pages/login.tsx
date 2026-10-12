// login.tsx —— NOX 露面①：登录卡。token 经 Health RPC 验证后存 sessionStorage。
import { useState } from "react";
import { client, setToken, errText } from "../api";
import { NOX_TITLE } from "../nox";

export default function LoginPage() {
  const [tok, setTok] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr("");
    setToken(tok.trim());
    try {
      await client.health({});
      location.hash = "#/";
      location.reload();
    } catch (ex) {
      setToken("");
      setErr(errText(ex));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-card">
      <h1>{NOX_TITLE}</h1>
      <div className="muted">运营看板 — 请填入访问令牌</div>
      <form onSubmit={submit}>
        <input
          type="password"
          placeholder="token"
          value={tok}
          onChange={(e) => setTok(e.target.value)}
          autoFocus
        />
        {err && <div className="err">{err}</div>}
        <button type="submit" disabled={busy || !tok.trim()}>
          {busy ? "验证中…" : "登录"}
        </button>
      </form>
    </div>
  );
}
