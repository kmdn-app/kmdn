import { queryOptions, useQuery, type QueryClient } from "@tanstack/react-query";
import { ApiError, createApi, unwrap, type Me, type SetupStatus } from "@kmdn/api-client";
import { sourceBufferMiddleware } from "./source-buffers";

export const api = createApi();
api.use(sourceBufferMiddleware);
export { ApiError, unwrap };

export const meQuery = queryOptions({
  queryKey: ["me"],
  queryFn: async (): Promise<Me | null> => {
    try {
      return await unwrap(api.GET("/me"));
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return null;
      throw e;
    }
  },
  staleTime: 60_000,
});

/** Refetches the signed-in user, bypassing any cached "signed out" state. */
export function refreshMe(qc: QueryClient) {
  return qc.fetchQuery({ ...meQuery, staleTime: 0 });
}

export const setupStatusQuery = queryOptions({
  queryKey: ["setup-status"],
  queryFn: (): Promise<SetupStatus> => unwrap(api.GET("/setup/status")),
  staleTime: 30_000,
});

export function useMe() {
  return useQuery(meQuery);
}

export function useSetupStatus() {
  return useQuery(setupStatusQuery);
}

/**
 * A failure that clears up on its own: the server restarting (a deploy, with
 * the proxy answering 502–504 meanwhile) or the network dropping.
 */
export function isTransient(e: unknown): boolean {
  if (e instanceof ApiError) return e.status === 502 || e.status === 503 || e.status === 504;
  return e instanceof TypeError; // fetch couldn't connect
}

/** Queries retry through a restart (about 20 s), and once otherwise. */
export function retryQuery(failures: number, e: unknown): boolean {
  return isTransient(e) ? failures < 6 : failures < 1;
}

/** 1, 2, 4, then every 5 seconds. */
export function retryDelay(failures: number): number {
  return Math.min(1000 * 2 ** failures, 5000);
}

/** Human-readable message for an error from the API. */
export function errorMessage(e: unknown, fallback: string): string {
  if (e instanceof ApiError) return e.problem.detail || fallback;
  if (e instanceof TypeError) return fallback;
  return fallback;
}

