import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

const button = cva(
  [
    "inline-flex shrink-0 items-center justify-center gap-1.5 rounded-control font-medium whitespace-nowrap",
    "transition-colors select-none",
    "disabled:pointer-events-none disabled:opacity-50",
    // WCAG 2.2 target size: compact on a mouse, thumb-sized on touch.
    "pointer-coarse:min-h-bar pointer-coarse:px-4",
  ],
  {
    variants: {
      variant: {
        // The one filled control. At most one per view — it marks the action
        // the operator came to perform, and loses that meaning if repeated.
        primary: "bg-accent text-accent-fg hover:bg-accent-hover",
        buy: "bg-buy text-buy-fg hover:brightness-105",
        sell: "bg-sell text-sell-fg hover:brightness-105",
        outline: "border border-line-strong text-text hover:bg-raised",
        ghost: "text-muted hover:bg-raised hover:text-text",
        // Destructive reads as an outline until hover, because a red block in
        // a dense table is a distraction the whole time it is not being used.
        danger:
          "border border-danger/50 text-danger hover:border-danger hover:bg-danger hover:text-bg",
      },
      size: {
        sm: "h-6 px-2 text-meta",
        md: "h-[30px] px-3 text-base",
        lg: "h-row px-4 text-base",
        icon: "size-[30px] p-0",
      },
    },
    defaultVariants: { variant: "outline", size: "md" },
  },
);

export interface ButtonProps
  extends ComponentProps<"button">,
    VariantProps<typeof button> {
  asChild?: boolean;
}

export function Button({ className, variant, size, asChild = false, ...props }: ButtonProps) {
  const Component = asChild ? Slot : "button";
  return (
    <Component
      className={cn(button({ variant, size }), className)}
      {...(asChild ? {} : { type: props.type ?? "button" })}
      {...props}
    />
  );
}

export { button as buttonVariants };
