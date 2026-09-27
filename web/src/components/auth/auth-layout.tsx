import type { ReactNode } from "react";
import { Logo } from "@/components/logo";

export function AuthLayout({ children, footer }: { children: ReactNode; footer?: ReactNode }) {
  return (
    <div className="relative flex min-h-full items-center justify-center bg-sidebar px-4 py-10">
      <div className="w-full max-w-[25rem] rounded-xl border bg-card p-8 shadow-[0_0.375rem_1rem_-0.25rem_rgb(9_9_11/0.1),0_2px_0.25rem_-2px_rgb(9_9_11/0.06)] max-sm:p-6">
        <div className="flex justify-center">
          <Logo size="lg" />
        </div>
        {children}
      </div>
      {footer && <div className="absolute inset-x-0 bottom-3.5 text-center text-xs text-muted-foreground">{footer}</div>}
    </div>
  );
}
