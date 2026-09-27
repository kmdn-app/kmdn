import { useEffect, useRef } from "react";
import { forceCenter, forceCollide, forceLink, forceManyBody, forceSimulation, type SimulationLinkDatum, type SimulationNodeDatum } from "d3-force";

export type GNode = { path: string; title: string; folder: string; in: number; out: number; orphan?: boolean; changed?: boolean; missing?: boolean };
export type GEdge = { from: string; to: string; count: number; broken?: boolean };

type SimNode = GNode & SimulationNodeDatum;
type SimLink = SimulationLinkDatum<SimNode> & { broken?: boolean };

/** Distinct, theme-friendly folder colors (the order folders sort in picks them). */
export const FOLDER_COLORS = ["#6366f1", "#0ea5e9", "#10b981", "#f59e0b", "#ec4899", "#8b5cf6", "#14b8a6", "#f97316", "#84cc16", "#e11d48"];

const radius = (n: GNode) => (n.missing ? 3 : 4 + Math.sqrt(n.in) * 2.2);

function loadPositions(key: string): Record<string, [number, number]> {
  try {
    return JSON.parse(localStorage.getItem(key) ?? "{}") as Record<string, [number, number]>;
  } catch {
    return {};
  }
}

function savePositions(key: string, nodes: SimNode[]) {
  try {
    const out: Record<string, [number, number]> = {};
    for (const n of nodes) if (!n.missing && n.x !== undefined && n.y !== undefined) out[n.path] = [Math.round(n.x), Math.round(n.y)];
    localStorage.setItem(key, JSON.stringify(out));
  } catch {
    /* storage unavailable */
  }
}

/**
 * The repository's link graph, force-laid-out in the browser and drawn on a
 * canvas (thousands of pages stay smooth). Wheel zooms, dragging the
 * background pans, dragging a page moves it, clicking one selects it.
 */
