import { useId } from "react";

import { cx } from "./primitives";

/**
 * Bolter mark: a rounded teal tile split by a bolt-shaped cut. The cut is a
 * mask so the mark reads on any background, down to a 16px favicon.
 * Keep in sync with public/favicon.svg.
 */
export function Mark({ size = 24, className }: { size?: number; className?: string }) {
  const id = "bolter-cut-" + useId().replace(/:/g, "");
  return (
    <svg viewBox="0 0 32 32" width={size} height={size} className={cx("shrink-0", className)} aria-hidden>
      <defs>
        <mask id={id}>
          <rect width="32" height="32" fill="#fff" />
          <path d="M19.5 1 11.5 16.8h8.6L12.5 31" fill="none" stroke="#000" strokeWidth="3.2" strokeMiterlimit="10" />
        </mask>
      </defs>
      <rect x="2" y="2" width="28" height="28" rx="8" fill="#50d2c1" mask={`url(#${id})`} />
    </svg>
  );
}

/** Mark + name, as used in the top bar and on the sign-in page. */
export function Wordmark({ size = 24, className }: { size?: number; className?: string }) {
  return (
    <span className={cx("flex items-center gap-2 text-text", className)}>
      <Mark size={size} />
      <span className="text-[17px] leading-none font-semibold tracking-[-0.01em]">Bolter</span>
    </span>
  );
}
