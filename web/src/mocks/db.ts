import type {
  Account,
  AccountRoute,
  ActivityEvent,
  ArbitrageExecution,
  ArbitragePreview,
  Command,
  CommandProjection,
  ManualIntent,
  ManualPreviewRequest,
} from "@/api/types";

import { ACCOUNT, FILLS, HEDGE_ACCOUNT, OBSERVED_AT, instrumentOf } from "./fixtures";

/* ---------------------------------------------------------------------------
 * The mock's memory.
 *
 * Handlers are stateless functions over this object, which is what makes the
 * sequence assertable: an order that was placed has to still be there when the
 * blotter polls two seconds later, and cancelling it has to change that same
 * record rather than append a second one.
 *
 * The state machine is the one the trading service actually runs — a placed
 * order rests in `submitted`, a cancel moves it to `canceled`, spending a
 * preview twice replays the first command instead of placing a second order,
 * and every transition leaves an activity event behind. Those are the parts a
 * frontend can get wrong, so they are the parts that are modelled. Everything
 * else (matching, funding accrual, the venue) is out of scope: nothing in the
 * browser can tell the difference.
 *
 * Identifiers are counters rather than UUIDs so two runs produce the same
 * transcript. A mock that renames its own records every run cannot be asserted
 * against.
 * ------------------------------------------------------------------------- */

/**
 * The session lives in a cookie because that is where the real one lives.
 *
 * The consequence is the one that matters: a reload keeps the operator signed
 * in and re-mints the CSRF token through `/me`, exactly as the deployed edge
 * does. Holding it in a module variable instead would sign the operator out on
 * every hot reload and hide that path completely.
 */
const SESSION_COOKIE = "bolter_mock_session";

let memorySession = false;

function readSession(): boolean {
  if (typeof document === "undefined") return memorySession;
  return document.cookie.split("; ").some((entry) => entry.startsWith(`${SESSION_COOKIE}=`));
}

function writeSession(open: boolean): void {
  memorySession = open;
  if (typeof document === "undefined") return;
  document.cookie = open ? `${SESSION_COOKIE}=1; path=/; SameSite=Lax` : `${SESSION_COOKIE}=; path=/; max-age=0`;
}

interface State {
  accounts: Account[];
  /** Sealed manual previews, by id, until they are spent. */
  previews: Map<string, ManualPreviewRequest>;
  arbitragePreviews: Map<string, ArbitragePreview>;
  /** What each spent preview produced, so a replay answers with it again. */
  spent: Map<string, Command>;
  orders: Command[];
  hedges: ArbitrageExecution[];
  activity: ActivityEvent[];
  counters: Record<string, number>;
}

function empty(): State {
  return {
    // Cloned, so a mutation in one test cannot leak into the fixture module and
    // from there into the next one.
    accounts: [structuredClone(ACCOUNT), structuredClone(HEDGE_ACCOUNT)],
    previews: new Map(),
    arbitragePreviews: new Map(),
    spent: new Map(),
    orders: [],
    hedges: [],
    activity: seedActivity(),
    counters: {},
  };
}

/**
 * The one event that was already there.
 *
 * Positions, ledger, pnl and fills all describe the same seeded lineage, and an
 * activity feed that started empty would be the only screen claiming that
 * lineage never happened. `fill.recorded` is the kind the trading service emits
 * for it — see `publishFillActivity` — so the feed's filter has something real
 * to match on before the operator has done anything.
 */
function seedActivity(): ActivityEvent[] {
  return FILLS.map((fill, index) => ({
    eventId: `evt-seed-${index + 1}`,
    sequence: index + 1,
    kind: "fill.recorded",
    productId: fill.productId,
    accountId: fill.accountId,
    resourceId: fill.fillId,
    payload: { lineageId: fill.lineageId, commandId: fill.commandId, quantity: fill.quantity },
    observedAt: fill.occurredAt,
  }));
}

