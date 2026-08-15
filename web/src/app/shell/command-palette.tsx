import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { useEffect } from "react";

import { sessionStore } from "@/app/session";
import { useTheme } from "@/app/theme";

/* ---------------------------------------------------------------------------
 * ⌘K.
 *
 * A keyboard route to everything the header can do, for an operator whose hands
 * are already on the keyboard placing orders. It is deliberately a shortcut to
 * existing commands and not a place where a command lives only here — anything
 * reachable solely by keystroke is unreachable for whoever does not know the
 * keystroke.
 *
 * The dialog ships focus trapping, escape-to-close and the `aria-modal`
 * semantics with it (cmdk builds on Radix's dialog); what is added here is the
 * global key binding and the command list.
 * ------------------------------------------------------------------------- */

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate();
  const { preference, setPreference } = useTheme();

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "k" || !(event.metaKey || event.ctrlKey)) return;
      // Claim the binding: ⌘K is Chrome's "search from address bar" and would
      // otherwise steal focus out of the app entirely.
      event.preventDefault();
      onOpenChange(!open);
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, onOpenChange]);

  const run = (action: () => void) => {
    onOpenChange(false);
    action();
  };

  return (
    <Command.Dialog
      open={open}
      onOpenChange={onOpenChange}
      label="Command palette"
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 pt-[12vh]"
    >
      <div className="w-full max-w-lg overflow-hidden rounded-lg border border-line-strong bg-panel shadow-[var(--bt-shadow)]">
        <Command.Input
          placeholder="Search commands…"
          className="h-bar w-full border-b border-line bg-transparent px-3.5 text-title text-text placeholder:text-dim focus:outline-none"
        />
        <Command.List className="max-h-80 overflow-y-auto p-1.5">
          <Command.Empty className="px-2.5 py-6 text-center text-base text-muted">No matching command.</Command.Empty>

          <Group heading="Go to">
            <Item onSelect={() => run(() => void navigate({ to: "/trade" }))}>Trade</Item>
            <Item onSelect={() => run(() => void navigate({ to: "/funding" }))}>Funding</Item>
            <Item onSelect={() => run(() => void navigate({ to: "/funding/arbitrage", search: {} }))}>Arbitrage</Item>
            <Item onSelect={() => run(() => void navigate({ to: "/portfolio" }))}>Portfolio</Item>
            <Item onSelect={() => run(() => void navigate({ to: "/activity" }))}>Activity</Item>
            <Item onSelect={() => run(() => void navigate({ to: "/settings" }))}>Settings</Item>
          </Group>

          <Group heading="Workspace">
            <Item onSelect={() => run(() => setPreference(preference === "dark" ? "light" : "dark"))}>
              Switch to {preference === "dark" ? "light" : "dark"} theme
            </Item>
            <Item onSelect={() => run(() => void sessionStore.logout())}>Sign out</Item>
          </Group>
        </Command.List>
      </div>
    </Command.Dialog>
  );
}

function Group({ heading, children }: { heading: string; children: React.ReactNode }) {
  return (
    <Command.Group
      heading={heading}
      className="[&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-micro [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:text-dim [&_[cmdk-group-heading]]:uppercase"
    >
      {children}
    </Command.Group>
  );
}

function Item({ children, onSelect }: { children: React.ReactNode; onSelect: () => void }) {
  return (
    <Command.Item
      onSelect={onSelect}
      className="flex h-row cursor-pointer items-center rounded-sm px-2.5 text-base text-muted data-[selected=true]:bg-raised data-[selected=true]:text-text"
    >
      {children}
    </Command.Item>
  );
}
