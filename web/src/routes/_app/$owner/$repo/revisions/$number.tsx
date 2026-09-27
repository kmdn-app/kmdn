import { useEffect, useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  Archive,
  ArchiveRestore,
  Bookmark,
  Eye,
  History as HistoryIcon,
  RotateCcw,
  TriangleAlert,
  CircleCheck,
  FilePen,
  FilePlus,
  FileText,
  GitPullRequest,
  Loader2,
  MoreHorizontal,
  Pencil,
  Trash2,
  UserPlus,
  X,
} from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { OP_MARK } from "@/components/shell/file-tree";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { StatePill } from "@/components/revision/revision-ui";
import { ReviewActions, ReviewersSection } from "@/components/revision/review-actions";
import { UpdatesBanner } from "@/components/revision/updates";
import { ReviewSummary } from "@/components/revision/review-summary";
import { RevisionConsistency } from "@/components/consistency/findings";
import { useIsPhone } from "@/lib/media";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap } from "@/lib/api";
import type { RepoView } from "@/lib/repos";
import { useRevision, useRevisionEvents, useRevisionFiles, type RevisionFile, type RevisionView } from "@/lib/revisions";
import { useRepo } from "@/lib/use-repo";
import { fileHref, rawUrl } from "@/lib/repos";
import { DocView } from "@/components/doc/doc-view";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/$owner/$repo/revisions/$number")({
  component: Overview,
});

function useInvalidate(repo: RepoView, rev?: RevisionView) {
  const qc = useQueryClient();
  return () => {
    if (!rev) return;
    void qc.invalidateQueries({ queryKey: ["revision", repo.id, rev.number] });
    void qc.invalidateQueries({ queryKey: ["revision-files", rev.id] });
    void qc.invalidateQueries({ queryKey: ["revision-tree", rev.id] });
    void qc.invalidateQueries({ queryKey: ["revision-events", rev.id] });
    void qc.invalidateQueries({ queryKey: ["revisions", repo.id] });
  };
}

function Overview() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { number } = Route.useParams();
  const rev = useRevision(repo, Number(number));
  useRevisionEvents(repo, rev.data);
  const r = rev.data;
  return (
    <AppShell repo={repo} revision={r}>
      {(controls) => (
        <>
          <TopBar controls={controls} title={r ? `#${r.number} ${r.title}` : t("revision.overview")} actions={r && <Actions repo={repo} rev={r} />} />
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="mx-auto grid max-w-[880px] gap-8 px-8 pt-7 pb-20 max-md:px-4">
              {rev.error && <p className="text-muted-foreground">{errorMessage(rev.error, t("errors.generic"))}</p>}
              {!r && !rev.error && <Skeleton className="h-24 w-full" />}
              {r && (
                <>
                  <Header repo={repo} rev={r} />
                  <UpdatesBanner rev={r} />
                  <Pages repo={repo} rev={r} />
                  <ReviewSummary repo={repo} rev={r} />
                  <Checks repo={repo} rev={r} />
                  <RevisionConsistency repo={repo} rev={r} />
                  <ReviewersSection repo={repo} rev={r} />
                  <People repo={repo} rev={r} />
                  <Checkpoints repo={repo} rev={r} />
                  <Activity rev={r} />
                </>
              )}
            </div>
          </div>
        </>
      )}
    </AppShell>
  );
}

