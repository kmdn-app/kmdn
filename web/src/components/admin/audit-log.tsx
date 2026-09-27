import { useMemo, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Download, Loader2, X } from "lucide-react";
import type { components } from "@kmdn/api-client";
import { Panel } from "@/components/settings-layout";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api, unwrap } from "@/lib/api";
import { useRepos } from "@/lib/repos";

type Entry = components["schemas"]["AuditEntry"];
const ANY = "__any";

type Filters = { action: string; actor: string; repo: string; from: string; to: string };

/** The query string the list and the exports share. */
function toQuery(f: Filters): Record<string, string> {
  const q: Record<string, string> = {};
  if (f.action !== ANY) q.action = f.action;
  if (f.actor !== ANY) {
    const [type, id] = f.actor.split(":");
    q.actor_type = type!;
    if (id) q.actor_id = id;
  }
  if (f.repo !== ANY) q.repo = f.repo;
  if (f.from) q.from = f.from;
  if (f.to) q.to = f.to;
  return q;
}

/** A compact line from an entry's data (never secrets: the server doesn't store any). */
function details(e: Entry): string {
  const d = (e.data ?? {}) as Record<string, unknown>;
  const parts: string[] = [];
  for (const k of ["number", "path", "query", "tool", "email", "name", "role", "provider", "url_host", "sha", "tokens", "count", "format", "status"]) {
    const v = d[k];
    if (v === undefined || v === null || v === "") continue;
    parts.push(k === "number" ? `#${String(v)}` : `${k}: ${typeof v === "object" ? JSON.stringify(v) : String(v)}`);
  }
  return parts.join(" · ");
}

