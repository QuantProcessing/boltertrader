import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { Venue } from "@/api/types";
import { CredentialFields, buildCredentialBundle, missingCredentialFields } from "./credential-form";

/* ---------------------------------------------------------------------------
 * The gateway answers 400 for a malformed credential bundle without saying
 * which key was wrong, so a mistake here surfaces as an unexplained refusal
 * during account creation — the one moment an operator has no way to debug.
 * The split between metadata and secret is the part that cannot be guessed
 * from a key name, so it is the part pinned hardest.
 * ------------------------------------------------------------------------- */

/** Hyperliquid's shape: two fields in one form that only `metadata` separates. */
const venue: Venue = {
  venue: "hyperliquid",
  environment: "live",
  nativeEnvironment: "live",
  ready: true,
  products: ["perp"],
  credentialSchema: {
    schemaVersion: 2,
    authKind: "eip712",
    fields: [
      { key: "accountAddress", label: "Account address", metadata: true, required: true, valueType: "string" },
      { key: "apiWalletPrivateKey", label: "API wallet private key", required: true, valueType: "string" },
      { key: "vaultAddress", label: "Vault address", metadata: true, required: false, valueType: "string" },
    ],
  },
  routeProfiles: [],
};

describe("buildCredentialBundle", () => {
  it("routes each value by the schema's metadata flag, not by its name", () => {
    const bundle = buildCredentialBundle(venue, {
      accountAddress: "0xabc",
      apiWalletPrivateKey: "0xsecret",
    });

    expect(bundle).toEqual({
      schemaVersion: 2,
      venue: "hyperliquid",
      authKind: "eip712",
      metadata: { accountAddress: "0xabc" },
      secrets: { apiWalletPrivateKey: "0xsecret" },
    });
  });

  it("trims values and drops the ones left blank", () => {
    const bundle = buildCredentialBundle(venue, {
      accountAddress: "  0xabc  ",
      apiWalletPrivateKey: "0xsecret",
      vaultAddress: "   ",
    });

    expect(bundle.metadata).toEqual({ accountAddress: "0xabc" });
  });

  it("keeps an empty metadata object for API-key venues", () => {
    const apiKeyVenue: Venue = {
      ...venue,
      venue: "binance",
      credentialSchema: {
        schemaVersion: 1,
        authKind: "api_key_secret",
        fields: [
          { key: "apiKey", label: "API key", required: true, valueType: "string" },
          { key: "apiSecret", label: "API secret", required: true, valueType: "string" },
        ],
      },
    };
    const bundle = buildCredentialBundle(apiKeyVenue, { apiKey: "key", apiSecret: "secret" });

    expect(bundle.metadata).toEqual({});
    expect(bundle.secrets).toEqual({ apiKey: "key", apiSecret: "secret" });
  });

  it("ignores keys the venue's schema does not declare", () => {
    // A stale value left in form state after switching venues must not travel:
    // the gateway validates against a fixed allow-list and refuses the whole
    // bundle for one unknown key.
    const bundle = buildCredentialBundle(venue, { apiKey: "from-binance", apiWalletPrivateKey: "0xsecret" });

    expect(bundle.secrets).toEqual({ apiWalletPrivateKey: "0xsecret" });
  });
});

describe("missingCredentialFields", () => {
  it("names the required fields still blank, and ignores the optional ones", () => {
    expect(missingCredentialFields(venue, { accountAddress: "0xabc" })).toEqual(["API wallet private key"]);
  });

  it("treats whitespace as unfilled, because the bundle would drop it anyway", () => {
    expect(missingCredentialFields(venue, { accountAddress: " ", apiWalletPrivateKey: "0xsecret" })).toEqual([
      "Account address",
    ]);
  });
});

describe("CredentialFields", () => {
  it("masks secret material and leaves metadata readable", () => {
    render(<CredentialFields venue={venue} values={{}} onChange={vi.fn()} />);

    expect(screen.getByLabelText(/Account address/)).toHaveAttribute("type", "text");
    expect(screen.getByLabelText(/API wallet private key/)).toHaveAttribute("type", "password");
  });

  it("keeps every field out of the password manager", () => {
    // These are venue API credentials. Offering to remember one would put it in
    // a store this app cannot clear, against PRODUCT.md's write-only rule.
    render(<CredentialFields venue={venue} values={{}} onChange={vi.fn()} />);

    for (const label of [/Account address/, /API wallet private key/, /Vault address/]) {
      expect(screen.getByLabelText(label)).toHaveAttribute("autocomplete", "off");
    }
  });

  it("renders only what it was handed, so a cleared form shows nothing", () => {
    const { rerender } = render(
      <CredentialFields venue={venue} values={{ apiWalletPrivateKey: "0xsecret" }} onChange={vi.fn()} />,
    );
    expect(screen.getByLabelText(/API wallet private key/)).toHaveValue("0xsecret");

    rerender(<CredentialFields venue={venue} values={{}} onChange={vi.fn()} />);

    expect(screen.getByLabelText(/API wallet private key/)).toHaveValue("");
  });
});
