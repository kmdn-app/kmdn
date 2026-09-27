import { cn } from "@/lib/utils";

const sizes = { sm: "size-[1.375rem] rounded-md text-xs", md: "size-7 rounded-[0.4375rem] text-sm", lg: "size-11 rounded-[0.6875rem] text-xl" };

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
