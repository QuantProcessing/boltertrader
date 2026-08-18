import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { useEffect } from "react";

import { modeStore, useReadOnly } from "@/app/mode";
import { sessionStore } from "@/app/session";
import { useT } from "@/lib/i18n";

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
  const readOnly = useReadOnly();
  const t = useT();

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
      label={t("palette.label")}
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 pt-[12vh]"
    >
      <div className="w-full max-w-lg overflow-hidden rounded-modal border border-line-strong bg-overlay shadow-modal">
        <Command.Input
          placeholder={t("palette.search")}
          className="h-bar w-full border-b border-line bg-transparent px-3.5 text-title text-text placeholder:text-dim focus:outline-none"
        />
        <Command.List className="max-h-80 overflow-y-auto p-1.5">
          <Command.Empty className="px-2.5 py-6 text-center text-base text-muted">{t("palette.empty")}</Command.Empty>

          {/* Every section stays listed for a read-only visitor, private ones
              included. Selecting one lands on the login form, which is a
              better answer to "where is Portfolio" than the command not being
              there at all. */}
          <Group heading={t("palette.goTo")}>
            <Item onSelect={() => run(() => void navigate({ to: "/trade" }))}>{t("nav.trade")}</Item>
            <Item onSelect={() => run(() => void navigate({ to: "/funding" }))}>{t("nav.funding")}</Item>
            <Item onSelect={() => run(() => void navigate({ to: "/funding/arbitrage", search: {} }))}>
              {t("nav.arbitrage")}
            </Item>
            <Item onSelect={() => run(() => void navigate({ to: "/portfolio" }))}>{t("nav.portfolio")}</Item>
            <Item onSelect={() => run(() => void navigate({ to: "/activity" }))}>{t("nav.activity")}</Item>
            <Item onSelect={() => run(() => void navigate({ to: "/settings" }))}>{t("nav.settings")}</Item>
          </Group>

          <Group heading={t("palette.workspace")}>
            {readOnly ? (
              <Item onSelect={() => run(() => void navigate({ to: "/login", search: { redirect: "/trade" } }))}>
                {t("utility.signIn")}
              </Item>
            ) : (
              // Resets the mode with the session, for the same reason the
              // header's sign-out does: a `readWrite` mode outliving the
              // session it was granted under only shows up as refused writes.
              <Item
                onSelect={() =>
                  run(() => {
                    void sessionStore.logout().then(() => modeStore.reset());
                  })
                }
              >
                {t("utility.signOut")}
              </Item>
            )}
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
      className="[&_[cmdk-group-heading]]:th [&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:py-1.5"
    >
      {children}
    </Command.Group>
  );
}

function Item({ children, onSelect }: { children: React.ReactNode; onSelect: () => void }) {
  return (
    <Command.Item
      onSelect={onSelect}
      className="flex h-row cursor-pointer items-center rounded-control px-2.5 text-base text-muted data-[selected=true]:bg-raised data-[selected=true]:text-text"
    >
      {children}
    </Command.Item>
  );
}
