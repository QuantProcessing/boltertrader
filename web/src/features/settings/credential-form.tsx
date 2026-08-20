import { Field, Input } from "@/components/ui/field";
import { useT } from "@/lib/i18n";
import type { CredentialBundle, Venue } from "@/api/types";

/* ---------------------------------------------------------------------------
 * The credential form, built from the venue rather than from a venue list.
 *
 * `catalog.CredentialSchema` says which fields this venue needs, whether each
 * one is metadata or secret, and which are required. The gateway validates the
 * bundle against a fixed key allow-list and answers 400 without saying which
 * key was wrong, so guessing field names here would produce an error message
 * nobody can act on. Driving the form from the schema means a venue added to
 * the registry gets a correct form with no change to this file.
 *
 * Every value lives in component state for exactly as long as the form is open
 * and is cleared the moment it is submitted. Nothing here is persisted, put in
 * query state, or read back: PRODUCT.md's write-only rule, and the gateway's
 * `PublicAccount` has no field that could return one anyway.
 * ------------------------------------------------------------------------- */

export type CredentialValues = Record<string, string>;

export function CredentialFields({
  venue,
  values,
  onChange,
  disabled = false,
}: {
  venue: Venue;
  values: CredentialValues;
  onChange: (key: string, value: string) => void;
  disabled?: boolean;
}) {
  const t = useT();

  return (
    <div className="flex flex-col gap-3">
      {venue.credentialSchema.fields.map((field) => (
        <Field
          key={field.key}
          // The venue's own name for the field, printed as it arrived.
          label={field.label}
          required={field.required}
          hint={field.metadata ? t("settings.credential.metadata") : t("settings.credential.secret")}
        >
          {(props) => (
            <Input
              {...props}
              // Secrets are masked and kept out of the password manager: this
              // is an API key for a venue, not a login the browser should offer
              // to remember and refill somewhere else.
              type={field.metadata ? "text" : "password"}
              autoComplete="off"
              spellCheck={false}
              disabled={disabled}
              value={values[field.key] ?? ""}
              onChange={(event) => onChange(field.key, event.target.value)}
            />
          )}
        </Field>
      ))}
    </div>
  );
}

/** Required fields the operator has not filled in, by label. */
export function missingCredentialFields(venue: Venue, values: CredentialValues): string[] {
  return venue.credentialSchema.fields
    .filter((field) => field.required && (values[field.key] ?? "").trim() === "")
    .map((field) => field.label);
}

/**
 * Splits the typed values into the two buckets the gateway's decoder expects.
 * `field.metadata` is the only thing that decides which side a key lands on.
 */
export function buildCredentialBundle(venue: Venue, values: CredentialValues): CredentialBundle {
  const metadata: CredentialValues = {};
  const secrets: CredentialValues = {};
  for (const field of venue.credentialSchema.fields) {
    const value = (values[field.key] ?? "").trim();
    if (value === "") continue;
    if (field.metadata) metadata[field.key] = value;
    else secrets[field.key] = value;
  }
  return {
    schemaVersion: venue.credentialSchema.schemaVersion,
    venue: venue.venue,
    authKind: venue.credentialSchema.authKind,
    // credential.BundleV1 requires both nested objects. In particular,
    // API-key venues have no metadata fields but the Go decoder still needs
    // the wire value to be an object, so it must be `{}` rather than absent.
    metadata,
    secrets,
  };
}
