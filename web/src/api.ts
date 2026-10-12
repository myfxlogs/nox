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
      return next(req);
    },
  ],
});

export const client = createClient(DashboardService, transport);

export function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
