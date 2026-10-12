// stream.ts —— StreamUpdates 单例：页面注册 case selector，断线重连（看门狗，不产数据）。
import { client } from "./api";
import type { StreamUpdatesResponse } from "./gen/dashboardv1/stream_pb";

type CaseName = NonNullable<StreamUpdatesResponse["update"]["case"]>;
type Handler = (value: NonNullable<StreamUpdatesResponse["update"]["value"]>) => void;

const handlers = new Map<CaseName, Set<Handler>>();
const statusHandlers = new Set<(connected: boolean) => void>();
let running = false;
let abort: AbortController | null = null;
let retryMs = 1000;

export function onCase(c: CaseName, fn: Handler): () => void {
  let set = handlers.get(c);
  if (!set) handlers.set(c, (set = new Set()));
  set.add(fn);
  ensure();
  return () => set!.delete(fn);
}

export function onStatus(fn: (connected: boolean) => void): () => void {
  statusHandlers.add(fn);
  return () => statusHandlers.delete(fn);
}

function emit(connected: boolean) {
  statusHandlers.forEach((f) => f(connected));
}

function ensure() {
  if (running || abort) return;
  running = true;
  void loop();
}

async function loop() {
  for (;;) {
    abort = new AbortController();
    try {
      emit(true);
      for await (const res of client.streamUpdates({}, { signal: abort.signal })) {
        retryMs = 1000;
        const u = res.update;
        if (!u.case) continue;
        const set = handlers.get(u.case as CaseName);
        if (set) set.forEach((f) => f(u.value!));
      }
    } catch {
      // 断线/错误 → 退避重连（看门狗职责：只触发重连，不携带业务数据）。
    } finally {
      emit(false);
      abort = null;
    }
    if (handlers.size === 0) break;
    await new Promise((r) => setTimeout(r, retryMs));
    retryMs = Math.min(retryMs * 2, 15000);
    if (handlers.size === 0) break;
  }
  running = false;
}
