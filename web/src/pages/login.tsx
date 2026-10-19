// login.tsx —— NOX 露面①：登录卡。TOTP 6 位码 → /totp/login → session（4h）存 sessionStorage。
// 业主令 10-19：原静态 token 输入退役（永不过期+太长记不住）；static token 径保留=AI 席位直连。
// 追加：登录卡显示身份码 Nox:xxxxxx（/totp/idcode 指纹面）——业主绑定程序多，
// 凭此码在 Authenticator 多条目里对号取 6 位码。
import { useEffect, useState } from "react";
import { setToken } from "../api";
import { NOX_TITLE } from "../nox";

export default function LoginPage() {
  const [code, setCode] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [idCode, setIdCode] = useState("");

  useEffect(() => {
    fetch("/totp/idcode")
      .then((r) => (r.ok ? r.json() : null))
      .then((d: { id_code?: string } | null) => {
        if (d?.id_code) setIdCode(d.id_code);
      })
      .catch(() => {});
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      const r = await fetch("/totp/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ code: code.trim() }),
      });
      if (!r.ok) throw new Error(r.status === 429 ? await r.text() : "验证码错误或已过期");
      const data = (await r.json()) as { token: string };
      setToken(data.token);
      location.hash = "#/";
      location.reload();
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : String(ex));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-card">
      <h1>{NOX_TITLE}</h1>
      <div className="muted">运营看板 — 请输入 Authenticator 6 位验证码</div>
      {idCode && <div className="totp-id">Authenticator 条目 <b>Nox:{idCode}</b>（多条目时凭此对号）</div>}
      <form onSubmit={submit}>
        <input
          type="text"
          inputMode="numeric"
          pattern="[0-9]{6}"
          maxLength={6}
          placeholder="6 位验证码"
          value={code}
          onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
          autoFocus
        />
        {err && <div className="err">{err}</div>}
        <button type="submit" disabled={busy || code.length !== 6}>
          {busy ? "验证中…" : "登录"}
        </button>
      </form>
    </div>
  );
}
