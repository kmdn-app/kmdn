import type { ReactNode } from "react";
import { Link, type LinkProps } from "@tanstack/react-router";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

export type SettingsSection = { key: string; label: string; icon: LucideIcon; danger?: boolean };

/** Side-nav settings page: sections are selected with the `section` search param. */
export function SettingsLayout({ title, sections, active, children, linkProps }: { title: string; sections: SettingsSection[]; active: string; children: ReactNode; linkProps: (key: string) => LinkProps }) {
  return (
    // Container queries: the main column narrows when the side panel is open.
    <div className="@container">
    <div className="mx-auto grid max-w-[1080px] grid-cols-[200px_minmax(0,1fr)] gap-10 px-8 pt-7 pb-20 @max-2xl:grid-cols-1 @max-2xl:gap-4 @max-2xl:px-4">
      <nav aria-label={title} className="flex flex-col gap-0.5 self-start @2xl:sticky @2xl:top-0 @max-2xl:flex-row @max-2xl:flex-wrap">
        <div className="px-2.5 pb-2 text-xs font-medium text-muted-foreground @max-2xl:hidden">{title}</div>
        {sections.map((s) => (
          <Link
            key={s.key}
            {...linkProps(s.key)}
            aria-current={active === s.key ? "page" : undefined}
            className={cn(
              "flex h-8 items-center gap-2 rounded-[7px] px-2.5 text-[13.5px] whitespace-nowrap text-muted-foreground hover:bg-accent hover:text-foreground [&_svg]:size-3.5",
              active === s.key && "bg-accent font-medium text-foreground",
              s.danger && "text-destructive hover:text-destructive",
            )}
          >
            <s.icon />
            {s.label}
          </Link>
        ))}
      </nav>
      <div className="min-w-0">{children}</div>
    </div>
    </div>
  );
}

export function Panel({ title, desc, children, actions }: { title: string; desc?: ReactNode; children: ReactNode; actions?: ReactNode }) {
  return (
    <section>
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
          {desc && <p className="mt-1 text-[13.5px] text-muted-foreground">{desc}</p>}
        </div>
        {actions}
      </div>
      <div className="grid gap-4">{children}</div>
    </section>
  );
}

export function Card({ children, footer, className }: { children: ReactNode; footer?: ReactNode; className?: string }) {
  return (
    <div className={cn("rounded-xl border bg-card shadow-xs", className)}>
      <div className="grid gap-4 p-5">{children}</div>
      {footer && <div className="flex items-center justify-end gap-2 border-t px-5 py-3">{footer}</div>}
    </div>
  );
}

export function Row({ title, desc, children }: { title: string; desc?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b py-3.5 last:border-b-0 first:pt-0 last:pb-0">
      <div>
        <div className="text-sm font-medium">{title}</div>
        {desc && <div className="mt-0.5 text-[13px] text-muted-foreground">{desc}</div>}
      </div>
      {children}
    </div>
  );
}
