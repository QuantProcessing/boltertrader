import * as RadixDialog from "@radix-ui/react-dialog";
import type { ButtonHTMLAttributes, ReactNode } from "react";

export function cx(...classes: Array<string | false | null | undefined>) {
  return classes.filter(Boolean).join(" ");
}

export function PrimaryButton({ className, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      {...props}
      className={cx(
        "h-10 w-full rounded-lg bg-accent text-sm text-on-accent transition-colors hover:bg-accent-hover",
        "disabled:cursor-not-allowed disabled:bg-accent-soft disabled:text-muted",
        className,
      )}
    >
      {children}
    </button>
  );
}

export function GhostButton({ className, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button {...props} className={cx("h-8 rounded-lg bg-raised px-3 text-text hover:bg-line-strong/40 disabled:opacity-50", className)}>
      {children}
    </button>
  );
}

/** Underline tabs, as in the book / ticket / blotter headers. */
export function Tabs<T extends string>({ value, options, onChange, className }: {
  value: T;
  options: Array<{ value: T; label: ReactNode }>;
  onChange: (value: T) => void;
  className?: string;
}) {
  return (
    <div role="tablist" className={cx("flex h-9 shrink-0 items-stretch border-b border-line", className)}>
      {options.map((option) => (
        <button
          key={option.value}
          role="tab"
          aria-selected={value === option.value}
          onClick={() => onChange(option.value)}
          className={cx(
            "-mb-px border-b px-3 whitespace-nowrap transition-colors",
            value === option.value ? "border-accent text-text" : "border-transparent text-muted hover:text-text",
          )}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

export function Dialog({ open, onOpenChange, title, description, children, width = 480 }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  children: ReactNode;
  width?: number;
}) {
  return (
    <RadixDialog.Root open={open} onOpenChange={onOpenChange}>
      <RadixDialog.Portal>
        <RadixDialog.Overlay className="fixed inset-0 z-40 bg-black/50 backdrop-blur-sm" />
        <RadixDialog.Content
          style={{ width: `min(${width}px, calc(100vw - 24px))` }}
          className="fixed top-1/2 left-1/2 z-50 max-h-[calc(100vh-24px)] -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-xl border border-line bg-surface p-6"
        >
          <RadixDialog.Title className="text-center text-lg">{title}</RadixDialog.Title>
          {description ? (
            <RadixDialog.Description className="mt-2 text-center text-text-2">{description}</RadixDialog.Description>
          ) : (
            <RadixDialog.Description className="sr-only">{title}</RadixDialog.Description>
          )}
          <RadixDialog.Close aria-label="Close" className="absolute top-4 right-4 flex size-6 items-center justify-center text-muted hover:text-text">
            ✕
          </RadixDialog.Close>
          <div className="mt-5">{children}</div>
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}

export function Row({ label, children, hint }: { label: ReactNode; children: ReactNode; hint?: string }) {
  return (
    <div className="flex items-center justify-between gap-2 py-1">
      <span className={cx("text-muted", hint && "dotted")} title={hint}>{label}</span>
      <span className="text-text">{children}</span>
    </div>
  );
}

export function Field({ label, suffix, ...props }: React.InputHTMLAttributes<HTMLInputElement> & { label: string; suffix?: ReactNode }) {
  return (
    <label className="flex h-10 items-center gap-2 rounded-lg border border-line bg-surface px-3 focus-within:border-line-strong">
      <span className="shrink-0 text-muted">{label}</span>
      <input {...props} className="min-w-0 flex-1 bg-transparent text-right text-text outline-none placeholder:text-faint" />
      {suffix}
    </label>
  );
}
