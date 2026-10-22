/* Domain types derived from docs/api-contract.md and backend/internal/platform.
 * Fields not pinned down by the contract are optional so the UI stays tolerant. */

export type Role = 'Admin' | 'Serialization' | 'Compliance' | 'Ops' | 'Integration' | 'Viewer';
export const ROLES: Role[] = ['Admin', 'Serialization', 'Compliance', 'Ops', 'Integration', 'Viewer'];

export interface Meta {
  name: string;
  version: string;
  mode: string;
  modules: string[];
  devAuth?: boolean;
  oidc?: boolean;
}

export interface Principal {
  tenantId: string;
  userId: string;
  email?: string;
  name?: string;
  roles: string[];
  kind: 'user' | 'apikey' | 'system' | string;
  operator?: boolean;
  keyId?: string;
}

export interface TokenResponse {
  token: string;
  expiresAt: string;
  principal: Principal;
}

export interface Brand {
  primaryColor?: string;
  logoUrl?: string;
  [k: string]: unknown;
}

export interface Tenant {
  id: string;
  slug: string;
  name: string;
  plan: string;
  status: string;
  region?: string;
  verticals?: string[];
  brand?: Brand | null;
  customDomain?: string;
  createdAt?: string;
}

export interface Organization {
  id: string;
  name: string;
  kind: string;
  companyPrefix?: string;
  country?: string;
  createdAt?: string;
}
export const ORG_KINDS = [
  'manufacturer', 'cmo', '3pl', 'wholesaler', 'pharmacy', 'hospital', 'regulator',
  'grower', 'processor', 'distributor', 'retailer', 'restaurant', 'importer',
] as const;

export interface Site {
  id: string;
  orgId: string;
  gln: string;
  name: string;
  kind: string;
  country?: string;
  address?: Record<string, unknown>;
  createdAt?: string;
}

export interface License {
  id: string;
  orgId: string;
  siteId?: string;
  kind: string;
  number: string;
  authority: string;
  country?: string;
  validFrom?: string;
  validTo?: string;
  createdAt?: string;
}

export interface User {
  id: string;
  email: string;
  name: string;
  roles: string[];
  status: string;
  locale: string;
  createdAt?: string;
}

export interface ApiKey {
  id: string;
  name: string;
  prefix: string;
  roles: string[];
  createdBy?: string;
  expiresAt?: string;
  revokedAt?: string;
  lastUsedAt?: string;
  createdAt?: string;
  secret?: string;
}

export interface AuditEntry {
  id?: number;
  seq: number;
  at: string;
  actor: string;
  action: string;
  entity: string;
  entityId: string;
  before?: unknown;
  after?: unknown;
  reason?: string;
  hash: string;
}

export interface AuditVerify {
  checked: number;
  intact: boolean;
  brokenAtSeq?: number;
}

export interface ESignature {
  id: string;
  userId?: string;
  email: string;
  meaning: string;
  reason: string;
  entity: string;
  entityId: string;
  auditSeq?: number;
  signedAt: string;
}

export interface Alert {
  id: string;
  kind: string;
  severity: string;
  title: string;
  detail?: Record<string, unknown> & { reportId?: string };
  status: string;
  entity?: string;
  entityId?: string;
  createdAt: string;
  resolvedAt?: string;
}

export interface Product {
  id: string;
  gtin: string;
  name: string;
  description?: string;
  companyPrefixLength?: number;
  ndc?: string;
  marketCodes?: Record<string, string>;
  targetMarkets?: string[];
  dosageForm?: string;
  strength?: string;
  packSize?: string | number;
  vertical?: string;
}

export type PackagingLevelName = 'unit' | 'bundle' | 'case' | 'pallet' | 'shipment';
export interface PackagingLevel {
  level: PackagingLevelName;
  gtin?: string;
  quantity: number;
}