let state = empty();

/** Drops every trade, account edit and event. The session is left alone. */
export function reset(): void {
  state = empty();
}

function nextId(prefix: string): string {
  const next = (state.counters[prefix] ?? 0) + 1;
  state.counters[prefix] = next;
  return `${prefix}-${next}`;
}

export const session = {
  get open(): boolean {
    return readSession();
  },
  signIn(): void {
    writeSession(true);
  },
  signOut(): void {
    writeSession(false);
  },
};

/* --- Accounts ------------------------------------------------------------- */

export const accounts = {
  list: (): Account[] => state.accounts,

  find: (accountId: string): Account | undefined =>
    state.accounts.find((account) => account.accountId === accountId),

  /**
   * Creating an account from what the form actually sends.
   *
   * `CreateAccountRequest` carries no environment: the gateway derives it from
   * the route profiles, and a client that sent one could disagree with the
   * routes it sent alongside. That derivation is reproduced here rather than
   * defaulted, because the settings screen's whole claim is that the routes
   * decide.
   *
   * The credential bundle is read and discarded. Nothing about it is stored and
   * nothing about it is echoed — `Account` has no field for a secret, which is
   * what makes the write-only rule a property of the API rather than a habit.
   */
  create(request: { venue: string; displayName: string; productPolicy: string[]; routes: AccountRoute[] }): Account {
    const account: Account = {
      accountId: nextId(`acct-${request.venue}`),
      venue: request.venue,
      nativeEnvironment: "live",
      displayName: request.displayName,
      productPolicy: request.productPolicy,
      routes: request.routes.map((route) => ({ ...route, requestedState: route.requestedState ?? "active" })),
      status: "active",
      lastValidatedAt: OBSERVED_AT,
      updatedAt: OBSERVED_AT,
    };
    state.accounts.push(account);
    return account;
  },

  /**
   * Credentials and runtime fences remain server-owned; browser mutations only
   * update the public lifecycle projection.
   */
  mutate(accountId: string, change: Partial<Account>): Account | undefined {
    const account = accounts.find(accountId);
    if (!account) return undefined;
    Object.assign(account, change, { updatedAt: OBSERVED_AT });
    return account;
  },
};

/* --- Manual orders -------------------------------------------------------- */

export const previews = {
  seal(request: ManualPreviewRequest): string {
    const previewId = nextId("prev");
    state.previews.set(previewId, request);
    return previewId;
  },

  /** The sealed request, or undefined if it was never issued. */
  get: (previewId: string): ManualPreviewRequest | undefined => state.previews.get(previewId),

  sealArbitrage(preview: ArbitragePreview): void {
    state.arbitragePreviews.set(preview.previewId, preview);
  },

  getArbitrage: (previewId: string): ArbitragePreview | undefined => state.arbitragePreviews.get(previewId),

  nextArbitrageId: (): string => nextId("aprev"),
};

