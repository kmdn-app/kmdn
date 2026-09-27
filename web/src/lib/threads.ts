import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "@kmdn/api-client";
import { api, unwrap } from "@/lib/api";
import { realtime } from "@/lib/realtime";

export type Thread = components["schemas"]["Thread"];
export type ThreadComment = components["schemas"]["Comment"];

/** Threads on a page of a revision, kept live by revision events. */
export function useThreads(revisionID: string | undefined, path: string, sort: "hot" | "updated") {
  const qc = useQueryClient();
  useEffect(() => {
    if (!revisionID) return;
    return realtime.follow("revision:" + revisionID, (ev) => {
      if (ev.type === "thread") void qc.invalidateQueries({ queryKey: ["threads", revisionID] });
    });
  }, [qc, revisionID]);
  return useQuery({
    queryKey: ["threads", revisionID, path, sort],
    queryFn: async () => (await unwrap(api.GET("/revisions/{revision}/threads", { params: { path: { revision: revisionID! }, query: { path, sort } } }))).items,
    enabled: !!revisionID,
  });
}