export interface SerialPool {
  id: string;
  productId: string;
  market: string;
  mode: 'random' | 'sequential';
  alphabet?: string;
  length: number;
  prefix?: string;
  lowWatermarkPct?: number;
  capacity?: number;
  issued?: number;
  remaining?: number;
  gtin?: string;
  createdAt?: string;
}

export interface Allocation {
  allocationId: string;
  poolId: string;
  gtin: string;
  serials: string[];
  start?: string | number;
  count: number;
}

export interface EncodeResult {
  elementString: string;
  hri: string;
  digitalLink: string;
  urn: string;
  image?: string;
}

export interface DecodeResult {
  gtin?: string;
  serial?: string;
  lot?: string;
  expiry?: string;
  urn?: string;
  digitalLink?: string;
}

export interface EventError {
  index?: number;
  eventId?: string;
  field?: string;
  path?: string;
  message?: string;
  [k: string]: unknown;
}

export interface CaptureJob {
  id?: string;
  captureId?: string;
  status: string;
  eventCount?: number;
  errorCount?: number;
  errors?: (EventError | string)[];
  createdAt?: string;
  finishedAt?: string;
}

export interface EpcisEvent {
  type?: string;
  eventID?: string;
  eventTime?: string;
  bizStep?: string;
  disposition?: string;
  action?: string;
  epcList?: string[];
  parentID?: string;
  childEPCs?: string[];
  bizLocation?: { id?: string } | string;
  readPoint?: { id?: string } | string;
  [k: string]: unknown;
}

export interface SerialTrace {
  state?: { status?: string; disposition?: string; bizLocation?: string; parentEpc?: string; lastEventTime?: string };
  history?: EpcisEvent[];
  children?: string[];
  ancestors?: string[];
}

export interface TreeNode {
  epc: string;
  children?: TreeNode[];
}

export interface TraceStats {
  events?: number;
  serials?: number;
  captures24h?: number;
  byStatus?: Record<string, number>;
}

export interface Subscription {
  id: string;
  queryName?: string;
  query?: Record<string, unknown>;
  dest: string;
  createdAt?: string;
}

export interface Shipment {
  id: string;
  direction: 'inbound' | 'outbound' | string;
  partnerId?: string;
  poNumber?: string;
  shipDate?: string;
  items?: unknown[];
  status?: string;
  createdAt?: string;
}

export interface ReconcileResult {
  matched?: number | boolean | string[];
  missing?: string[];
  unexpected?: string[];
}

export interface VrsRequest {
  id?: string;
  gtin: string;
  serial: string;
  lot?: string;
  expiry?: string;
  requestorGln?: string;
  context?: string;
  verified?: boolean;
  responder?: string;
  latencyMs?: number;
  createdAt?: string;
}

export interface VrsDirectoryEntry {
  id?: string;
  gtinPrefix: string;
  responderUrl: string;
  auth?: string;
}

export interface ConnectorDef {
  id?: string;
  name: string;
  title?: string;
  description?: string;
  country?: string;
  configSchema?: JsonSchema;
  schema?: JsonSchema;
}

export interface ConnectorInstance {
  id: string;
  connector: string;
  name: string;
  config?: Record<string, unknown>;
  enabled?: boolean;
  createdAt?: string;
}

export interface Report {
  id: string;
  connector: string;
  status: string;
  attempts?: number;
  response?: unknown;
  createdAt?: string;
}

export interface RulePack {
  id?: string;
  name?: string;
  country?: string;
  version?: string;
  yaml?: string;
  [k: string]: unknown;
}

export interface Partner {
  id: string;
  name: string;
  gln?: string;
  status?: string;
  endpoints?: Record<string, string>;
  health?: string | Record<string, unknown>;
}

export interface B2BDocument {
  id: string;
  type: string;
  partnerId?: string;
  payload?: unknown;
  status?: string;
  createdAt?: string;
}

export interface Certificate {
  id?: string;
  supplier?: string;
  supplierId?: string;
  partnerId?: string;
  kind?: string;
  type?: string;
  number?: string;
  validTo?: string;
  expiresAt?: string;
  [k: string]: unknown;
}

