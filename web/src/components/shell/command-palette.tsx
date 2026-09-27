import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { FileText, Hash, Settings, Shield, UserRound } from "lucide-react";
import { CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { useMe } from "@/lib/api";
import { useSearch, useTree, type RepoView } from "@/lib/repos";
import { useCurrentOrg } from "@/lib/orgs";

type PaletteState = { open: () => void };
const PaletteContext = createContext<PaletteState>({ open: () => {} });

export function usePalette() {
  return useContext(PaletteContext);
}

/** Renders snippet text with \u0002…\u0003 match markers as <mark>. */
export function Snippet({ text }: { text: string }) {
  // The search API marks matches with STX/ETX control characters.
  // eslint-disable-next-line no-control-regex
  const parts = text.split(/(\u0002[^\u0003]*\u0003)/);
  return (
    <>
      {parts.map((p, i) =>
        p.startsWith("\u0002") ? (
          <mark key={i} className="rounded-sm bg-warning/30 px-0.5 text-inherit">
            {p.slice(1, -1)}
          </mark>
        ) : (
          <span key={i}>{p}</span>
        ),
      )}
    </>
  );
}

/** ⌘K palette: pages by title, full-text hits with headings, and actions. */
export function CommandPaletteProvider({ repo, children }: { repo?: RepoView; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  // cmdk keeps the highlighted item across queries when it isn't filtering, so
  // highlight the first result unless the user has moved since the last keystroke.
  const [picked, setPicked] = useState("");
  const navigate = useNavigate();
  const { data: me } = useMe();
  const org = useCurrentOrg();
  const canAdmin = !!me?.is_instance_admin || org?.role === "admin" || org?.role === "owner";
  const { data: tree } = useTree(repo);
  const { data: hits, isFetching } = useSearch(repo, q);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((v) => !v);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const pages = useMemo(() => {
    const files = (tree?.items ?? []).filter((n) => n.type === "file" && n.markdown);
    const needle = q.trim().toLowerCase();
    return (needle ? files.filter((f) => f.path.toLowerCase().includes(needle)) : files).slice(0, 8);
  }, [tree, q]);

  const actions = [...(repo ? ["action:settings"] : []), "action:profile", ...(canAdmin ? ["action:admin"] : [])];
  const values = [
    ...(repo ? pages.map((p) => "page:" + p.path) : []),
    ...(repo ? (hits ?? []).map((h) => "hit:" + h.path + h.heading_slug) : []),
    ...actions,
  ];
  const selected = values.includes(picked) ? picked : (values[0] ?? "");

  const go = useCallback(
    (to: string) => {
      setOpen(false);
      setQ("");
      void navigate({ to });
    },
    [navigate],
  );
  const openPage = (path: string, hash?: string) => repo && go(`/${repo.org_slug}/${repo.owner}/${repo.name}/${path}${hash ? `#${hash}` : ""}`);

  return (
    <PaletteContext.Provider value={{ open: () => setOpen(true) }}>
      {children}
      <CommandDialog open={open} onOpenChange={setOpen} title="Search" description="Search pages and actions" shouldFilter={false} value={selected} onValueChange={setPicked}>
        <CommandInput
          placeholder={repo ? `Search ${repo.display_name}…` : "Type a command…"}
          value={q}
          onValueChange={(v) => {
            setQ(v);
            setPicked("");
          }}
        />
        <CommandList>
          <CommandEmpty>{isFetching ? "Searching…" : "No results."}</CommandEmpty>
          {repo && pages.length > 0 && (
            <CommandGroup heading="Pages">
              {pages.map((p) => (
                <CommandItem key={p.path} value={"page:" + p.path} onSelect={() => openPage(p.path)}>
                  <FileText />
                  <span className="truncate">{p.path}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          {repo && (hits?.length ?? 0) > 0 && (
            <CommandGroup heading="Matches">
              {hits!.map((h) => (
                <CommandItem key={h.path + (h.heading_slug ?? "")} value={"hit:" + h.path + h.heading_slug} onSelect={() => openPage(h.path, h.heading_slug)}>
                  {h.heading ? <Hash /> : <FileText />}
                  <span className="min-w-0 flex-1">
                    <b className="block truncate font-medium">
                      {h.title}
                      {h.heading && <span className="font-normal text-muted-foreground"> › {h.heading}</span>}
                    </b>
                    <small className="block truncate text-xs text-muted-foreground">
                      <Snippet text={h.snippet} />
                    </small>
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          <CommandGroup heading="Actions">
            {repo && (
              <CommandItem value="action:settings" onSelect={() => go(`/${repo.org_slug}/${repo.owner}/${repo.name}/settings`)}>
                <Settings />
                Repository settings
              </CommandItem>
            )}
            <CommandItem value="action:profile" onSelect={() => go("/settings/profile")}>
              <UserRound />
              Your account
            </CommandItem>
            {canAdmin && (
              <CommandItem value="action:admin" onSelect={() => go("/admin")}>
                <Shield />
                Admin console
              </CommandItem>
            )}
          </CommandGroup>
        </CommandList>
      </CommandDialog>
    </PaletteContext.Provider>
  );
}
