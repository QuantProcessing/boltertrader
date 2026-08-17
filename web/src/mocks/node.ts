import { setupServer } from "msw/node";

import { handlers } from "./handlers";

/**
 * The same handlers, for jsdom.
 *
 * Component tests that mount a screen and let its queries run get the real
 * envelopes instead of a hand-stubbed `useQuery` return, which is the only way
 * a test can catch an unwrap that reads the wrong shape. Start it from a setup
 * file, not from individual tests: a server started per test file leaks
 * listeners.
 */
export const server = setupServer(...handlers);