export interface FederationPeer {
  id?: string;
  name?: string;
  url?: string;
  publicKey?: string;
  status?: string;
  [k: string]: unknown;
}

export interface AgentAction {
  id: string;
  agent?: string;
  kind?: string;
  title?: string;
  summary?: string;
  reasoning?: string | string[];
  diff?: { before?: unknown; after?: unknown } | string;
  before?: unknown;
  after?: unknown;
  status?: string;
  confidence?: number;
  createdAt?: string;
}

export interface AgentDef {
  name: string;
  description?: string;
  enabled?: boolean;
  [k: string]: unknown;
}

export interface Case {
  id: string;
  title?: string;
  status?: string;
  priority?: string | number;
  kind?: string;
  assignee?: string;
  entity?: string;
  entityId?: string;
  createdAt?: string;
}

export interface Monitor {
  id?: string;
  name?: string;
  status?: string;
  value?: number | string;
  description?: string;
}

export interface ControlTower {
  monitors?: Monitor[];
  cases?: Case[];
  queue?: Case[];
}

export interface FormDef {
  id?: string;
  name: string;
  schema?: JsonSchema;
  createdAt?: string;
  [k: string]: unknown;
}

export interface ChatMessage {
  role: 'user' | 'assistant' | 'system';
  content: string;
}

export interface ChatReply {
  reply: string;
  citations?: (string | { title?: string; url?: string; ref?: string })[];
}

export interface FoodLot {
  id?: string;
  tlc: string;
  productId?: string;
  quantity?: number;
  uom?: string;
  siteId?: string;
  harvestDate?: string;
  plotIds?: string[];
}

export interface Cte {
  id?: string;
  type: string;
  tlc: string;
  kdes?: Record<string, unknown>;
  createdAt?: string;
}

export interface Plot {
  id?: string;
  name: string;
  country?: string;
  geojson?: unknown;
  commodity?: string;
}

export interface Recall {
  id: string;
  title?: string;
  status?: string;
  reason?: string;
  tlcs?: string[];
  affectedLots?: unknown[];
  affectedCustomers?: unknown[];
  createdAt?: string;
}

export interface Dds {
  id?: string;
  reference?: string;
  status?: string;
  commodity?: string;
  plotIds?: string[];
  createdAt?: string;
  [k: string]: unknown;
}

export interface TenantUsage {
  period?: string;
  events?: number;
  reports?: number;
  activePartners?: number;
  quota?: Record<string, number>;
}

export interface OperatorTenant extends Tenant {
  usage?: TenantUsage | Record<string, number>;
  health?: string | Record<string, unknown>;
  quotas?: Record<string, number>;
}

export interface VerifyScanResult {
  verified: boolean;
  status: string;
  product?: { name?: string; gtin?: string; [k: string]: unknown };
  pack?: { gtin?: string; serial?: string; lot?: string; expiry?: string; [k: string]: unknown };
  message?: string;
}

export interface PlaygroundValidate {
  valid: boolean;
  format?: string;
  eventCount?: number;
  errors?: (EventError | string)[];
}

export interface VizNode {
  id: string;
  label?: string;
  kind?: string;
  type?: string;
}
export interface VizEdge {
  from?: string;
  to?: string;
  source?: string;
  target?: string;
  label?: string;
}
export interface VizTimelineItem {
  time?: string;
  eventTime?: string;
  label?: string;
  type?: string;
  bizStep?: string;
  [k: string]: unknown;
}
export interface PlaygroundVisualize {
  nodes?: VizNode[];
  edges?: VizEdge[];
  timeline?: VizTimelineItem[];
}

/** Minimal JSON-schema subset used by connector config and form designer. */
export interface JsonSchema {
  type?: string | string[];
  title?: string;
  description?: string;
  properties?: Record<string, JsonSchema>;
  required?: string[];
  enum?: (string | number)[];
  default?: unknown;
  format?: string;
  items?: JsonSchema;
}
