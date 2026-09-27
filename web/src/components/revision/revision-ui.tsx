import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Check, ChevronsUpDown, Globe, List, Loader2, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { errorMessage } from "@/lib/api";
import { atLeast, type RepoView } from "@/lib/repos";
import { useCreateRevision, useRevisions, type RevisionEvent, type RevisionState, type RevisionView } from "@/lib/revisions";
import { cn } from "@/lib/utils";
import { Time } from "@/components/time";

export const STATE_DOT: Record<RevisionState, string> = {
  editing: "bg-sky-500",
  in_review: "bg-warning",
  approved: "bg-success",
  publishing: "bg-success",
  published: "bg-muted-foreground",
  closed: "bg-muted-foreground/60",
};

export function StateDot({ state, className }: { state: RevisionState; className?: string }) {
  return <span className={cn("inline-block size-1.5 shrink-0 rounded-full", STATE_DOT[state], className)} />;
}

/** "Editing", "In review", …, plus the Changes requested / Conflict flags. */
export function StatePill({ rev, className }: { rev: RevisionView; className?: string }) {
  const { t } = useTranslation();
  return (
    <span className={cn("inline-flex items-center gap-1.5", className)}>
      <span className="inline-flex h-6 items-center gap-1.5 rounded-full border px-2 text-[0.75rem] font-medium whitespace-nowrap">
        <StateDot state={rev.state} />
        {t(`revision.state.${rev.state}`)}
      </span>
      {rev.changes_requested && (
        <span className="inline-flex h-6 items-center rounded-full bg-warning/15 px-2 text-[0.75rem] font-medium whitespace-nowrap text-warning">{t("revision.changesRequested")}</span>
      )}
      {rev.has_conflicts && (
        <span className="inline-flex h-6 items-center rounded-full bg-destructive/10 px-2 text-[0.75rem] font-medium whitespace-nowrap text-destructive">{t("revision.conflict")}</span>
      )}
    </span>
  );
}

/**
 * One line of a revision's Activity. kmdn's own events (the branch, the pull
 * request, review and publish state) have no actor: their text is a sentence.
 */
export function ActivityItem({ event: e }: { event: RevisionEvent }) {
  const { t } = useTranslation();
  const data = e.data as { path?: string; from_path?: string; branch?: string };
  const system = e.actor_type === "system";
  // A published event without an actor is the forge merging the pull request.
  const kind = system && e.kind === "published" ? "published_merged" : e.kind;
  const text = t(`revision.events.${kind}`, {
    defaultValue: e.kind,
    path: data.path ?? "",
    from: data.from_path ?? "",
    branch: data.branch ?? "",
  });
  return (
    <li className="relative text-[0.84375rem]">
      <span className="absolute top-2 -left-[1.4375rem] size-1.5 rounded-full bg-border ring-4 ring-background" />
      {!system && (
        <>
          <span className="font-medium">{e.actor_name || t("revision.someone")}</span>{" "}
        </>
      )}
      {text}{" "}
      <span className="text-muted-foreground">
        · <Time iso={e.created_at} />
      </span>
    </li>
  );
}

export function NewRevisionDialog({ repo, open, onOpenChange }: { repo: RepoView; open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const create = useCreateRevision(repo);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const submit = () =>
    create.mutate(
      { title, description },
      {
        onSuccess: (rev) => {
          onOpenChange(false);
          setTitle("");
          setDescription("");
          void navigate({ to: "/$owner/$repo/revisions/$number", params: { owner: repo.owner, repo: repo.name, number: String(rev.number) } });
        },
        onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
      },
    );
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("revision.newTitle")}</DialogTitle>
          <DialogDescription>{t("revision.newDesc")}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <div className="grid gap-1.5">
            <Label htmlFor="rev-title">{t("revision.titleLabel")}</Label>
            <Input id="rev-title" autoFocus value={title} onChange={(e) => setTitle(e.target.value)} placeholder={t("revision.titlePlaceholder")} maxLength={200} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="rev-desc">{t("revision.descriptionLabel")}</Label>
            <Textarea id="rev-desc" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} placeholder={t("revision.descriptionPlaceholder")} />
          </div>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={!title.trim() || create.isPending}>
              {create.isPending && <Loader2 className="animate-spin" />}
              {t("revision.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Decides where edits go and what the sidebar shows (docs/specs/02-ux.md#contextual-sidebar).
 * Switching keeps the page you're on when there is one.
 */
export function RevisionPicker({ repo, revision, currentPath, onNew }: { repo: RepoView; revision?: RevisionView; currentPath?: string; onNew: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const mine = useRevisions(repo, { mine: true });
  const canCreate = atLeast(repo.role, "contributor");
  const go = (n?: number) => {
    if (currentPath) void navigate({ to: "/$owner/$repo/$", params: { owner: repo.owner, repo: repo.name, _splat: currentPath }, search: n ? { revision: n } : {} });
    else if (n) void navigate({ to: "/$owner/$repo/revisions/$number", params: { owner: repo.owner, repo: repo.name, number: String(n) } });
    else void navigate({ to: "/$owner/$repo", params: { owner: repo.owner, repo: repo.name } });
  };
  const others = (mine.data ?? []).filter((r) => r.id !== revision?.id);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="flex h-8 w-full items-center gap-2 rounded-lg border bg-background px-2.5 text-left text-[0.8125rem] font-medium shadow-xs hover:bg-accent"
          aria-label={t("revision.picker")}
        >
          {revision ? <StateDot state={revision.state} /> : <span className="size-1.5 rounded-full bg-success" />}
          <span className="min-w-0 flex-1 truncate">
            {revision ? (
              <>
                <span className="font-normal text-muted-foreground">#{revision.number}</span> {revision.title}
              </>
            ) : (
              <>
                {t("shell.published")} <span className="font-normal text-muted-foreground">· {repo.target_branch}</span>
              </>
            )}
          </span>
          <ChevronsUpDown className="size-3.5 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuItem onSelect={() => go()}>
          <Globe />
          {t("shell.published")}
          {!revision && <Check className="ml-auto" />}
        </DropdownMenuItem>
        {revision && (
          <DropdownMenuItem onSelect={() => go(revision.number)}>
            <StateDot state={revision.state} className="mx-[0.3125rem]" />
            <span className="truncate">{revision.title}</span>
            <Check className="ml-auto" />
          </DropdownMenuItem>
        )}
        {others.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-xs text-muted-foreground">{t("revision.yours")}</DropdownMenuLabel>
            {others.slice(0, 8).map((r) => (
              <DropdownMenuItem key={r.id} onSelect={() => go(r.number)}>
                <StateDot state={r.state} className="mx-[0.3125rem]" />
                <span className="truncate">{r.title}</span>
                <span className="ml-auto text-xs text-muted-foreground">#{r.number}</span>
              </DropdownMenuItem>
            ))}
          </>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => void navigate({ to: "/$owner/$repo/revisions", params: { owner: repo.owner, repo: repo.name } })}>
          <List />
          {t("revision.all")}
        </DropdownMenuItem>
        {canCreate && (
          <DropdownMenuItem onSelect={onNew}>
            <Plus />
            {t("revision.newTitle")}
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
