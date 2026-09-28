import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { routeTree } from "./routeTree.gen";
import { applyAppearance, storedAppearance } from "./theme";
import { retryDelay, retryQuery } from "./lib/api";
import "./styles.css";
import "./i18n";

applyAppearance(storedAppearance());

const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 30_000, retry: retryQuery, retryDelay } },
});

const router = createRouter({ routeTree, context: { queryClient }, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
