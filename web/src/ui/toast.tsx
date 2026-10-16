import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";

export interface ToastInput {
  kind: "long" | "short" | "info" | "error";
  title: string;
  detail?: string;
}

interface Toast extends ToastInput {
  id: number;
}

const ToastContext = createContext<{ push: (toast: ToastInput) => void } | null>(null);
let nextId = 1;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const push = useCallback((input: ToastInput) => {
    const toast = { ...input, id: nextId++ };
    setToasts((current) => [...current.slice(-3), toast]);
    setTimeout(() => setToasts((current) => current.filter((item) => item.id !== toast.id)), input.kind === "error" ? 8000 : 4000);
  }, []);
  const value = useMemo(() => ({ push }), [push]);
  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="fixed bottom-8 left-3 z-50 flex w-80 flex-col gap-2" role="status" aria-live="polite">
        {toasts.map((toast) => (
          <div key={toast.id} className="rounded-lg border border-line bg-raised px-3 py-2 shadow-lg">
            <div className={toast.kind === "long" ? "text-up" : toast.kind === "short" || toast.kind === "error" ? "text-down" : "text-text"}>{toast.title}</div>
            {toast.detail && <div className="mt-0.5 text-muted">{toast.detail}</div>}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast() {
  const value = useContext(ToastContext);
  if (!value) throw new Error("ToastProvider missing");
  return value;
}
