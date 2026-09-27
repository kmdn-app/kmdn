import { cn } from "@/lib/utils";

const sizes = { sm: "size-[22px] rounded-md text-xs", md: "size-7 rounded-[7px] text-sm", lg: "size-11 rounded-[11px] text-xl" };

export function Logo({ size = "md", className }: { size?: keyof typeof sizes; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "inline-grid shrink-0 place-items-center bg-primary font-mono font-semibold tracking-tighter text-primary-foreground",
        sizes[size],
        className,
      )}
    >
      k
    </span>
  );
}
