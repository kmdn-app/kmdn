import { ApiError, type Api } from "@kmdn/api-client";
import { realtime } from "./realtime";

const buffers = new Map<string, Set<() => void>>();
const unacknowledged = new Map<string, symbol>();
type FailedSource = { message: string; source?: string; base?: string };
const failures = new Map<string, Map<string, FailedSource>>();

export function getSourceBufferError(revision: string, path: string): FailedSource | undefined {
  return failures.get(revision)?.get(path);
}

/** A failed flush must remain visible to Save after its editor has closed. */
export function setSourceBufferError(revision: string, path: string, message?: string, source?: string, base?: string): void {
  let paths = failures.get(revision);
  if (message) {
    if (!paths) failures.set(revision, (paths = new Map()));
    paths.set(path, { message, source: source ?? paths.get(path)?.source, base: base ?? paths.get(path)?.base });
  } else {
    paths?.delete(path);
    if (!paths?.size) failures.delete(revision);
  }
}

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
      if (revision) {
        try {
          active?.forEach((flush) => flush());
          const failure = failures.get(revision)?.values().next().value;
          if (failure) throw new Error(failure.message);
          const version = unacknowledged.get(revision);
          if (active?.size || version || realtime.hasPendingUpdates(revision)) {
            await realtime.waitForUpdates(revision);
            // A later source edit needs its own receipt even if this request succeeds.
            if (unacknowledged.get(revision) === version) unacknowledged.delete(revision);
          }
        } catch (error) {
          throw new ApiError({ type: "about:blank", status: 409, title: "Changes are not saved", code: "source_not_synced", detail: error instanceof Error ? error.message : "The server has not received these edits." });
        }
      }
    }
    return request;
  },
};
