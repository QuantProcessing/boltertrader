import * as Popover from "@radix-ui/react-popover";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

export const PopoverRoot = Popover.Root;
export const PopoverTrigger = Popover.Trigger;
export const PopoverClose = Popover.Close;

export function PopoverContent({ className, sideOffset = 6, ...props }: ComponentProps<typeof Popover.Content>) {
  return (
    <Popover.Portal>
      <Popover.Content
        sideOffset={sideOffset}
        className={cn(
          "z-50 overflow-hidden rounded-modal border border-line-strong bg-overlay shadow-overlay outline-none",
          className,
        )}
        {...props}
      />
    </Popover.Portal>
  );
}
