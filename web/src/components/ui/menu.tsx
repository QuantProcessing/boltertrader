import * as Menu from "@radix-ui/react-dropdown-menu";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * A menu hung off a control.
 *
 * Radix again for the parts that are wrong to hand-write — typeahead, roving
 * focus, the escape and outside-click bindings, collision-aware placement — and
 * this file for the surface, which is `--shadow-overlay` rather than the modal
 * shadow: a menu sits *on* the page and a dialog sits *over* it, and design.md
 * §6 gives the two different depths for exactly that reason.
 *
 * Items are `h-row` so a menu next to a table reads at the same rhythm as the
 * table, and `text-base` because a menu is read, not scanned.
 * ------------------------------------------------------------------------- */

export const MenuRoot = Menu.Root;
export const MenuTrigger = Menu.Trigger;
export const MenuRadioGroup = Menu.RadioGroup;

export function MenuContent({ className, sideOffset = 6, ...props }: ComponentProps<typeof Menu.Content>) {
  return (
    <Menu.Portal>
      <Menu.Content
        sideOffset={sideOffset}
        className={cn(
          "z-50 min-w-44 overflow-hidden rounded-modal border border-line-strong bg-overlay p-1 shadow-overlay",
          className,
        )}
        {...props}
      />
    </Menu.Portal>
  );
}

const ITEM = [
  "flex h-row cursor-pointer items-center gap-2 rounded-control px-2 text-base text-text",
  "outline-none select-none",
  // `data-highlighted` covers both hover and keyboard focus, which is the whole
  // reason to use it rather than `hover:` — a menu driven by arrow keys with no
  // visible selection is a menu being navigated blind.
  "data-highlighted:bg-raised",
  "data-disabled:pointer-events-none data-disabled:opacity-50",
];

export function MenuItem({ className, ...props }: ComponentProps<typeof Menu.Item>) {
  return <Menu.Item className={cn(ITEM, className)} {...props} />;
}

/**
 * A choice among alternatives, marked with a check.
 *
 * The indicator occupies its slot whether or not it is filled, so the labels
 * stay on one left edge instead of shifting by the width of a tick when the
 * selection moves.
 */
export function MenuRadioItem({ className, children, ...props }: ComponentProps<typeof Menu.RadioItem>) {
  return (
    <Menu.RadioItem className={cn(ITEM, className)} {...props}>
      <span className="flex w-3 shrink-0 justify-center text-accent">
        <Menu.ItemIndicator>✓</Menu.ItemIndicator>
      </span>
      {children}
    </Menu.RadioItem>
  );
}

export function MenuLabel({ className, ...props }: ComponentProps<typeof Menu.Label>) {
  return <Menu.Label className={cn("px-2 py-1 text-meta text-dim", className)} {...props} />;
}

export function MenuSeparator({ className, ...props }: ComponentProps<typeof Menu.Separator>) {
  return <Menu.Separator className={cn("my-1 h-px bg-line", className)} {...props} />;
}
