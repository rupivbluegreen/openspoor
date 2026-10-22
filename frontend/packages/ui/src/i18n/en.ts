const en = {
  app: { name: 'Open Spoor', tagline: 'Open Agentic Supply Chain OS', console: 'Console', operator: 'Operator console' },
  pillar: { trace: 'Trace', link: 'Link', act: 'Act', comply: 'Comply', food: 'Food', settings: 'Settings', ecosystem: 'Ecosystem' },
  nav: {
    dashboard: 'Dashboard', serialization: 'Serialization', epcisInbox: 'EPCIS Inbox', serialLookup: 'Serial lookup',
    eventQuery: 'Event query', subscriptions: 'Subscriptions', partners: 'Partners', shipments: 'Shipments',
    b2bDocuments: 'B2B documents', supplierCompliance: 'Supplier compliance', federation: 'Federation peers',
    controlTower: 'Control tower', agentApprovals: 'Agent approvals', cases: 'Cases', assistant: 'Assistant',
    formsWorkflows: 'Forms & workflows', alerts: 'Regulatory alerts', reports: 'Regulatory reports', connectors: 'Connectors',
    vrs: 'VRS console', rulePacks: 'Rule packs', audit: 'Audit trail', esignatures: 'E-signatures', lots: 'Lots',
    ctes: 'Critical tracking events', transformations: 'Transformations', plots: 'Plots', fsmaExport: 'FSMA 204 export',
    recalls: 'Recalls', eudr: 'EUDR due diligence', products: 'Master data', organizations: 'Organizations', sites: 'Sites',
    licenses: 'Licenses', users: 'Users & roles', apiKeys: 'API keys', branding: 'Branding', usage: 'Usage & plan',
    dataExport: 'Data export', tenants: 'Tenants', verify: 'Scan & verify', playground: 'EPCIS Playground',
    marketplace: 'Marketplace', developerApps: 'Developer apps', learn: 'Learning hub', benchmarks: 'Benchmarks',
    udiDevices: 'UDI devices', coldChain: 'Cold chain',
  },
  action: {
    create: 'Create', edit: 'Edit', save: 'Save', cancel: 'Cancel', delete: 'Delete', submit: 'Submit', search: 'Search',
    reset: 'Reset', refresh: 'Refresh', upload: 'Upload', download: 'Download', copy: 'Copy', copied: 'Copied',
    approve: 'Approve', reject: 'Reject', revert: 'Revert', resubmit: 'Resubmit', resolve: 'Resolve', send: 'Send',
    build: 'Build', reconcile: 'Reconcile', verify: 'Verify', validate: 'Validate', convert: 'Convert',
    visualize: 'Visualize', loadSample: 'Load sample', signIn: 'Sign in', signOut: 'Sign out', signInSso: 'Sign in with SSO',
    switchTenant: 'Switch tenant', import: 'Import', export: 'Export', view: 'View', close: 'Close', allocate: 'Allocate',
    encode: 'Encode', decode: 'Decode', sign: 'Sign', run: 'Run', invite: 'Invite', accept: 'Accept', revoke: 'Revoke',
    impersonate: 'Impersonate', scan: 'Scan', stopScan: 'Stop camera', lookup: 'Look up', query: 'Run query', add: 'Add',
    remove: 'Remove', preview: 'Preview', verifyChain: 'Verify chain', back: 'Back',
  },
  field: {
    id: 'ID', name: 'Name', email: 'Email', password: 'Password', tenant: 'Tenant', status: 'Status', kind: 'Kind',
    type: 'Type', country: 'Country', gtin: 'GTIN', gln: 'GLN', serial: 'Serial', lot: 'Lot', expiry: 'Expiry',
    quantity: 'Quantity', createdAt: 'Created', roles: 'Roles', reason: 'Reason', meaning: 'Meaning', market: 'Market',
    product: 'Product', partner: 'Partner', direction: 'Direction', description: 'Description', severity: 'Severity',
    title: 'Title', actor: 'Actor', action: 'Action', entity: 'Entity', entityId: 'Entity ID', hash: 'Hash', seq: 'Seq',
    at: 'At', plan: 'Plan', version: 'Version', enabled: 'Enabled', config: 'Configuration', code: 'Code', epc: 'EPC',
    eventTime: 'Event time', bizStep: 'Business step', disposition: 'Disposition', bizLocation: 'Business location',
    eventType: 'Event type', destination: 'Destination', organization: 'Organization', site: 'Site', number: 'Number',
    authority: 'Authority', validFrom: 'Valid from', validTo: 'Valid to', locale: 'Locale', priority: 'Priority',
    commodity: 'Commodity', health: 'Health', usage: 'Usage', events: 'Events',
  },
  common: {
    loading: 'Loading…', empty: 'Nothing here yet', error: 'Something went wrong', yes: 'Yes', no: 'No', all: 'All',
    open: 'Open', resolved: 'Resolved', language: 'Language', theme: 'Theme', light: 'Light', dark: 'Dark',
    audited: 'This action is recorded in the tamper-evident audit trail.', required: 'Required',
    invalidGtin: 'Invalid GTIN-14 (check digit)', invalidGln: 'Invalid GLN-13 (check digit)', success: 'Done',
    details: 'Details', result: 'Result', total: 'Total', actions: 'Actions', saved: 'Saved', notFound: 'Page not found',
    comingFromContract: 'Data is shown as returned by the API.', secretOnce: 'Copy this secret now. It will not be shown again.',
  },
  auth: {
    title: 'Sign in to Open Spoor', subtitle: 'Trace · Link · Act · Comply', tenantSlug: 'Tenant',
    operatorTitle: 'Operator sign in', operatorOnly: 'This account is not an operator.', sessionExpired: 'Your session has expired.',
  },
  esig: {
    title: 'Electronic signature', intro: 'Re-enter your password to sign. Your signature is bound to the audit trail.',
    meaningApprove: 'Approval', meaningReview: 'Review', meaningAuthor: 'Authorship', meaningResponsibility: 'Responsibility',
    signed: 'Signature recorded',
  },
  verify: {
    title: 'Scan & verify', subtitle: 'Check a medicine pack or product code', manual: 'Enter code manually',
    manualPlaceholder: 'Paste DataMatrix text, (01)…(21)… or a GS1 Digital Link URL', cameraUnsupported: 'Camera scanning is not supported in this browser. Use manual entry.',
    startCamera: 'Scan with camera', verified: 'Verified', suspect: 'Suspect', notFound: 'Not found', recalled: 'Recalled',
    expired: 'Expired', unknown: 'Unknown', scanAgain: 'Scan another',
  },
  playground: {
    title: 'EPCIS Playground', subtitle: 'Validate, convert and visualize EPCIS 1.2 / 2.0 documents',
    input: 'EPCIS document', valid: 'Valid document', invalid: 'Invalid document', events: 'Events', format: 'Format',
    errors: 'Errors', graph: 'Graph', timeline: 'Timeline', convertTo: 'Convert to',
  },
  operator: {
    tenants: 'Tenants', editTenant: 'Edit tenant', impersonateReason: 'Reason for support access',
    impersonated: 'Support session started', quotas: 'Quotas',
  },
};

export default en;

type DeepString<T> = { [K in keyof T]: T[K] extends string ? string : DeepString<T[K]> };
export type Messages = DeepString<typeof en>;
