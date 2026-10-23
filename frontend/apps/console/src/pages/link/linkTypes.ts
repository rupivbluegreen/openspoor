/** API shapes for the mapping studio, message tracking, onboarding, directory and credentials. */

export interface Transform {
  op: 'trim' | 'upper' | 'lower' | 'date' | 'lookup' | 'constant' | 'concat';
  from?: string;
  to?: string;
  table?: Record<string, string>;
  value?: string;
  sources?: string[];
  separator?: string;
}

export interface MappingField {
  target: string;
  source?: string;
  transforms?: Transform[];
}

export interface FlatColumn {
  name: string;
  start: number;
  length: number;
}

export interface MappingOptions {
  delimiter?: string;
  noHeader?: boolean;
  recordsPath?: string;
  linesPath?: string;
  columns?: FlatColumn[];
  skipLines?: number;
}

export interface Mapping {
  id: string;
  name: string;
  partnerId?: string;
  docType: string;
  format: 'csv' | 'json' | 'flat' | 'xml' | 'x12' | 'edifact';
  options: MappingOptions;
  fields: MappingField[];
  updatedAt?: string;
}

export interface RowError {
  row: number;
  field?: string;
  message: string;
}

export interface MapResult {
  columns: string[];
  rows: number;
  sample: Record<string, string>[];
  documents?: unknown[];
  events?: unknown[];
  errors: RowError[];
}

export interface LogEntry {
  id: string;
  kind: 'b2b' | 'federation' | 'tits';
  direction: string;
  type: string;
  partnerId?: string;
  partnerName: string;
  reference: string;
  channel: string;
  status: string;
  attempts: number;
  lastError: string;
  retryable: boolean;
  createdAt: string;
  sentAt?: string;
  ackedAt?: string;
  updatedAt: string;
}

export interface MessageLog {
  items: LogEntry[];
  total: number;
  counts: Record<string, number>;
}

export interface SLA {
  partnerId: string;
  partnerName: string;
  ackHours: number;
  enabled: boolean;
}

export interface OnboardingItem {
  id: string;
  key: string;
  title: string;
  kind: 'manual' | 'test';
  channel: string;
  done: boolean;
  doneBy: string;
  doneAt?: string;
  note: string;
}

export interface TestRun {
  id: string;
  channel: string;
  passed: boolean;
  detail: Record<string, unknown>;
  createdAt: string;
}

export interface Onboarding {
  partnerId: string;
  started: boolean;
  template?: string;
  items: OnboardingItem[];
  tests: TestRun[];
  score: number;
}

export interface OnboardingTemplate {
  key: string;
  name: string;
  description: string;
}

export interface ProfileLicense {
  id?: string;
  kind: string;
  number: string;
  authority: string;
  country?: string;
  validTo?: string;
  verified?: boolean;
}

export interface Profile {
  tenantId: string;
  name: string;
  gln?: string;
  glns: string[];
  companyPrefixes: string[];
  country?: string;
  kind: string;
  description: string;
  website: string;
  contact: string;
  licenses: ProfileLicense[];
  endpoints?: Record<string, string>;
  published?: boolean;
  verified: boolean;
  verifiedAt?: string;
  verificationNote?: string;
}

export interface Check {
  name: string;
  ok: boolean;
  detail?: string;
}

export interface Verification {
  id: string;
  partnerId?: string;
  credentialId: string;
  issuer: string;
  subject: string;
  verified: boolean;
  checks: Check[];
  checkedAt: string;
}

export interface IssuedCredential {
  id: string;
  licenseId: string;
  credential: Record<string, unknown>;
  validFrom: string;
  validUntil?: string;
  revokedAt?: string;
  revokedReason: string;
  createdAt: string;
}


/** One entry of the B2B transaction type registry (GET /b2b/types). */
export interface TransactionType {
  key: string;
  name: string;
  description: string;
  category: string;
  flow: 'buyer_to_seller' | 'seller_to_buyer' | 'either';
  standards: { x12?: string; edifact?: string; edifactBgm?: string; idocBasic?: string; idocMessage?: string };
  formats: { x12Write: boolean; x12Read: boolean; edifactWrite: boolean; edifactRead: boolean; idocWrite: boolean };
  required: string[];
  fields: string[];
  todo?: string;
}

export interface TransactionTypeList {
  items: TransactionType[];
  total: number;
  categories: { key: string; count: number }[];
}

/** AS2 certificate overview (`GET /as2/certificates`). */
export interface As2CertView {
  subject: string;
  notBefore: string;
  notAfter: string;
  fingerprint: string;
  daysLeft: number;
  /** ok, 60d, 30d, 7d or expired */
  stage: string;
}

export interface As2Warning {
  code: string;
  severity: 'info' | 'warning' | 'error';
  message: string;
}

export interface As2Options {
  signatureAlgorithm: string;
  mdnSignatureAlgorithm: string;
  encryption: string;
  compress: boolean;
  legacy: boolean;
}

export interface As2PartnerCerts {
  partnerId: string;
  name: string;
  as2Id: string;
  current: As2CertView;
  next?: As2CertView;
  switchAt?: string;
  active: 'current' | 'next';
  options: As2Options;
  warnings: As2Warning[];
}

export interface As2StationCerts {
  as2Id: string;
  receiveUrl: string;
  current: As2CertView;
  next?: As2CertView;
  switchAt?: string;
  active: 'current' | 'next';
  warnings: As2Warning[];
}

export interface As2Overview {
  station: As2StationCerts | null;
  partners: As2PartnerCerts[];
  summary: { partners: number; expired: number; expiring: number; legacy: number; rotations: number };
  thresholdsDays: number[];
}