export function GraphCanvas({
  nodes,
  edges,
  colorOf,
  match,
  selected,
  onSelect,
  cacheKey,
}: {
  nodes: GNode[];
  edges: GEdge[];
  colorOf: (folder: string) => string;
  /** Search: pages that match are shown, the others dimmed (null: no search). */
  match: ((n: GNode) => boolean) | null;
  selected: string | null;
  onSelect: (path: string | null) => void;
  /** Where laid-out positions are remembered (per repository and scope). */
  cacheKey: string;
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const live = useRef({ match, selected, colorOf, onSelect, hover: null as string | null });
  const draw = useRef<(() => void) | null>(null);
  useEffect(() => {
    live.current.match = match;
    live.current.selected = selected;
    live.current.colorOf = colorOf;
    live.current.onSelect = onSelect;
    draw.current?.();
  });

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const cached = loadPositions(cacheKey);
    const byPath = new Map<string, SimNode>();
    const simNodes: SimNode[] = nodes.map((n) => {
      const at = cached[n.path];
      const s: SimNode = { ...n, ...(at ? { x: at[0], y: at[1] } : {}) };
      byPath.set(n.path, s);
      return s;
    });
    // Broken links point at pages that don't exist: small ghost nodes.
    for (const e of edges) {
      if (e.broken && !byPath.has(e.to)) {
        const ghost: SimNode = { path: e.to, title: e.to.split("/").pop() ?? e.to, folder: "", in: 0, out: 0, missing: true };
        byPath.set(e.to, ghost);
        simNodes.push(ghost);
      }
    }
    const links: SimLink[] = edges.filter((e) => byPath.has(e.from) && byPath.has(e.to)).map((e) => ({ source: e.from, target: e.to, broken: e.broken }));
    const neighbors = new Map<string, Set<string>>();
    for (const e of edges) {
      if (!neighbors.has(e.from)) neighbors.set(e.from, new Set());
      if (!neighbors.has(e.to)) neighbors.set(e.to, new Set());
      neighbors.get(e.from)!.add(e.to);
      neighbors.get(e.to)!.add(e.from);
    }

    const view = { k: 1, x: 0, y: 0 };
    let width = 0;
    let height = 0;
    const dpr = window.devicePixelRatio || 1;
    const styles = getComputedStyle(canvas);
    const fg = styles.getPropertyValue("--foreground").trim() || "#111";
    const muted = styles.getPropertyValue("--muted-foreground").trim() || "#888";
    const border = styles.getPropertyValue("--border").trim() || "#ddd";
    const bg = styles.getPropertyValue("--background").trim() || "#fff";
    const danger = styles.getPropertyValue("--destructive").trim() || "#e11d48";

    const render = () => {
      const { match: m, selected: sel, colorOf: color, hover } = live.current;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, width, height);
      ctx.translate(width / 2 + view.x, height / 2 + view.y);
      ctx.scale(view.k, view.k);
      const focus = hover ?? sel;
      const near = focus ? (neighbors.get(focus) ?? new Set<string>()) : null;
      const lit = (n: SimNode) => (!m || m(n)) && (!near || n.path === focus || near.has(n.path));
      for (const l of links) {
        const s = l.source as SimNode;
        const t = l.target as SimNode;
        const on = lit(s) && lit(t) && (!focus || s.path === focus || t.path === focus);
        ctx.globalAlpha = on ? (focus ? 0.9 : 0.45) : 0.08;
        ctx.strokeStyle = l.broken ? danger : focus && on ? fg : border;
        ctx.lineWidth = (l.broken ? 1.2 : 1) / view.k;
        ctx.setLineDash(l.broken ? [4 / view.k, 3 / view.k] : []);
        ctx.beginPath();
        ctx.moveTo(s.x!, s.y!);
        ctx.lineTo(t.x!, t.y!);
        ctx.stroke();
      }
      ctx.setLineDash([]);
      for (const n of simNodes) {
        const r = radius(n);
        ctx.globalAlpha = lit(n) ? 1 : 0.15;
        ctx.beginPath();
        ctx.arc(n.x!, n.y!, r, 0, Math.PI * 2);
        ctx.fillStyle = n.missing ? bg : color(n.folder);
        ctx.fill();
        if (n.missing || n.orphan) {
          ctx.setLineDash([2.5 / view.k, 2 / view.k]);
          ctx.strokeStyle = n.missing ? danger : muted;
          ctx.lineWidth = 1.4 / view.k;
          ctx.stroke();
          ctx.setLineDash([]);
        }
        if (n.changed || n.path === sel) {
          ctx.beginPath();
          ctx.arc(n.x!, n.y!, r + 3 / view.k, 0, Math.PI * 2);
          ctx.strokeStyle = n.path === sel ? fg : "#f59e0b";
          ctx.lineWidth = 1.6 / view.k;
          ctx.stroke();
        }
      }
      // Labels: when zoomed in, and for the focused page and its neighbours or search hits.
      ctx.font = `${12 / view.k}px ui-sans-serif, system-ui, sans-serif`;
      ctx.textAlign = "center";
      ctx.textBaseline = "top";
      for (const n of simNodes) {
        const show = simNodes.length <= 80 || view.k > 1.6 || n.path === focus || (near?.has(n.path) ?? false) || (m ? m(n) && simNodes.length < 400 : false);
        if (!show || !lit(n)) continue;
        ctx.globalAlpha = 1;
        ctx.fillStyle = n.missing ? danger : fg;
        ctx.fillText(n.title, n.x!, n.y! + radius(n) + 3 / view.k);
      }
      ctx.globalAlpha = 1;
    };
    draw.current = render;

    const resize = () => {
      const rect = canvas.getBoundingClientRect();
      width = rect.width;
      height = rect.height;
      canvas.width = Math.round(width * dpr);
      canvas.height = Math.round(height * dpr);
      render();
    };
    const ro = new ResizeObserver(resize);
    ro.observe(canvas);
    resize();

    const known = simNodes.filter((n) => n.x !== undefined).length;
    const sim = forceSimulation(simNodes)
      .force("link", forceLink<SimNode, SimLink>(links).id((d) => d.path).distance(46).strength(0.35))
      .force("charge", forceManyBody().strength(simNodes.length > 800 ? -28 : -70))
      .force("center", forceCenter(0, 0))
      .force("collide", forceCollide<SimNode>((d) => radius(d) + 2))
      .alpha(known === simNodes.length ? 0.08 : 1)
      .on("tick", render)
      .on("end", () => savePositions(cacheKey, simNodes));

    // Interaction.
    const toWorld = (cx: number, cy: number) => ({ x: (cx - width / 2 - view.x) / view.k, y: (cy - height / 2 - view.y) / view.k });
    const hit = (cx: number, cy: number) => {
      const p = toWorld(cx, cy);
      let best: SimNode | null = null;
      let bestD = Infinity;
      for (const n of simNodes) {
        const d = Math.hypot(n.x! - p.x, n.y! - p.y);
        if (d < radius(n) + 4 / view.k && d < bestD) {
          best = n;
          bestD = d;
        }
      }
      return best;
    };
    let drag: { node: SimNode | null; sx: number; sy: number; vx: number; vy: number; moved: boolean } | null = null;
    const local = (e: PointerEvent | WheelEvent) => {
      const r = canvas.getBoundingClientRect();
      return { cx: e.clientX - r.left, cy: e.clientY - r.top };
    };
    const down = (e: PointerEvent) => {
      const { cx, cy } = local(e);
      const node = hit(cx, cy);
      drag = { node, sx: cx, sy: cy, vx: view.x, vy: view.y, moved: false };
      canvas.setPointerCapture(e.pointerId);
      if (node) {
        node.fx = node.x;
        node.fy = node.y;
      }
    };
    const move = (e: PointerEvent) => {
      const { cx, cy } = local(e);
      if (!drag) {
        const h = hit(cx, cy)?.path ?? null;
        if (h !== live.current.hover) {
          live.current.hover = h;
          canvas.style.cursor = h ? "pointer" : "grab";
          render();
        }
        return;
      }
      if (Math.hypot(cx - drag.sx, cy - drag.sy) > 3) drag.moved = true;
      if (drag.node) {
        const p = toWorld(cx, cy);
        drag.node.fx = p.x;
        drag.node.fy = p.y;
        sim.alphaTarget(0.2).restart();
      } else {
        view.x = drag.vx + cx - drag.sx;
        view.y = drag.vy + cy - drag.sy;
        render();
      }
    };
    const up = (e: PointerEvent) => {
      if (!drag) return;
      const d = drag;
      drag = null;
      canvas.releasePointerCapture(e.pointerId);
      if (d.node) {
        d.node.fx = null;
        d.node.fy = null;
        sim.alphaTarget(0);
      }
      if (!d.moved) live.current.onSelect(d.node && !d.node.missing ? d.node.path : null);
    };
    const wheel = (e: WheelEvent) => {
      e.preventDefault();
      const { cx, cy } = local(e);
      const before = toWorld(cx, cy);
      view.k = Math.min(6, Math.max(0.15, view.k * Math.exp(-e.deltaY * 0.0015)));
      view.x = cx - width / 2 - before.x * view.k;
      view.y = cy - height / 2 - before.y * view.k;
      render();
    };
    canvas.addEventListener("pointerdown", down);
    canvas.addEventListener("pointermove", move);
    canvas.addEventListener("pointerup", up);
    canvas.addEventListener("wheel", wheel, { passive: false });
    canvas.style.cursor = "grab";
    return () => {
      sim.stop();
      savePositions(cacheKey, simNodes);
      ro.disconnect();
      canvas.removeEventListener("pointerdown", down);
      canvas.removeEventListener("pointermove", move);
      canvas.removeEventListener("pointerup", up);
      canvas.removeEventListener("wheel", wheel);
      draw.current = null;
    };
  }, [nodes, edges, cacheKey]);

  return <canvas ref={canvasRef} className="size-full touch-none" role="img" aria-label="Link graph" />;
}
