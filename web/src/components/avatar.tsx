import { cn } from "@/lib/utils";

const COLORS = ["#e11d48", "#2563eb", "#059669", "#d97706", "#7c3aed", "#0891b2", "#db2777", "#4f46e5", "#65a30d", "#ea580c", "#0d9488", "#9333ea"];

/** Stable presence color per user id. */
export function userColor(id: string): string {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return COLORS[h % COLORS.length]!;
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0]!.slice(0, 2).toUpperCase();
  return (parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase();
}

const SIZES = { xs: "size-[18px] text-[8px]", sm: "size-5 text-[8.5px]", md: "size-7 text-[10.5px]", lg: "size-9 text-[12.5px]" };

/** ring: outline color (presence on the same page). */
export function Avatar({ name, id, size = "md", className, ring }: { name: string; id: string; size?: keyof typeof SIZES; className?: string; ring?: string }) {
  return (
    <span
      role="img"
      aria-label={name}
      title={name}
      style={{ background: userColor(id), ...(ring ? { boxShadow: `0 0 0 2px var(--background), 0 0 0 4px ${ring}` } : {}) }}
      className={cn("inline-grid shrink-0 place-items-center rounded-full font-semibold tracking-wide text-white", SIZES[size], className)}
    >
      {initials(name)}
    </span>
  );
}
