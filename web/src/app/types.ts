export type Mode = "test" | "live";
export type NativeEnvironment = "demo" | "testnet" | "live";
export type ProductPolicy = "funding" | "manual";

export type AccountState = "creating" | "active" | "rotating" | "draining" | "drained" | "disabled" | "removing" | "removed";

export interface AccountRoute {
  routeId: string;
  product: "spot" | "perp";
  settlementAsset?: string;
  routeProfileId: string;
  requestedState: "active" | "disabled";
}

export interface Account {
  accountId: string;
  venue: string;
  mode: Mode;
  nativeEnvironment: NativeEnvironment;
  displayName: string;
  productPolicy: ProductPolicy[];
  status: AccountState;
  desiredStateRevision: number;
  runtimeGeneration: number;
  credentialVersion: number;
  routes: AccountRoute[];
  lastValidatedAt?: string | null;
  updatedAt?: string;
}

export interface CredentialFieldDescriptor {
  key: string;
  label: string;
  metadata?: boolean;
  required?: boolean;
  valueType?: "string" | "integer";
}

export interface CredentialSchema {
  schemaVersion: number;
  authKind: string;
  fields: CredentialFieldDescriptor[];
}

export interface RouteProfile extends Omit<AccountRoute, "requestedState"> {
  label: string;
}

export interface Venue {
  venue: string;
  environment: NativeEnvironment;
  nativeEnvironment: NativeEnvironment;
  ready: boolean;
  products: ProductPolicy[];
  reason?: string;
  credentialSchema: CredentialSchema;
  routeProfiles: RouteProfile[];
}

export interface Readiness {
  ready: boolean;
  components?: Record<string, { ready?: boolean; reason?: string; state?: string }>;
}

export interface ProjectionReadiness {
  ready: boolean;
  reason?: string;
}

export interface RuntimeCapability {
  rowId: string;
  venue: string;
  product: string;
  settlement?: string;
  writeReady: boolean;
  operations: Record<string, boolean>;
}

export interface Capability {
  accountId: string;
  rowId: string;
  venue: string;
  product: string;
  settlement?: string;
  writeReady: boolean;
  operation: string;
  supported: boolean;
}

export interface Instrument {
  instrumentId: string;
  venue: string;
  environment?: string;
  product?: string;
  symbol: string;
  baseAsset?: string;
  quoteAsset?: string;
  ready: boolean;
}

export interface FundingOpportunity {
  opportunityId: string;
  canonicalMarketId: string;
  longAccountId: string;
  shortAccountId: string;
  longInstrumentId: string;
  shortInstrumentId: string;
  longVenue: string;
  shortVenue: string;
  longFundingRate: string;
  shortFundingRate: string;
  reservationRequirements: FundingReservationScopeRequirement[];
}

export interface NettingScope {
  accountId: string;
  venue: string;
  instrumentId: string;
  positionMode: string | null;
  settlementScope: string | null;
  venueEnvironment: string;
}

export interface FundingReservationScopeRequirement {
  accountId: string;
  productId: "funding";
  reservationKind: "open" | "close";
  nettingScope: NettingScope;
  mode: Mode;
}

export interface FundingReservationRequirement extends FundingReservationScopeRequirement {
  quantity: string;
  notional: string;
  currency: string;
}

export interface Preview {
  previewId: string;
  canSubmit?: boolean;
  blockers: string[];
  warnings: string[];
  expiresAt?: string;
  plan?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface FundingPreview extends Preview {
  opportunityId: string;
  canSubmit: boolean;
  mode: Mode;
  catalogRevision?: string;
  factsRevision?: string;
  reservationRequirements: FundingReservationRequirement[];
}

export interface Command {
  commandId?: string;
  workItemId?: string;
  lineageId?: string;
  productId?: string;
  accountId?: string;
  commandKind?: string;
  status?: string;
  state?: string;
  createdAt?: string;
  updatedAt?: string;
  executionEvidence?: Record<string, unknown>;
}

export interface Position {
  lineageId: string;
  productId: string;
  mode?: Mode;
  accountId: string;
  instrumentId: string;
  settlementCurrency?: string;
  quantity: string;
  averageEntryPrice?: string;
  markPrice?: string;
  realizedPnl?: string;
  unrealizedPnl?: string;
  updatedAt?: string;
}

export interface Fill {
  fillId: string;
  commandId?: string;
  productId?: string;
  accountId: string;
  instrumentId: string;
  side: string;
  quantity: string;
  price: string;
  fee?: string;
  feeCurrency?: string;
  occurredAt: string;
}

export interface LedgerEntry {
  entryId: string;
  lineageId: string;
  productId: string;
  accountId: string;
  instrumentId: string;
  kind: string;
  amount: string;
  currency: string;
  occurredAt: string;
}

export interface ActivityEvent {
  eventId: string;
  sequence: number;
  cursor: string;
  kind: string;
  productId: string | null;
  accountId: string | null;
  resourceId: string | null;
  payload: Record<string, unknown>;
  observedAt: string;
}

export interface AuditRecord {
  auditId: string;
  action: string;
  resourceType: string;
  resourceId: string;
  outcome: string;
  requestId: string;
  occurredAt: string;
}

export interface RuntimeView {
  accountId: string;
  active?: RuntimeSnapshotView;
  pending?: PendingSnapshotView;
  retiring: RuntimeSnapshotView[];
  desiredStateRevision: number;
  runtimeGeneration: number;
  credentialVersion: number;
  runtimeVersion: number;
  readiness: ProjectionReadiness;
  writeReadiness: ProjectionReadiness;
  lastAppliedDesiredStateRevision: number;
  lastAppliedRuntimeGeneration: number;
  blockedReason: string | null;
  capabilities: RuntimeCapability[];
  observedAt: string;
}

export interface RouteRuntimeView extends AccountRoute {
  ready: boolean;
  reason?: string;
}

export interface RuntimeSnapshotView {
  desiredStateRevision: number;
  runtimeGeneration: number;
  credentialVersion: number;
  requestedState: AccountState;
  routes: RouteRuntimeView[];
}

export interface PendingSnapshotView extends RuntimeSnapshotView {
  state: "prepared" | "candidate_ready" | "candidate_failed" | "applied";
  reason?: string;
}
