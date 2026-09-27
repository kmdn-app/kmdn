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

/** Human-readable message for an error from the API. */
export function errorMessage(e: unknown, fallback: string): string {
  if (e instanceof ApiError) return e.problem.detail || fallback;
  if (e instanceof TypeError) return fallback;
  return fallback;
}

/** Whether the assistant (and consistency checks) are set up on this instance. */
export function useAssistantStatus() {
  return useQuery({ queryKey: ["assistant-status"], queryFn: () => unwrap(api.GET("/assistant/status")), staleTime: 60_000 });
}
