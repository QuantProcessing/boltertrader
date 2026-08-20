import { useLocation, useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { modeStore, useMode, type Mode } from "@/app/mode";
import { useSession } from "@/app/session";
import { Button } from "@/components/ui/button";
import { DialogContent, DialogRoot } from "@/components/ui/dialog";
import { Segmented } from "@/components/ui/segmented";
import { LOCALES, localeStore, useLocale, useT, type Locale } from "@/lib/i18n";

/* ---------------------------------------------------------------------------
 * The gate a visitor passes on the way in.
 *
 * It exists because the product used to open on a login form, which is a
 * strange thing to show someone who has not been told what they are logging
 * into. What they get instead is the disclosure and a choice: read the market,
 * or sign in and trade it.
 *
 * Deliberately not a landing page. PRODUCT.md rules out hero typography and
 * feature-card grids inside an authenticated product, and this is a door, not a
 * pitch — `text-title` at the largest, no illustration, no marketing.
 *
 * It is not dismissible: the two buttons are the two ways out, and both are a
 * decision. The buttons choose whether to continue anonymously or go to login;
 * authentication, not this dialog, determines whether the product is writable.
 * ------------------------------------------------------------------------- */

/** The four clauses, in the order they are read. Text lives in the dictionary. */
const RISKS = [
  "entry.risk.live",
  "entry.risk.loss",
  "entry.risk.venue",
  "entry.risk.responsibility",
] as const;

export function EntryDialog() {
  const { status } = useSession();
  const mode = useMode();
  const locale = useLocale();
  const t = useT();
  const navigate = useNavigate();
  const location = useLocation();
  const [acknowledged, setAcknowledged] = useState(false);

  const firstEntry = status === "anonymous" && mode === "undecided";

  useEffect(() => {
    if (firstEntry) setAcknowledged(false);
  }, [firstEntry]);

  if (!firstEntry) return null;

  const choose = (next: Exclude<Mode, "undecided">) => {
    modeStore.choose(next);
    if (next === "readWrite") {
      void navigate({ to: "/login", search: { redirect: location.href } });
    }
  };

  return (
    <DialogRoot open>
      <DialogContent
        title={t("entry.title")}
        description={t("entry.subtitle")}
        // Radix closes on both by default. A disclosure that can be dismissed
        // without reading it is not one.
        onEscapeKeyDown={(event) => {
          event.preventDefault();
        }}
        onPointerDownOutside={(event) => {
          event.preventDefault();
        }}
        onInteractOutside={(event) => {
          event.preventDefault();
        }}
      >
        <div className="flex min-h-0 flex-col gap-4 overflow-y-auto px-4 pt-3 pb-4">
          <Segmented<Locale>
            name="entry-locale"
            label={t("utility.language")}
            value={locale}
            options={LOCALES.map((option) => ({ value: option.value, label: option.short }))}
            onChange={(next) => localeStore.set(next)}
            className="self-start"
          />

          {/* `list-none` with an em-dash marker rather than bullets: §11 keeps
              decoration out of dense surfaces, and these are clauses to read
              rather than items to count. */}
          <ul className="flex list-none flex-col gap-2 text-base text-muted">
            {RISKS.map((key) => (
              <li key={key} className="flex gap-2">
                <span aria-hidden="true" className="text-dim">
                  —
                </span>
                <span>{t(key)}</span>
              </li>
            ))}
          </ul>

          <label className="flex cursor-pointer items-start gap-2 text-base text-text">
            <input
              type="checkbox"
              checked={acknowledged}
              onChange={(event) => setAcknowledged(event.target.checked)}
              // `mt-0.5` rather than centring the row: the label wraps at narrow
              // widths and a vertically-centred box drifts away from its first
              // line when it does.
              className="mt-0.5 size-3.5 shrink-0 accent-[var(--bt-accent)]"
            />
            {t("entry.acknowledge")}
          </label>

          {/* Read-only first, and it is the outline button. The filled one is
              reserved for the action the operator came to perform (§button), and
              here the safe path is the one most visitors want — but the one that
              spends money should not be the one the eye lands on by default. */}
          <div className="flex flex-col gap-2">
            <ModeChoice
              label={t("entry.readOnly")}
              detail={t("entry.readOnly.detail")}
              disabled={!acknowledged}
              onClick={() => choose("readOnly")}
            />
            <ModeChoice
              variant="danger"
              label={t("entry.readWrite")}
              detail={t("entry.readWrite.detail")}
              disabled={!acknowledged}
              onClick={() => choose("readWrite")}
            />
          </div>

          <p className="text-meta text-dim">{t("entry.footnote")}</p>
        </div>
      </DialogContent>
    </DialogRoot>
  );
}

/**
 * One of the two ways out: a label and the consequence of choosing it.
 *
 * The consequence sits inside the button rather than beside it because it is
 * part of what is being agreed to, and a caption a click away from the control
 * it describes is a caption nobody reads.
 */
function ModeChoice({
  label,
  detail,
  variant,
  disabled,
  onClick,
}: {
  label: string;
  detail: string;
  variant?: "danger";
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <Button
      variant={variant}
      size="lg"
      disabled={disabled}
      onClick={onClick}
      className="h-auto flex-col items-start gap-0.5 px-3 py-2 text-left whitespace-normal"
    >
      <span className="font-medium">{label}</span>
      {/* `text-current` with opacity rather than `text-muted`: the danger
          variant inverts to `text-bg` on hover, and a fixed grey caption would
          vanish into the fill at exactly the moment it is being read. */}
      <span className="text-meta font-normal opacity-70">{detail}</span>
    </Button>
  );
}
