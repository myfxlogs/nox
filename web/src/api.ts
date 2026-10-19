// api.ts —— DashboardService client 封装（Authorization 头注入+错误统一）。
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { DashboardService } from "./gen/dashboardv1/service_pb";

const TOKEN_KEY = "nox_token";

export function getToken(): string {
  return sessionStorage.getItem(TOKEN_KEY) ?? "";
}
export function setToken(t: string): void {
  sessionStorage.setItem(TOKEN_KEY, t);
}
export function clearToken(): void {
  sessionStorage.removeItem(TOKEN_KEY);
}

const transport = createConnectTransport({
  baseUrl: "/",
  interceptors: [
    (next) => async (req) => {
      const t = getToken();
      if (t) req.header.set("Authorization", `Bearer ${t}`);
      try {
        return await next(req);
      } catch (e) {
        // session 过期/失效→自动回登录页（TOTP 票禁线：过期后非静默续期=审计面）。
        // 已在登录页则只清 token 不 reload——预登录 RPC 的 401 若触发刷新=死循环
        // （10-19 业主实报：登录页反复刷新无法填 6 位码）。
        if (e instanceof Error && /unauthenticated|invalid token/i.test(e.message)) {
          clearToken();
          if ((location.hash || "#/").slice(1) !== "/login") {
            location.hash = "#/login";
            location.reload();
          }
        }
        throw e;
      }
    },
  ],
});

export const client = createClient(DashboardService, transport);

export function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
