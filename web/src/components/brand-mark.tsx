import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

/**
 * A bolt inside a hexagonal fastener: execution and mechanical certainty in
 * one mark. The cut-out is part of the path, so the logo stays transparent and
 * works on every terminal surface without a background-colour patch.
 */
export function BrandMark({ className, ...props }: ComponentProps<"svg">) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
      className={cn("shrink-0", className)}
      {...props}
    >
      <path
        fill="currentColor"
        fillRule="evenodd"
        d="M12 1.6 21.1 6.8v10.4L12 22.4l-9.1-5.2V6.8L12 1.6Zm1.7 3.9-6.4 7.6h4.2l-1.2 5.5 6.5-8.1h-4.2l1.1-5Z"
        clipRule="evenodd"
      />
    </svg>
  );
}
