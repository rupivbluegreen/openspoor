import type { Messages } from './en';

const nl: Messages = {
  app: { name: 'Open Spoor', tagline: 'Open agentisch supply-chain-OS', console: 'Console', operator: 'Beheerdersconsole' },
  pillar: { trace: 'Traceren', link: 'Verbinden', act: 'Handelen', comply: 'Naleven', food: 'Voedsel', settings: 'Instellingen', ecosystem: 'Ecosysteem' },
  nav: {
    dashboard: 'Dashboard', serialization: 'Serialisatie', epcisInbox: 'EPCIS-inbox', serialLookup: 'Serienummer opzoeken',
    eventQuery: 'Gebeurtenissen zoeken', subscriptions: 'Abonnementen', partners: 'Partners', shipments: 'Zendingen',
    b2bDocuments: 'B2B-documenten', supplierCompliance: 'Leveranciersnaleving', federation: 'Federatiepeers',
    controlTower: 'Controletoren', agentApprovals: 'Agentgoedkeuringen', cases: 'Zaken', assistant: 'Assistent',
    formsWorkflows: 'Formulieren & workflows', alerts: 'Regelgevingsmeldingen', reports: 'Regelgevingsrapporten', connectors: 'Connectoren',
    vrs: 'VRS-console', rulePacks: 'Regelpakketten', audit: 'Audittrail', esignatures: 'E-handtekeningen', lots: 'Partijen',
    ctes: 'Kritieke traceergebeurtenissen', transformations: 'Transformaties', plots: 'Percelen', fsmaExport: 'FSMA 204-export',
    recalls: 'Terugroepacties', eudr: 'EUDR-zorgvuldigheid', products: 'Stamgegevens', organizations: 'Organisaties', sites: 'Locaties',
    licenses: 'Vergunningen', users: 'Gebruikers & rollen', apiKeys: 'API-sleutels', branding: 'Huisstijl', usage: 'Gebruik & abonnement',
    dataExport: 'Gegevensexport', tenants: 'Tenants', verify: 'Scannen & verifiëren', playground: 'EPCIS-speeltuin',
    marketplace: 'Marktplaats', developerApps: 'Ontwikkelaarsapps', learn: 'Kennisbank', benchmarks: 'Benchmarks',
    udiDevices: 'UDI-hulpmiddelen', coldChain: 'Koudeketen',
  },
  action: {
    create: 'Aanmaken', edit: 'Bewerken', save: 'Opslaan', cancel: 'Annuleren', delete: 'Verwijderen', submit: 'Indienen', search: 'Zoeken',
    reset: 'Herstellen', refresh: 'Vernieuwen', upload: 'Uploaden', download: 'Downloaden', copy: 'Kopiëren', copied: 'Gekopieerd',
    approve: 'Goedkeuren', reject: 'Afwijzen', revert: 'Terugdraaien', resubmit: 'Opnieuw indienen', resolve: 'Afhandelen', send: 'Versturen',
    build: 'Opbouwen', reconcile: 'Afstemmen', verify: 'Verifiëren', validate: 'Valideren', convert: 'Converteren',
    visualize: 'Visualiseren', loadSample: 'Voorbeeld laden', signIn: 'Inloggen', signOut: 'Uitloggen', signInSso: 'Inloggen met SSO',
    switchTenant: 'Tenant wisselen', import: 'Importeren', export: 'Exporteren', view: 'Bekijken', close: 'Sluiten', allocate: 'Toewijzen',
    encode: 'Coderen', decode: 'Decoderen', sign: 'Ondertekenen', run: 'Uitvoeren', invite: 'Uitnodigen', accept: 'Accepteren', revoke: 'Intrekken',
    impersonate: 'Meekijken', scan: 'Scannen', stopScan: 'Camera stoppen', lookup: 'Opzoeken', query: 'Zoekopdracht uitvoeren', add: 'Toevoegen',
    remove: 'Verwijderen', preview: 'Voorbeeld', verifyChain: 'Keten verifiëren', back: 'Terug',
  },
  field: {
    id: 'ID', name: 'Naam', email: 'E-mail', password: 'Wachtwoord', tenant: 'Tenant', status: 'Status', kind: 'Soort',
    type: 'Type', country: 'Land', gtin: 'GTIN', gln: 'GLN', serial: 'Serienummer', lot: 'Partij', expiry: 'Vervaldatum',
    quantity: 'Aantal', createdAt: 'Aangemaakt', roles: 'Rollen', reason: 'Reden', meaning: 'Betekenis', market: 'Markt',
    product: 'Product', partner: 'Partner', direction: 'Richting', description: 'Omschrijving', severity: 'Ernst',
    title: 'Titel', actor: 'Actor', action: 'Actie', entity: 'Entiteit', entityId: 'Entiteit-ID', hash: 'Hash', seq: 'Volgnr.',
    at: 'Tijdstip', plan: 'Abonnement', version: 'Versie', enabled: 'Ingeschakeld', config: 'Configuratie', code: 'Code', epc: 'EPC',
    eventTime: 'Gebeurtenistijd', bizStep: 'Bedrijfsstap', disposition: 'Dispositie', bizLocation: 'Bedrijfslocatie',
    eventType: 'Gebeurtenistype', destination: 'Bestemming', organization: 'Organisatie', site: 'Locatie', number: 'Nummer',
    authority: 'Instantie', validFrom: 'Geldig vanaf', validTo: 'Geldig tot', locale: 'Taal', priority: 'Prioriteit',
    commodity: 'Grondstof', health: 'Gezondheid', usage: 'Gebruik', events: 'Gebeurtenissen',
  },
  common: {
    loading: 'Laden…', empty: 'Nog niets hier', error: 'Er is iets misgegaan', yes: 'Ja', no: 'Nee', all: 'Alle',
    open: 'Open', resolved: 'Afgehandeld', language: 'Taal', theme: 'Thema', light: 'Licht', dark: 'Donker',
    audited: 'Deze actie wordt vastgelegd in de manipulatiebestendige audittrail.', required: 'Verplicht',
    invalidGtin: 'Ongeldige GTIN-14 (controlecijfer)', invalidGln: 'Ongeldige GLN-13 (controlecijfer)', success: 'Gereed',
    details: 'Details', result: 'Resultaat', total: 'Totaal', actions: 'Acties', saved: 'Opgeslagen', notFound: 'Pagina niet gevonden',
    comingFromContract: 'Gegevens worden getoond zoals de API ze teruggeeft.', secretOnce: 'Kopieer dit geheim nu. Het wordt niet opnieuw getoond.',
  },
  auth: {
    title: 'Inloggen bij Open Spoor', subtitle: 'Traceren · Verbinden · Handelen · Naleven', tenantSlug: 'Tenant',
    operatorTitle: 'Inloggen als beheerder', operatorOnly: 'Dit account is geen beheerder.', sessionExpired: 'Uw sessie is verlopen.',
  },
  esig: {
    title: 'Elektronische handtekening', intro: 'Voer uw wachtwoord opnieuw in om te ondertekenen. Uw handtekening wordt aan de audittrail gekoppeld.',
    meaningApprove: 'Goedkeuring', meaningReview: 'Beoordeling', meaningAuthor: 'Auteurschap', meaningResponsibility: 'Verantwoordelijkheid',
    signed: 'Handtekening vastgelegd',
  },
  verify: {
    title: 'Scannen & verifiëren', subtitle: 'Controleer een medicijnverpakking of productcode', manual: 'Code handmatig invoeren',
    manualPlaceholder: 'Plak DataMatrix-tekst, (01)…(21)… of een GS1 Digital Link-URL', cameraUnsupported: 'Scannen met de camera wordt niet ondersteund in deze browser. Gebruik handmatige invoer.',
    startCamera: 'Scannen met camera', verified: 'Geverifieerd', suspect: 'Verdacht', notFound: 'Niet gevonden', recalled: 'Teruggeroepen',
    expired: 'Verlopen', unknown: 'Onbekend', scanAgain: 'Nog een scannen',
  },
  playground: {
    title: 'EPCIS-speeltuin', subtitle: 'Valideer, converteer en visualiseer EPCIS 1.2 / 2.0-documenten',
    input: 'EPCIS-document', valid: 'Geldig document', invalid: 'Ongeldig document', events: 'Gebeurtenissen', format: 'Formaat',
    errors: 'Fouten', graph: 'Grafiek', timeline: 'Tijdlijn', convertTo: 'Converteren naar',
  },
  search: {
    placeholder: 'Zoek serienummers, producten, zendingen…', shortcut: 'Druk op / om te zoeken', empty: 'Geen resultaten',
    kind: { serial: 'Serienummers', product: 'Producten', shipment: 'Zendingen', partner: 'Partners', lot: 'Partijen', case: 'Zaken' },
  },
  table: {
    views: 'Weergaven', saveView: 'Huidige weergave opslaan…', viewName: 'Naam van weergave', noViews: 'Geen opgeslagen weergaven',
    deleteView: 'Weergave verwijderen', viewSaved: 'Weergave opgeslagen', exportCsv: 'CSV exporteren', nothingToExport: 'Geen rijen om te exporteren',
  },
  operator: {
    tenants: 'Tenants', editTenant: 'Tenant bewerken', impersonateReason: 'Reden voor ondersteuningstoegang',
    impersonated: 'Ondersteuningssessie gestart', quotas: 'Quota',
  },
};

export default nl;