function Actions({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const invalidate = useInvalidate(repo, rev);
  const act = useMutation({
    mutationFn: (op: "close" | "reopen") =>
      op === "close"
        ? unwrap(api.POST("/revisions/{revision}/close", { params: { path: { revision: rev.id } } }))
        : unwrap(api.POST("/revisions/{revision}/reopen", { params: { path: { revision: rev.id } } })),
    onSuccess: invalidate,
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  if (!rev.access.can_manage) return rev.access.can_review ? <ReviewActions repo={repo} rev={rev} /> : null;
  return rev.state === "closed" ? (
    <Button size="sm" variant="outline" onClick={() => act.mutate("reopen")} disabled={act.isPending}>
      <ArchiveRestore />
      {t("revision.reopen")}
    </Button>
  ) : (
    <>
      <Button size="sm" variant="ghost" onClick={() => act.mutate("close")} disabled={act.isPending || rev.state === "publishing"}>
        <Archive />
        <span className="max-md:hidden">{t("revision.close")}</span>
      </Button>
      <ReviewActions repo={repo} rev={rev} />
    </>
  );
}

function Header({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const invalidate = useInvalidate(repo, rev);
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState(rev.title);
  const [desc, setDesc] = useState(rev.description);
  const owner = rev.members.find((m) => m.role === "owner");
  const save = useMutation({
    mutationFn: () => unwrap(api.PATCH("/revisions/{revision}", { params: { path: { revision: rev.id } }, body: { title, description: desc } })),
    onSuccess: () => {
      setEditing(false);
      invalidate();
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const canEdit = rev.access.can_manage && rev.state !== "closed";
  const startEditing = () => {
    setTitle(rev.title);
    setDesc(rev.description);
    setEditing(true);
  };
  return (
    <section>
      {editing ? (
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <Input value={title} onChange={(e) => setTitle(e.target.value)} className="h-10 text-lg font-semibold" aria-label={t("revision.titleLabel")} autoFocus maxLength={200} />
          <Textarea value={desc} onChange={(e) => setDesc(e.target.value)} rows={4} aria-label={t("revision.descriptionLabel")} placeholder={t("revision.descriptionPlaceholder")} />
          <div className="flex gap-2">
            <Button type="submit" size="sm" disabled={!title.trim() || save.isPending}>
              {t("common.save")}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => {
                setEditing(false);
                setTitle(rev.title);
                setDesc(rev.description);
              }}
            >
              {t("common.cancel")}
            </Button>
          </div>
        </form>
      ) : (
        <>
          <div className="flex items-start gap-3">
            <h1 className="min-w-0 flex-1 text-2xl font-semibold tracking-tight text-balance">{rev.title}</h1>
            {canEdit && (
              <Button variant="ghost" size="icon" className="size-8" onClick={startEditing} aria-label={t("revision.editDetails")}>
                <Pencil />
              </Button>
            )}
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-2 text-[13px] text-muted-foreground">
            <StatePill rev={rev} />
            <span>
              #{rev.number} · {t("revision.startedBy", { name: owner?.name ?? "—" })} <Time iso={rev.created_at} />
            </span>
            {rev.change_request_url && (
              <a href={rev.change_request_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 hover:text-foreground hover:underline">
                <GitPullRequest className="size-3.5" />
                {t(`revision.changeRequest.${repo.forge_kind === "gitlab" ? "mr" : "pr"}${rev.change_request_draft ? "Draft" : ""}`)}
              </a>
            )}
          </div>
          {rev.description ? (
            <p className="mt-4 text-[14.5px] whitespace-pre-wrap">{rev.description}</p>
          ) : (
            canEdit && (
              <button type="button" onClick={startEditing} className="mt-4 text-[13.5px] text-muted-foreground hover:text-foreground">
                {t("revision.addDescription")}
              </button>
            )
          )}
          {rev.state === "published" && rev.published_sha && (
            <p className="mt-4 rounded-lg border border-success/30 bg-success/5 px-3 py-2 text-[13px]">
              {t("publish.done", { branch: repo.target_branch })}{" "}
              {commitURL(repo, rev.published_sha) ? (
                <a href={commitURL(repo, rev.published_sha)!} target="_blank" rel="noreferrer" className="font-mono underline">
                  {rev.published_sha.slice(0, 8)}
                </a>
              ) : (
                <code>{rev.published_sha.slice(0, 8)}</code>
              )}
            </p>
          )}
          {rev.state === "publishing" && rev.change_request_url && (
            <p className="mt-4 rounded-lg border px-3 py-2 text-[13px]">
              {t("publish.waitingForMerge")}{" "}
              <a href={rev.change_request_url} target="_blank" rel="noreferrer" className="underline">
                {t("publish.openThePR")}
              </a>
            </p>
          )}
          {rev.access.reason && rev.state !== "closed" && rev.state !== "published" && rev.state !== "publishing" && <p className="mt-4 rounded-lg border bg-muted/40 px-3 py-2 text-[13px]">{t(`revision.readOnly.${rev.access.reason}`, { defaultValue: t("revision.readOnly.generic"), editors: rev.members.map((m) => m.name.split(" ")[0]).join(", ") })}</p>}
        </>
      )}
    </section>
  );
}

function SectionTitle({ title, action }: { title: string; action?: React.ReactNode }) {
  return (
    <div className="mb-3 flex items-center justify-between gap-3">
      <h2 className="text-[15px] font-semibold">{title}</h2>
      {action}
    </div>
  );
}

function Pages({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const phone = useIsPhone();
  const files = useRevisionFiles(rev);
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState<RevisionFile | null>(null);
  const invalidate = useInvalidate(repo, rev);
  const del = useMutation({
    mutationFn: (path: string) => unwrap(api.POST("/revisions/{revision}/files", { params: { path: { revision: rev.id } }, body: { op: "delete", path } })),
    onSuccess: invalidate,
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  // Phones review; adding, renaming and deleting pages needs a bigger screen.
  const canEdit = rev.access.can_edit && !phone;
  return (
    <section>
      <SectionTitle
        title={t("revision.changed")}
        action={
          canEdit && (
            <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
              <FilePlus />
              {t("revision.addPage")}
            </Button>
          )
        }
      />
      <div className="overflow-hidden rounded-xl border">
        {files.data?.length === 0 && (
          <p className="p-6 text-center text-[13.5px] text-muted-foreground">
            {canEdit ? t("revision.noChangesHint") : t("revision.noChanges")}
          </p>
        )}
        {files.data?.map((f) => {
          const mark = OP_MARK[f.op];
          return (
            <div key={f.id} className="flex items-center gap-3 border-b px-4 py-2.5 last:border-b-0">
              <span className={cn("w-3 text-center font-mono text-[12px] font-semibold", mark?.cls)} title={t(`revision.ops.${f.op}`)}>
                {mark?.letter}
              </span>
              <FileText className="size-4 shrink-0 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                {f.op === "delete" ? (
                  <span className="truncate text-[13.5px] text-muted-foreground line-through">{f.path}</span>
                ) : (
                  <Link
                    to="/$owner/$repo/$"
                    params={{ owner: repo.owner, repo: repo.name, _splat: f.path }}
                    search={{ revision: rev.number }}
                    className="truncate text-[13.5px] font-medium hover:underline"
                  >
                    {f.path}
                  </Link>
                )}
                {f.from_path && <div className="truncate text-[12px] text-muted-foreground">{t("revision.renamedFrom", { path: f.from_path })}</div>}
              </div>
              <span className="font-mono text-[12px] tabular-nums">
                {f.additions > 0 && <span className="text-success">+{f.additions}</span>}
                {f.deletions > 0 && <span className="ml-1.5 text-destructive">−{f.deletions}</span>}
              </span>
              {canEdit && f.op !== "delete" && (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="icon" className="size-7" aria-label={t("revision.pageActions", { path: f.path })}>
                      <MoreHorizontal />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onSelect={() => setRenaming(f)}>
                      <FilePen />
                      {t("revision.renameOrMove")}
                    </DropdownMenuItem>
                    <DropdownMenuItem className="text-destructive focus:text-destructive" onSelect={() => del.mutate(f.path)}>
                      <Trash2 />
                      {f.op === "add" ? t("revision.discardPage") : t("revision.deletePage")}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
            </div>
          );
        })}
      </div>
      {adding && <AddPageDialog repo={repo} rev={rev} onClose={() => setAdding(false)} />}
      {renaming && <RenameDialog repo={repo} rev={rev} file={renaming} onClose={() => setRenaming(null)} />}
    </section>
  );
}

function AddPageDialog({ repo, rev, onClose }: { repo: RepoView; rev: RevisionView; onClose: () => void }) {
  const { t } = useTranslation();
  const root = repo.scope.root;
  // The input holds the path inside the content folder; the folder is a fixed prefix.
  const [rel, setRel] = useState("");
  const path = (root ? root + "/" : "") + rel.replace(/^\/+/, "");
  const [template, setTemplate] = useState("");
  const templates = useQuery({ queryKey: ["templates", repo.id], queryFn: async () => (await unwrap(api.GET("/repos/{repo}/templates", { params: { path: { repo: repo.id } } }))).items });
  const invalidate = useInvalidate(repo, rev);
  const full = /\.(md|markdown|mdx)$/i.test(path) ? path : path + ".md";
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/revisions/{revision}/files", { params: { path: { revision: rev.id } }, body: { op: "add", path: full, ...(template ? { template } : {}) } })),
    onSuccess: () => {
      invalidate();
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("revision.addPage")}</DialogTitle>
          <DialogDescription>{t("revision.addPageDesc")}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            add.mutate();
          }}
        >
          <div className="grid gap-1.5">
            <Label htmlFor="new-path">{t("revision.pagePath")}</Label>
            <div className="flex items-center rounded-md border border-input shadow-xs focus-within:ring-[3px] focus-within:ring-ring/50">
              {root && <span className="pl-3 font-mono text-[13px] text-muted-foreground select-none">{root}/</span>}
              <input
                id="new-path"
                value={rel}
                onChange={(e) => setRel(e.target.value)}
                placeholder="guides/new-page"
                className="h-9 min-w-0 flex-1 bg-transparent px-1 font-mono text-[13px] outline-none first:pl-3"
                autoFocus
              />
            </div>
            <p className="text-[12px] text-muted-foreground">{t("revision.willCreate", { path: full })}</p>
          </div>
          {(templates.data?.length ?? 0) > 0 && (
            <div className="grid gap-1.5">
              <Label htmlFor="new-template">{t("revision.template")}</Label>
              <select id="new-template" value={template} onChange={(e) => setTemplate(e.target.value)} className="h-9 rounded-md border border-input bg-background px-3 text-sm shadow-xs">
                <option value="">{t("revision.blankPage")}</option>
                {templates.data!.map((tpl) => (
                  <option key={tpl.path} value={tpl.path}>
                    {tpl.name.replace(/\.(md|markdown|mdx)$/i, "")}
                  </option>
                ))}
              </select>
            </div>
          )}
          {add.error && <p className="text-[13px] text-destructive">{errorMessage(add.error, t("errors.generic"))}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={add.isPending || !rel.trim() || rel.endsWith("/")}>
              {add.isPending && <Loader2 className="animate-spin" />}
              {t("revision.addPage")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RenameDialog({ repo, rev, file, onClose }: { repo: RepoView; rev: RevisionView; file: RevisionFile; onClose: () => void }) {
  const { t } = useTranslation();
  const [path, setPath] = useState(file.path);
  const [debounced, setDebounced] = useState(file.path);
  const [updateLinks, setUpdateLinks] = useState(true);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(path), 300);
    return () => clearTimeout(id);
  }, [path]);
  const invalidate = useInvalidate(repo, rev);
  const preview = useQuery({
    queryKey: ["rename-preview", rev.id, file.path, debounced],
    queryFn: async () =>
      (await unwrap(api.POST("/revisions/{revision}/files/rename-preview", { params: { path: { revision: rev.id } }, body: { from: file.path, to: debounced } }))).items,
    enabled: debounced !== file.path && /\.(md|markdown|mdx)$/i.test(debounced),
  });
  const rewrites = preview.data ?? [];
  const pages = new Set(rewrites.map((r) => r.path)).size;
  const rename = useMutation({
    mutationFn: async () => {
      await unwrap(api.POST("/revisions/{revision}/files", { params: { path: { revision: rev.id } }, body: { op: "rename", from_path: file.path, path } }));
      if (updateLinks && rewrites.length > 0) {
        await unwrap(api.POST("/revisions/{revision}/links/rewrite", { params: { path: { revision: rev.id } }, body: { from: file.path, to: path } }));
      }
    },
    onSuccess: () => {
      invalidate();
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("revision.renameOrMove")}</DialogTitle>
          <DialogDescription>{t("revision.renameDesc")}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            rename.mutate();
          }}
        >
          <Input value={path} onChange={(e) => setPath(e.target.value)} className="font-mono text-[13px]" autoFocus aria-label={t("revision.pagePath")} />
          {rewrites.length > 0 && (
            <div className="rounded-lg border">
              <label className="flex items-center gap-2 border-b px-3 py-2 text-[13px] font-medium">
                <input type="checkbox" checked={updateLinks} onChange={(e) => setUpdateLinks(e.target.checked)} className="accent-primary" />
                {t("revision.updateLinks", { count: rewrites.length, pages })}
              </label>
              <ul className="max-h-40 overflow-auto px-3 py-2 font-mono text-[11.5px] text-muted-foreground">
                {rewrites.map((r, i) => (
                  <li key={i} className="truncate" title={`${r.path}:${r.line}`}>
                    {r.path}:{r.line} <span className="line-through">{r.before}</span> → <span className="text-foreground">{r.after}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {rename.error && <p className="text-[13px] text-destructive">{errorMessage(rename.error, t("errors.generic"))}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={rename.isPending || path === file.path}>
              {rename.isPending && <Loader2 className="animate-spin" />}
              {t("common.rename")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function People({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const invalidate = useInvalidate(repo, rev);
  const found = useQuery({
    queryKey: ["user-search", q],
    queryFn: async () => (await unwrap(api.GET("/users", { params: { query: { q } } }))).items,
    enabled: q.trim().length >= 2,
  });
  const add = useMutation({
    mutationFn: (id: string) => unwrap(api.PUT("/revisions/{revision}/members/{user}", { params: { path: { revision: rev.id, user: id } } })),
    onSuccess: () => {
      setQ("");
      invalidate();
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const remove = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/revisions/{revision}/members/{user}", { params: { path: { revision: rev.id, user: id } } })),
    onSuccess: invalidate,
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const ids = new Set(rev.members.map((m) => m.user_id));
  const manage = rev.access.can_manage && rev.state !== "closed";
  return (
    <section>
      <SectionTitle title={t("revision.people")} />
      <div className="overflow-hidden rounded-xl border">
        {rev.members.map((m) => (
          <div key={m.user_id} className="flex items-center gap-3 border-b px-4 py-2.5 last:border-b-0">
            <Avatar name={m.name} id={m.user_id} />
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13.5px] font-medium">{m.name}</div>
              <div className="truncate text-[12px] text-muted-foreground">{m.email}</div>
            </div>
            <span className="text-[12px] text-muted-foreground">{t(`revision.roles.${m.role}`)}</span>
            {manage && m.role !== "owner" && (
              <Button variant="ghost" size="icon" className="size-7" onClick={() => remove.mutate(m.user_id)} aria-label={t("revision.removePerson", { name: m.name })}>
                <X />
              </Button>
            )}
          </div>
        ))}
        {manage && (
          <div className="relative border-t px-4 py-2.5">
            <div className="flex items-center gap-2">
              <UserPlus className="size-4 text-muted-foreground" />
              <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("revision.addEditor")} className="h-8 border-0 px-1 shadow-none focus-visible:ring-0" aria-label={t("revision.addEditor")} />
            </div>
            {(found.data?.length ?? 0) > 0 && (
              <div className="absolute inset-x-2 top-full z-10 mt-1 overflow-hidden rounded-lg border bg-popover shadow-md">
                {found.data!
                  .filter((u) => !ids.has(u.id))
                  .map((u) => (
                    <button key={u.id} type="button" onClick={() => add.mutate(u.id)} className="flex w-full items-center gap-2.5 px-3 py-2 text-left hover:bg-accent">
                      <Avatar name={u.name} id={u.id} size="sm" />
                      <span className="text-[13.5px]">{u.name}</span>
                      <span className="text-[12px] text-muted-foreground">{u.email}</span>
                    </button>
                  ))}
              </div>
            )}
          </div>
        )}
      </div>
    </section>
  );
}

function Activity({ rev }: { rev: RevisionView }) {
  const { t } = useTranslation();
  const events = useQuery({
    queryKey: ["revision-events", rev.id, rev.updated_at],
    queryFn: async () => (await unwrap(api.GET("/revisions/{revision}/events", { params: { path: { revision: rev.id } } }))).items,
  });
  return (
    <section>
      <SectionTitle title={t("revision.activity")} />
      <ol className="grid gap-3 border-l pl-5">
        {(events.data ?? [])
          .slice()
          .reverse()
          .map((e) => (
            <li key={e.id} className="relative text-[13.5px]">
              <span className="absolute top-2 -left-[23px] size-1.5 rounded-full bg-border ring-4 ring-background" />
              <span className="font-medium">{e.actor_name || t("revision.someone")}</span>{" "}
              {t(`revision.events.${e.kind}`, { defaultValue: e.kind, path: String((e.data as { path?: string }).path ?? ""), from: String((e.data as { from_path?: string }).from_path ?? "") })}{" "}
              <span className="text-muted-foreground">
                · <Time iso={e.created_at} />
              </span>
            </li>
          ))}
      </ol>
    </section>
  );
}

function Checkpoints({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [viewing, setViewing] = useState<string | null>(null);
  const list = useQuery({
    queryKey: ["checkpoints", rev.id, rev.updated_at],
    queryFn: async () => (await unwrap(api.GET("/revisions/{revision}/checkpoints", { params: { path: { revision: rev.id } } }))).items,
  });
  const invalidate = useInvalidate(repo, rev);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["checkpoints", rev.id] });
    invalidate();
  };
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/revisions/{revision}/checkpoints", { params: { path: { revision: rev.id } }, body: { name } })),
    onSuccess: () => {
      setName("");
      refresh();
      toast.success(t("checkpoints.named"));
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const restore = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/revisions/{revision}/checkpoints/{checkpoint}/restore", { params: { path: { revision: rev.id, checkpoint: id } } })),
    onSuccess: () => {
      refresh();
      toast.success(t("checkpoints.restored"));
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const canEdit = rev.access.can_edit;
  return (
    <section>
      <SectionTitle title={t("checkpoints.title")} />
      <div className="overflow-hidden rounded-xl border">
        {canEdit && (
          <form
            className="flex items-center gap-2 border-b px-4 py-2.5"
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
          >
            <Bookmark className="size-4 text-muted-foreground" />
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("checkpoints.namePlaceholder")} className="h-8 border-0 px-1 shadow-none focus-visible:ring-0" maxLength={200} aria-label={t("checkpoints.name")} />
            <Button type="submit" size="sm" variant="outline" disabled={!name.trim() || create.isPending}>
              {t("checkpoints.save")}
            </Button>
          </form>
        )}
        {list.data?.length === 0 && <p className="p-6 text-center text-[13.5px] text-muted-foreground">{t("checkpoints.none")}</p>}
        {list.data?.map((c) => (
          <div key={c.id} className="flex items-center gap-3 border-b px-4 py-2.5 last:border-b-0">
            {c.kind === "named" ? <Bookmark className="size-4 text-foreground" /> : <HistoryIcon className="size-4 text-muted-foreground" />}
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13.5px] font-medium">{c.name || t(`checkpoints.kinds.${c.kind}`)}</div>
              <div className="text-[12px] text-muted-foreground">
                {c.created_by_name && `${c.created_by_name} · `}
                <Time iso={c.created_at} /> · {t("revision.pages", { count: c.file_count })}
              </div>
            </div>
            <Button variant="ghost" size="sm" onClick={() => setViewing(c.id)}>
              <Eye />
              <span className="max-md:hidden">{t("checkpoints.view")}</span>
            </Button>
            {canEdit && (
              <Button variant="ghost" size="sm" onClick={() => restore.mutate(c.id)} disabled={restore.isPending}>
                <RotateCcw />
                <span className="max-md:hidden">{t("checkpoints.restore")}</span>
              </Button>
            )}
          </div>
        ))}
      </div>
      {viewing && <CheckpointDialog repo={repo} rev={rev} id={viewing} onClose={() => setViewing(null)} />}
    </section>
  );
}

function CheckpointDialog({ repo, rev, id, onClose }: { repo: RepoView; rev: RevisionView; id: string; onClose: () => void }) {
  const { t } = useTranslation();
  const files = useQuery({
    queryKey: ["checkpoint-files", id],
    queryFn: async () => (await unwrap(api.GET("/revisions/{revision}/checkpoints/{checkpoint}/files", { params: { path: { revision: rev.id, checkpoint: id } } }))).items,
  });
  const [path, setPath] = useState<string | null>(null);
  const shown = path ?? files.data?.find((f) => f.op !== "delete")?.path ?? null;
  const file = useQuery({
    queryKey: ["checkpoint-file", id, shown],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/checkpoints/{checkpoint}/files/{path}", { params: { path: { revision: rev.id, checkpoint: id, path: shown! } } })),
    enabled: !!shown,
  });
  const ctx = { path: shown ?? "", pageHref: (p: string) => fileHref(repo, p), imageSrc: (p: string) => rawUrl(repo, p) };
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-hidden sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t("checkpoints.viewTitle")}</DialogTitle>
          <DialogDescription>{t("checkpoints.viewDesc")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-wrap gap-1">
          {files.data?.map((f) => (
            <button
              key={f.path}
              type="button"
              disabled={f.op === "delete"}
              onClick={() => setPath(f.path)}
              className={cn("rounded-md border px-2 py-1 font-mono text-[12px]", f.path === shown && "bg-accent", f.op === "delete" && "line-through opacity-60")}
            >
              {OP_MARK[f.op]?.letter} {f.path}
            </button>
          ))}
        </div>
        <div className="min-h-0 overflow-auto rounded-lg border" style={{ maxHeight: "55vh" }}>
          {file.data ? <DocView markdown={file.data.content ?? ""} ctx={ctx} className="!py-6" /> : <Skeleton className="m-6 h-24" />}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Checks({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const checks = useQuery({
    queryKey: ["revision-checks", rev.id, rev.updated_at],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/checks", { params: { path: { revision: rev.id } } })),
  });
  const files = useRevisionFiles(rev);
  if (!checks.data) return null;
  const { broken_links: broken, breaks_inbound: inbound } = checks.data;
  const conflicted = (files.data ?? []).filter((f) => f.has_conflicts);
  const page = (p: string, line: number) => (
    <Link to="/$owner/$repo/$" params={{ owner: repo.owner, repo: repo.name, _splat: p }} search={{ revision: rev.number }} className="font-medium hover:underline">
      {p}:{line}
    </Link>
  );
  return (
    <section>
      <SectionTitle title={t("revision.checks")} />
      <div className="overflow-hidden rounded-xl border text-[13.5px]">
        {conflicted.map((f) => (
          <p key={"c" + f.path} className="flex items-start gap-2 border-b px-4 py-2.5">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-destructive" />
            <span>
              <Link to="/$owner/$repo/$" params={{ owner: repo.owner, repo: repo.name, _splat: f.path }} search={{ revision: rev.number }} className="font-medium hover:underline">
                {f.path}
              </Link>{" "}
              {f.conflict === "deleted_upstream" ? t("updates.conflict.pageDeleted") : t("revision.hasConflicts")}
            </span>
          </p>
        ))}
        {broken.length === 0 && inbound.length === 0 ? (
          <p className="flex items-center gap-2 px-4 py-3 text-muted-foreground">
            <CircleCheck className="size-4 text-success" />
            {t("revision.linksOk")}
          </p>
        ) : (
          <ul>
            {broken.map((b, i) => (
              <li key={"b" + i} className="flex items-start gap-2 border-b px-4 py-2.5 last:border-b-0">
                <TriangleAlert className="mt-0.5 size-4 shrink-0 text-destructive" />
                <span>
                  {page(b.path, b.line)} {t("revision.brokenLink", { url: b.url })} <span className="text-muted-foreground">({t(`links.reasons.${b.reason}`)})</span>
                </span>
              </li>
            ))}
            {inbound.map((b, i) => (
              <li key={"i" + i} className="flex items-start gap-2 border-b px-4 py-2.5 last:border-b-0">
                <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
                <span>
                  {page(b.path, b.line)} {t("revision.breaksInbound", { target: b.target })}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

/** Link to a commit on the forge (none for plain git remotes). */
function commitURL(repo: RepoView, sha: string): string | null {
  if (!repo.web_url) return null;
  if (repo.forge_kind === "github") return `${repo.web_url}/commit/${sha}`;
  if (repo.forge_kind === "gitlab") return `${repo.web_url}/-/commit/${sha}`;
  return null;
}
