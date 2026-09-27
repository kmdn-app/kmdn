import { useMemo } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Bell, CheckCheck } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { notificationHref, useMarkRead, useNotifications, type Notification } from "@/lib/notifications";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/inbox")({ component: Inbox });

type Group = { key: string; title: string; subtitle: string; items: Notification[] };

/** Grouped per revision (or per page for discussions), newest group first. */
function group(items: Notification[]): Group[] {
  const out = new Map<string, Group>();
  for (const n of items) {
    const key = n.revision_id || `${n.repo_id}:${n.data.path ?? n.thread_id ?? n.id}`;
    let g = out.get(key);
    if (!g) {
      g = { key, title: n.data.title || n.data.path || "", subtitle: [n.data.owner && `${n.data.owner}/${n.data.repo}`, n.data.number && `#${n.data.number}`].filter(Boolean).join(" · "), items: [] };
      out.set(key, g);
    }
    g.items.push(n);
  }
  return [...out.values()];
}

function Inbox() {
  const { t } = useTranslation();
  const q = useNotifications();
  const read = useMarkRead();
  const navigate = useNavigate();
  const groups = useMemo(() => group(q.data?.items ?? []), [q.data]);
  const open = (n: Notification) => {
    if (!n.read_at) read.mutate([n.id]);
    const href = notificationHref(n);
    if (href) void navigate({ href });
  };
  return (
    <AppShell>
      {(controls) => (
        <>
          <TopBar
            controls={controls}
            title={t("inbox.title")}
            actions={
              (q.data?.unread ?? 0) > 0 && (
                <Button size="sm" variant="ghost" onClick={() => read.mutate("all")} disabled={read.isPending}>
                  <CheckCheck />
                  {t("inbox.markAll")}
                </Button>
              )
            }
          />
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="mx-auto grid max-w-[47.5rem] gap-5 px-6 pt-7 pb-20 max-md:px-4">
              {q.isLoading && <Skeleton className="h-24 w-full" />}
              {q.data && groups.length === 0 && (
                <div className="flex flex-col items-center py-20 text-center text-muted-foreground">
                  <Bell className="mb-3 size-6" />
                  <p>{t("inbox.empty")}</p>
                </div>
              )}
              {groups.map((g) => (
                <section key={g.key} className="overflow-hidden rounded-xl border">
                  <header className="flex items-baseline gap-2 border-b bg-muted/40 px-4 py-2.5">
                    <h2 className="truncate text-[0.84375rem] font-semibold">{g.title}</h2>
                    <span className="shrink-0 text-[0.75rem] text-muted-foreground">{g.subtitle}</span>
                    {g.items.length > 1 && <span className="ml-auto shrink-0 text-[0.75rem] text-muted-foreground">{t("inbox.updates", { count: g.items.length })}</span>}
                  </header>
                  <ul className="divide-y">
                    {g.items.map((n) => (
                      <li key={n.id}>
                        <button type="button" onClick={() => open(n)} className="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-accent/50">
                          <span className={cn("mt-2 size-1.5 shrink-0 rounded-full", n.read_at ? "bg-transparent" : "bg-primary")} aria-label={n.read_at ? undefined : t("inbox.unread")} />
                          {n.actor_id ? <Avatar name={n.actor_name ?? "?"} id={n.actor_id} size="sm" /> : <Bell className="mt-0.5 size-4 text-muted-foreground" />}
                          <span className="min-w-0 flex-1">
                            <span className={cn("block text-[0.84375rem]", !n.read_at && "font-medium")}>{t(`inbox.kinds.${n.kind}`, { defaultValue: n.kind, actor: n.actor_name || t("revision.someone"), ...n.data })}</span>
                            {n.data.excerpt && <span className="mt-0.5 line-clamp-2 block text-[0.78125rem] text-muted-foreground">{n.data.excerpt}</span>}
                          </span>
                          <Time iso={n.created_at} className="shrink-0 text-xs whitespace-nowrap text-muted-foreground" />
                        </button>
                      </li>
                    ))}
                  </ul>
                </section>
              ))}
            </div>
          </div>
        </>
      )}
    </AppShell>
  );
}
