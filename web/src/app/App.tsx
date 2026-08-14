import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { useState } from "react";

import { createQueryClient } from "@/app/query";
import { createAppRouter } from "@/app/router";
import { sessionStore } from "@/app/session";
import { ThemeProvider } from "@/app/theme";
import { ToastProvider } from "@/components/ui/toast";

/* ---------------------------------------------------------------------------
 * The composition root.
 *
 * Router and query client are built by factories held in `useState`, not by
 * module-level singletons: a singleton survives between tests in the same file
 * and carries one test's cache into the next. `useState` with an initialiser
 * runs the factory exactly once per mount, which is what both production and a
 * test that renders `<App />` twice actually want.
 *
 * Provider order matters. `ThemeProvider` writes the class on <html> and so is
 * outermost; the router is innermost because everything else is a service it
 * consumes — query cache errors reach the session store, and toasts fired from
 * a route need a provider already above the outlet.
 * ------------------------------------------------------------------------- */

export function App() {
  const [{ queryClient, router }] = useState(() => {
    const queryClient = createQueryClient();
    return { queryClient, router: createAppRouter({ session: sessionStore, queryClient }) };
  });

  return (
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </QueryClientProvider>
    </ThemeProvider>
  );
}