/** Admin → Audit log: filter by action, actor, repo and date; export CSV or NDJSON. */
export function AuditLogPanel() {
  const { t } = useTranslation();
  const { data: repos } = useRepos();
  const people = useQuery({ queryKey: ["admin-users", ""], queryFn: async () => (await unwrap(api.GET("/admin/users", { params: { query: {} } }))).items });
  const [f, setF] = useState<Filters>({ action: ANY, actor: ANY, repo: ANY, from: "", to: "" });
  const query = useMemo(() => toQuery(f), [f]);
  const list = useInfiniteQuery({
    queryKey: ["audit", query],
    queryFn: ({ pageParam }) => unwrap(api.GET("/admin/audit", { params: { query: { ...query, ...(pageParam ? { cursor: pageParam } : {}) } } })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const [actions, setActions] = useState<string[]>([]);
  const first = list.data?.pages[0]?.actions;
  // Remember every action seen, so the filter keeps its choices once narrowed.
  if (first && first.some((a) => !actions.includes(a))) setActions([...new Set([...actions, ...first])].sort());
  const families = [...new Set(actions.map((a) => a.split(".")[0] + "."))];
  const items = list.data?.pages.flatMap((p) => p.items) ?? [];
  const exportHref = (format: "csv" | "ndjson") => "/api/v1/admin/audit/export?" + new URLSearchParams({ format, ...query }).toString();
  const set = (k: keyof Filters) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const filtered = f.action !== ANY || f.actor !== ANY || f.repo !== ANY || f.from || f.to;
  return (
    <Panel
      title={t("admin.audit")}
      desc={t("admin.auditDesc")}
      actions={
        <div className="flex gap-2">
          <Button asChild size="sm" variant="outline">
            <a href={exportHref("csv")} download>
              <Download />
              CSV
            </a>
          </Button>
          <Button asChild size="sm" variant="outline">
            <a href={exportHref("ndjson")} download>
              <Download />
              NDJSON
            </a>
          </Button>
        </div>
      }
    >
      <div className="flex flex-wrap items-end gap-2">
        <Select value={f.action} onValueChange={set("action")}>
          <SelectTrigger size="sm" className="w-52" aria-label={t("auditLog.action")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ANY}>{t("auditLog.anyAction")}</SelectItem>
            {families.map((fam) => (
              <SelectItem key={fam} value={fam}>
                {t("auditLog.family", { family: fam.slice(0, -1) })}
              </SelectItem>
            ))}
            {actions.map((a) => (
              <SelectItem key={a} value={a}>
                <code className="font-mono text-[0.75rem]">{a}</code>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={f.actor} onValueChange={set("actor")}>
          <SelectTrigger size="sm" className="w-48" aria-label={t("auditLog.actor")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ANY}>{t("auditLog.anyone")}</SelectItem>
            <SelectItem value="system">{t("auditLog.actors.system")}</SelectItem>
            <SelectItem value="agent_key">{t("auditLog.actors.agent_key")}</SelectItem>
            <SelectItem value="assistant">{t("auditLog.actors.assistant")}</SelectItem>
            {(people.data ?? []).map((u) => (
              <SelectItem key={u.id} value={`user:${u.id}`}>
                {u.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={f.repo} onValueChange={set("repo")}>
          <SelectTrigger size="sm" className="w-44" aria-label={t("auditLog.repo")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ANY}>{t("auditLog.anyRepo")}</SelectItem>
            {(repos ?? []).map((r) => (
              <SelectItem key={r.id} value={r.id}>
                {r.slug}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <label className="grid gap-0.5 text-[0.71875rem] text-muted-foreground">
          {t("auditLog.from")}
          <Input type="date" value={f.from} onChange={(e) => set("from")(e.target.value)} className="h-8 w-36 text-[0.8125rem]" />
        </label>
        <label className="grid gap-0.5 text-[0.71875rem] text-muted-foreground">
          {t("auditLog.to")}
          <Input type="date" value={f.to} onChange={(e) => set("to")(e.target.value)} className="h-8 w-36 text-[0.8125rem]" />
        </label>
        {filtered && (
          <Button size="sm" variant="ghost" onClick={() => setF({ action: ANY, actor: ANY, repo: ANY, from: "", to: "" })}>
            <X />
            {t("auditLog.clear")}
          </Button>
        )}
      </div>
      <div className="overflow-x-auto rounded-xl border focus-visible:outline-2 focus-visible:outline-ring" tabIndex={0} role="region" aria-label={t("admin.audit")}>
        <table className="w-full text-[0.8125rem]">
          <thead className="text-left text-[0.78125rem] text-muted-foreground">
            <tr className="border-b">
              <th className="px-3 py-2.5 font-medium">{t("admin.time")}</th>
              <th className="px-3 py-2.5 font-medium">{t("auditLog.actor")}</th>
              <th className="px-3 py-2.5 font-medium">{t("admin.action")}</th>
              <th className="px-3 py-2.5 font-medium max-md:hidden">{t("auditLog.details")}</th>
            </tr>
          </thead>
          <tbody>
            {items.map((e) => (
              <tr key={e.id} className="border-b align-top last:border-0">
                <td className="px-3 py-2 whitespace-nowrap text-muted-foreground tabular-nums">
                  <Time iso={e.at} />
                </td>
                <td className="px-3 py-2">
                  <span className="block max-w-44 truncate">{e.actor_name || t(`auditLog.actors.${e.actor_type}`, { defaultValue: e.actor_type })}</span>
                  {(e.actor_type !== "user" || e.ip) && (
                    <span className="block text-[0.71875rem] text-muted-foreground">
                      {e.actor_type !== "user" && t(`auditLog.actors.${e.actor_type}`, { defaultValue: e.actor_type })}
                      {e.actor_type !== "user" && e.ip && " · "}
                      {e.ip}
                    </span>
                  )}
                </td>
                <td className="px-3 py-2">
                  <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.71875rem]">{e.action}</code>
                </td>
                <td className="px-3 py-2 text-muted-foreground max-md:hidden">
                  {e.repo_name && <span className="text-foreground">{e.repo_name} </span>}
                  {details(e) || [e.target_type, e.target_id].filter(Boolean).join(" ")}
                </td>
              </tr>
            ))}
            {list.data && items.length === 0 && (
              <tr>
                <td colSpan={4} className="px-3 py-8 text-center text-muted-foreground">
                  {t("auditLog.none")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {list.isLoading && <Loader2 className="mx-auto size-4 animate-spin text-muted-foreground" />}
      {list.hasNextPage && (
        <Button variant="outline" className="justify-self-center" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
          {list.isFetchingNextPage && <Loader2 className="animate-spin" />}
          {t("auditLog.more")}
        </Button>
      )}
    </Panel>
  );
}
