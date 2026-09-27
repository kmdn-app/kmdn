import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Avatar, userColor } from "@/components/avatar";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useMe } from "@/lib/api";
import { useAnnounceChanges } from "@/lib/announce";
import type { RepoView } from "@/lib/repos";
import { usePresence, type RevisionView } from "@/lib/revisions";

/** Scrolls to a collaborator's caret on this page (CollaborationCaret labels carry their name). */
function scrollToCaret(name: string): boolean {
  for (const el of document.querySelectorAll<HTMLElement>(".collaboration-carets__label")) {
    if (el.textContent === name) {
      el.scrollIntoView({ block: "center", behavior: "smooth" });
      return true;
    }
  }
  return false;
}

/**
 * Everyone with a page of the revision open. People on this page get a ring
 * in their color; clicking jumps to their caret, or opens the page they're on.
 */
export function PresenceStack({ repo, rev, path }: { repo: RepoView; rev: RevisionView; path: string }) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const { data: people } = usePresence(rev);
  const navigate = useNavigate();
  const others = (people ?? []).filter((p) => p.id !== me?.id);
  useAnnounceChanges(
    people && me ? others : undefined,
    (p) => p.id,
    (p) => t("presence.joined", { name: p.name }),
    (p) => t("presence.left", { name: p.name }),
  );
  if (others.length === 0) return null;
  const shown = others.slice(0, 4);
  return (
    <div className="flex shrink-0 items-center -space-x-1.5 max-sm:hidden" aria-label={t("presence.label", { count: others.length })}>
      {shown.map((p) => {
        const here = p.paths.includes(path);
        return (
          <Tooltip key={p.id}>
            <TooltipTrigger asChild>
              <button
                type="button"
                className="rounded-full"
                onClick={() => {
                  if (here && scrollToCaret(p.name)) return;
                  const to = p.paths[0];
                  if (to) void navigate({ to: "/$owner/$repo/$", params: { owner: repo.owner, repo: repo.name, _splat: to }, search: { revision: rev.number } });
                }}
              >
                <Avatar
                  name={p.name}
                  id={p.id}
                  size="md"
                  className={here ? undefined : "ring-2 ring-background"}
                  ring={here ? userColor(p.id) : undefined}
                />
              </button>
            </TooltipTrigger>
            <TooltipContent>
              {p.name} · {here ? t("presence.here") : (p.paths[0] ?? "").split("/").pop()}
              {!p.editing && ` · ${t("presence.viewing")}`}
            </TooltipContent>
          </Tooltip>
        );
      })}
      {others.length > shown.length && <span className="grid size-7 place-items-center rounded-full bg-muted text-[0.6875rem] font-medium ring-2 ring-background">+{others.length - shown.length}</span>}
    </div>
  );
}
