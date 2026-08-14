import { X } from "lucide-react";
import {
  createContext,
  use,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

import { cn } from "@/lib/utils";

export type ToastTone = "info" | "ok" | "warn" | "danger";

export interface Toast {
  id: string;
  tone: ToastTone;
  title: string;
  detail?: string;
  /** Milliseconds before auto-dismiss. Errors default to staying put. */
  duration?: number;
}

interface ToastContextValue {
  push: (toast: Omit<Toast, "id">) => string;
  dismiss: (id: string) => void;
}

const ToastContext = createContext<ToastContextValue | null>(null);

let counter = 0;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>());

  const dismiss = useCallback((id: string) => {
    const timer = timers.current.get(id);
    if (timer) {
      clearTimeout(timer);
      timers.current.delete(id);
    }
    setToasts((current) => current.filter((toast) => toast.id !== id));
  }, []);

  const push = useCallback(
    (toast: Omit<Toast, "id">) => {
      counter += 1;
      const id = `toast-${counter}`;
      setToasts((current) => [...current, { ...toast, id }]);

      // Anything the operator must act on stays until they dismiss it. A
      // rejected order that vanishes after four seconds is a rejected order
      // nobody saw.
      const duration = toast.duration ?? (toast.tone === "danger" ? 0 : 6000);
      if (duration > 0) {
        timers.current.set(
          id,
          setTimeout(() => dismiss(id), duration),
        );
      }
      return id;
    },
    [dismiss],
  );

  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const timer of pending.values()) clearTimeout(timer);
      pending.clear();
    };
  }, []);

  const value = useMemo(() => ({ push, dismiss }), [push, dismiss]);

  const polite = toasts.filter((toast) => toast.tone !== "danger");
  const assertive = toasts.filter((toast) => toast.tone === "danger");

  return (
    <ToastContext value={value}>
      {children}
      {/* Two regions, because aria-live cannot be switched on an existing one
       * and be relied upon to announce. Neither takes focus: the operator is
       * mid-order and stealing it would be worse than the message. */}
      <div className="pointer-events-none fixed right-3 bottom-3 z-50 flex w-80 max-w-[calc(100vw-1.5rem)] flex-col gap-2">
        <ToastRegion toasts={assertive} live="assertive" role="alert" onDismiss={dismiss} />
        <ToastRegion toasts={polite} live="polite" role="status" onDismiss={dismiss} />
      </div>
    </ToastContext>
  );
}

function ToastRegion({
  toasts,
  live,
  role,
  onDismiss,
}: {
  toasts: Toast[];
  live: "polite" | "assertive";
  role: "status" | "alert";
  onDismiss: (id: string) => void;
}) {
  return (
    <div role={role} aria-live={live} aria-atomic="false" className="flex flex-col gap-2">
      {toasts.map((toast) => (
        <div
          key={toast.id}
          className={cn(
            "pointer-events-auto flex items-start gap-2 rounded-md border bg-overlay p-3 shadow-overlay",
            toast.tone === "danger" && "border-danger/50",
            toast.tone === "warn" && "border-warn/50",
            toast.tone === "ok" && "border-ok/50",
            toast.tone === "info" && "border-line-strong",
          )}
        >
          <span
            aria-hidden="true"
            className={cn(
              "mt-1.5 size-1.5 shrink-0 rounded-full",
              toast.tone === "danger" && "bg-danger",
              toast.tone === "warn" && "bg-warn",
              toast.tone === "ok" && "bg-ok",
              toast.tone === "info" && "bg-dim",
            )}
          />
          <div className="min-w-0 flex-1">
            <p className="text-base text-text">{toast.title}</p>
            {toast.detail ? <p className="mt-0.5 text-meta text-dim">{toast.detail}</p> : null}
          </div>
          <button
            type="button"
            onClick={() => onDismiss(toast.id)}
            aria-label="Dismiss notification"
            className="-m-1 rounded-sm p-1 text-dim hover:text-text"
          >
            <X className="size-3.5" aria-hidden="true" />
          </button>
        </div>
      ))}
    </div>
  );
}

export function useToast(): ToastContextValue {
  const value = use(ToastContext);
  if (!value) {
    throw new Error("useToast must be used inside <ToastProvider>");
  }
  return value;
}
