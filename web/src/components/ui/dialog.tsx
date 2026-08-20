import * as Dialog from "@radix-ui/react-dialog";
import type { ComponentProps, ReactNode } from "react";

import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * A modal dialog.
 *
 * Radix supplies the parts that are wrong to hand-write — the focus trap, the
 * escape binding, the `aria-modal` wiring, the inert background — and this file
 * supplies the surface. Every modal shares this overlay and panel so dialogs
 * do not drift into separate visual languages.
 *
 * `Title` is not optional in the Radix sense but it is easy to omit by
 * accident, and a dialog with no accessible name is announced as nothing at
 * all. Every dialog built on this passes one.
 * ------------------------------------------------------------------------- */

export const DialogRoot = Dialog.Root;
export const DialogTrigger = Dialog.Trigger;
export const DialogClose = Dialog.Close;

/**
 * `title` shadows the native `title` attribute, which `Dialog.Content` inherits
 * from `div` and which is why the `Omit` is needed at all. Losing it costs
 * nothing: a browser tooltip on a modal panel has no reader, and the dialog's
 * accessible name is exactly what the attribute would have been misused for.
 */
export interface DialogContentProps
  extends Omit<ComponentProps<typeof Dialog.Content>, "title"> {
  /** The accessible name. Rendered unless `titleHidden`. */
  title: ReactNode;
  /** Announced with the title. Rendered beneath it unless `descriptionHidden`. */
  description?: ReactNode;
  titleHidden?: boolean;
  descriptionHidden?: boolean;
}

export function DialogContent({
  title,
  description,
  titleHidden = false,
  descriptionHidden = false,
  className,
  children,
  ...props
}: DialogContentProps) {
  return (
    <Dialog.Portal>
      <Dialog.Overlay className="fixed inset-0 z-50 bg-black/50" />
      <Dialog.Content
        className={cn(
          "fixed top-1/2 left-1/2 z-50 w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2",
          // `max-h` with an internal scroll rather than a panel that grows past
          // the viewport: a risk disclosure whose buttons are below the fold is
          // a disclosure nobody reads.
          "flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden",
          "rounded-modal border border-line-strong bg-overlay shadow-modal",
          className,
        )}
        {...props}
      >
        <div className="flex flex-col gap-1 px-4 pt-4">
          <Dialog.Title className={cn("text-title font-semibold text-text", titleHidden && "sr-only")}>
            {title}
          </Dialog.Title>
          {description === undefined ? null : (
            <Dialog.Description className={cn("text-base text-muted", descriptionHidden && "sr-only")}>
              {description}
            </Dialog.Description>
          )}
        </div>
        {children}
      </Dialog.Content>
    </Dialog.Portal>
  );
}
