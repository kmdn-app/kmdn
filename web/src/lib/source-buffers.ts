import { ApiError, type Api } from "@kmdn/api-client";
import { realtime } from "./realtime";

const buffers = new Map<string, Set<() => void>>();
const unacknowledged = new Map<string, symbol>();

/** Receipt tracking outlives the editor that produced the update. */
export function markSourceUpdate(revision: string): void {
  unacknowledged.set(revision, Symbol());
}

export function registerSourceBuffer(revision: string, flush: () => void): () => void {
  let active = buffers.get(revision);
  if (!active) buffers.set(revision, (active = new Set()));
  active.add(flush);
  return () => {
    active.delete(flush);
    if (!active.size) buffers.delete(revision);
  };
}

/** Save, review and restore requests must include edits still in CodeMirror's debounce buffer. */
export const sourceBufferMiddleware: Parameters<Api["use"]>[0] = {
  async onRequest({ request }) {
    if (["POST", "PUT", "PATCH", "DELETE"].includes(request.method)) {
      const revision = /^\/api\/v1\/revisions\/([^/]+)(?:\/|$)/.exec(new URL(request.url).pathname)?.[1];
      const active = revision ? buffers.get(revision) : undefined;
      active?.forEach((flush) => flush());
      const version = revision ? unacknowledged.get(revision) : undefined;
      if (revision && (active?.size || version || realtime.hasPendingUpdates(revision))) {
        try {
          await realtime.waitForUpdates(revision);
        } catch (error) {
          throw new ApiError({ type: "about:blank", status: 409, title: "Changes are not saved", code: "source_not_synced", detail: error instanceof Error ? error.message : "The server has not received these edits." });
        }
        // A later source edit needs its own receipt even if this request succeeds.
        if (unacknowledged.get(revision) === version) unacknowledged.delete(revision);
      }
    }
    return request;
  },
};
