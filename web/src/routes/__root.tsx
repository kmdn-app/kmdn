import { Outlet, createRootRouteWithContext } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Toaster } from "@/components/ui/sonner";
import { ThemeProvider } from "@/lib/theme";

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: Root,
});

function Root() {
  return (
    <ThemeProvider>
      <TooltipProvider delayDuration={300}>
        <Outlet />
        <Toaster position="bottom-right" />
      </TooltipProvider>
    </ThemeProvider>
  );
}
