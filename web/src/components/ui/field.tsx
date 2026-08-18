import { useId, type ComponentProps, type ReactNode } from "react";

import { cn } from "@/lib/utils";

export const inputClass = [
  "h-[30px] w-full rounded-control border border-line-strong bg-raised px-2 text-base text-text",
  "placeholder:text-dim",
  "disabled:cursor-not-allowed disabled:opacity-50",
  "aria-invalid:border-danger",
  "pointer-coarse:min-h-bar",
].join(" ");

export interface FieldProps {
  label: string;
  /** Static help. Always rendered, so revealing it cannot shift the layout. */
  hint?: ReactNode;
  /** Blocking problem with the current value. Announced when it appears. */
  error?: string | null;
  required?: boolean;
  className?: string;
  children: (props: {
    id: string;
    "aria-describedby": string | undefined;
    "aria-invalid": boolean | undefined;
    "aria-required": boolean | undefined;
  }) => ReactNode;
}

/**
 * Wires label, hint and error to the control by id so the accessible name and
 * description are correct without each call site remembering to do it.
 */
export function Field({ label, hint, error, required, className, children }: FieldProps) {
  const id = useId();
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const describedBy = [hint ? hintId : null, error ? errorId : null].filter(Boolean).join(" ");

  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <label htmlFor={id} className="th">
        {label}
        {required ? (
          <span className="text-danger" aria-hidden="true">
            {" "}
            *
          </span>
        ) : null}
      </label>

      {children({
        id,
        "aria-describedby": describedBy === "" ? undefined : describedBy,
        "aria-invalid": error ? true : undefined,
        "aria-required": required ? true : undefined,
      })}

      {hint ? (
        <p id={hintId} className="text-meta text-dim">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={errorId} className="text-meta text-danger">
          {error}
        </p>
      ) : null}
    </div>
  );
}

export function Input({ className, ...props }: ComponentProps<"input">) {
  return <input className={cn(inputClass, className)} {...props} />;
}