export const orders = {
  list: (): Command[] => state.orders,

  find: (commandId: string): Command | undefined =>
    state.orders.find((order) => order.commandId === commandId),

  /** The trimmed view `GET /api/v1/commands` serves: no payload, no evidence. */
  projections(): CommandProjection[] {
    return state.orders.map(({ payload: _payload, executionEvidence: _evidence, ...rest }) => rest);
  },

  /**
   * Spend a sealed preview.
   *
   * `replay` distinguishes the two outcomes because the caller has to answer
   * 200 for one and 202 for the other: a retried submission must not place a
   * second order, and the client reads the returned command's state either way.
   */
  place(request: { previewId: string; lineageId: string; idempotencyKey: string }): {
    command: Command;
    replay: boolean;
  } | null {
    const existing = state.spent.get(request.previewId);
    if (existing) return { command: existing, replay: true };

    const intent = state.previews.get(request.previewId);
    if (!intent) return null;

    const account = accounts.find(intent.accountId);
    const command: Command = {
      commandId: nextId("cmd"),
      accountId: intent.accountId,
      lineageId: request.lineageId,
      productId: "manual",
      reservationId: null,
      idempotencyKey: request.idempotencyKey,
      commandKind: "place",
      // Submitted rather than filled: the order is at the venue and resting,
      // which is the state a blotter has something to do with.
      state: "submitted",
      payload: {
        instrumentId: intent.instrumentId,
        side: intent.side,
        quantity: intent.quantity,
        orderType: intent.orderType,
        limitPrice: intent.limitPrice,
        timeInForce: intent.timeInForce,
        reduceOnly: intent.reduceOnly,
        leverage: intent.leverage,
        routeId: routeIdFor(account, intent.instrumentId),
        runtimeId: "runtime-mock-v1",
      } satisfies ManualIntent,
      blockers: [],
      createdAt: OBSERVED_AT,
      updatedAt: OBSERVED_AT,
      executionEvidence: {
        gatewayOrderId: `gw-${state.orders.length + 1}`,
        state: "submitted",
        resourceVersion: 1,
        orderEvents: [{ eventId: nextId("oe"), state: "submitted", resourceVersion: 1, occurredAt: OBSERVED_AT }],
      },
    };

    // The seal is consumed whether or not anything else happens next, which is
    // the property that makes a double-submit safe.
    state.previews.delete(request.previewId);
    state.spent.set(request.previewId, command);
    state.orders.unshift(command);
    record("command.submitted", command);
    return { command, replay: false };
  },

  /** Cancels a resting order. Returns null when there is no such order to cancel. */
  cancel(orderId: string): Command | null {
    const order = orders.find(orderId);
    if (!order) return null;
    order.state = "canceled";
    order.commandKind = "cancel";
    order.updatedAt = OBSERVED_AT;
    record("command.cancel_accepted", order);
    return order;
  },
};

/** Which of the account's routes can carry this instrument's product. */
function routeIdFor(account: Account | undefined, instrumentId: string): string {
  const instrument = instrumentOf(instrumentId);
  const route = (account?.routes ?? []).find(
    (candidate) =>
      candidate.product === instrument?.product &&
      (instrument.settlement ?? "") === (candidate.settlementAsset ?? ""),
  );
  return route?.routeId ?? "";
}

/* --- Activity ------------------------------------------------------------- */

/**
 * The audit trail, newest first.
 *
 * Kinds are the server's own: `publishCommandActivity` names an event
 * `"command." + command.state`, which is why a cancel appears as
 * `command.cancel_accepted` rather than as anything with "order" in it. The
 * activity screen filters on these strings, so inventing friendlier ones here
 * would make the filter work against a vocabulary the server never emits.
 */
export function record(kind: string, command: Command): ActivityEvent {
  const sequence = state.activity.length + 1;
  const event: ActivityEvent = {
    eventId: nextId("evt"),
    sequence,
    kind,
    productId: command.productId,
    accountId: command.accountId,
    resourceId: command.commandId,
    payload: { state: command.state, lineageId: command.lineageId },
    observedAt: OBSERVED_AT,
  };
  state.activity.unshift(event);
  return event;
}

export const activity = {
  list: (): ActivityEvent[] => state.activity,
};

/* --- Arbitrage pairs ------------------------------------------------------ */

export const hedges = {
  list: (): ArbitrageExecution[] => state.hedges,

  /** Records an opened or closed pair, newest first. */
  push(execution: ArbitrageExecution): void {
    const existing = state.hedges.findIndex((pair) => pair.lineageId === execution.lineageId);
    // Closing a pair edits the pair that was opened rather than appending a
    // second row: the positions screen keys on the lineage, and two rows under
    // one lineage is a state a real pair can never be in.
    if (existing >= 0) state.hedges.splice(existing, 1);
    state.hedges.unshift(execution);
  },

  nextLegId: (): string => nextId("cmd"),
};
