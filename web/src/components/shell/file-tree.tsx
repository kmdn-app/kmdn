import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronDown, ChevronRight, FileText, Folder, Image as ImageIcon } from "lucide-react";
import type { RepoView, TreeNode } from "@/lib/repos";
import { cn } from "@/lib/utils";

type Node = TreeNode & { op?: string };
type Dir = { name: string; path: string; dirs: Map<string, Dir>; files: Node[] };

/** One-letter marks for files a revision touches. */
export const OP_MARK: Record<string, { letter: string; cls: string }> = {
  modify: { letter: "M", cls: "text-warning" },
  add: { letter: "A", cls: "text-success" },
  rename: { letter: "R", cls: "text-sky-500" },
  delete: { letter: "D", cls: "text-destructive" },
};

function build(nodes: Node[], root: string): Dir {
  const top: Dir = { name: "", path: root, dirs: new Map(), files: [] };
  for (const n of nodes) {
    if (n.type !== "file") continue;
    const rel = root && n.path.startsWith(root + "/") ? n.path.slice(root.length + 1) : n.path;
    const parts = rel.split("/");
    let d = top;
    let acc = root;
    for (const part of parts.slice(0, -1)) {
      acc = acc ? `${acc}/${part}` : part;
      let next = d.dirs.get(part);
      if (!next) {
        next = { name: part, path: acc, dirs: new Map(), files: [] };
        d.dirs.set(part, next);
      }
      d = next;
    }
    d.files.push(n);
  }
  return top;
}

/** Content tree of the repo in the sidebar. Markdown first; images shown muted. */
export function FileTree({
  repo,
  nodes,
  root,
  current,
  revision,
  onNavigate,
}: {
  repo: RepoView;
  nodes: Node[];
  root: string;
  current?: string;
  /** Links open pages in this revision. */
  revision?: number;
  onNavigate?: () => void;
}) {
  const { t } = useTranslation();
  const tree = useMemo(() => build(nodes, root), [nodes, root]);
  const [open, setOpen] = useState<Set<string>>(() => {
    const s = new Set<string>();
    if (current) {
      const parts = current.split("/");
      for (let i = 1; i < parts.length; i++) s.add(parts.slice(0, i).join("/"));
    }
    return s;
  });
  const toggle = (p: string) =>
    setOpen((prev) => {
      const n = new Set(prev);
      if (n.has(p)) n.delete(p);
      else n.add(p);
      return n;
    });

  const render = (d: Dir, depth: number) => (
    <>
      {[...d.dirs.values()]
        .sort((a, b) => a.name.localeCompare(b.name))
        .map((sub) => {
          const isOpen = open.has(sub.path) || (!!current && current.startsWith(sub.path + "/"));
          return (
            <li key={sub.path}>
              <button
                type="button"
                onClick={() => toggle(sub.path)}
                aria-expanded={isOpen}
                className="flex h-[30px] w-full items-center gap-1.5 rounded-[7px] pr-2 text-left text-[13px] hover:bg-sidebar-accent"
                style={{ paddingLeft: 8 + depth * 14 }}
              >
                {isOpen ? <ChevronDown className="size-3 text-muted-foreground" /> : <ChevronRight className="size-3 text-muted-foreground" />}
                <Folder className="size-3.5 text-muted-foreground" />
                <span className="truncate">{sub.name}</span>
              </button>
              {isOpen && <ul>{render(sub, depth + 1)}</ul>}
            </li>
          );
        })}
      {d.files
        .slice()
        .sort((a, b) => Number(!a.markdown) - Number(!b.markdown) || a.name.localeCompare(b.name))
        .map((f) => {
          const active = f.path === current;
          const Icon = f.markdown ? FileText : ImageIcon;
          const cls = cn(
            "flex h-[30px] items-center gap-1.5 rounded-[7px] pr-2 text-[13px] hover:bg-sidebar-accent",
            active && "bg-sidebar-accent font-medium",
            !f.markdown && "text-muted-foreground",
          );
          const style = { paddingLeft: 8 + depth * 14 + 18 };
          return (
            <li key={f.path}>
              {f.markdown ? (
            <Link
              to="/$owner/$repo/$"
              params={{ owner: repo.owner, repo: repo.name, _splat: f.path }}
              search={revision ? { revision } : {}}
              onClick={onNavigate}
              className={cls}
              style={style}
              aria-current={active ? "page" : undefined}
            >
              <Icon className="size-3.5 shrink-0 text-muted-foreground" />
              <span className="truncate">{f.name}</span>
              {f.op && OP_MARK[f.op] && <span className={cn("ml-auto font-mono text-[11px] font-semibold", OP_MARK[f.op]!.cls)}>{OP_MARK[f.op]!.letter}</span>}
            </Link>
          ) : (
            <div className={cls} style={style} title={f.path}>
              <Icon className="size-3.5 shrink-0" />
              <span className="truncate">{f.name}</span>
            </div>
              )}
            </li>
          );
        })}
    </>
  );
  // Nested lists of disclosure buttons and links: every item is reachable
  // with Tab, which a role="tree" would require roving focus for.
  return <ul aria-label={t("shell.files")}>{render(tree, 0)}</ul>;
}
