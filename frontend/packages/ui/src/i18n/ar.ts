import type { Messages } from './en';

const ar: Messages = {
  app: { name: 'أوبن سبور', tagline: 'نظام تشغيل مفتوح وذكي لسلاسل الإمداد', console: 'وحدة التحكم', operator: 'وحدة تحكم المشغّل' },
  pillar: { trace: 'التتبع', link: 'الربط', act: 'التنفيذ', comply: 'الامتثال', food: 'الأغذية', settings: 'الإعدادات', ecosystem: 'المنظومة' },
  nav: {
    dashboard: 'لوحة المعلومات', serialization: 'الترقيم التسلسلي', epcisInbox: 'صندوق وارد EPCIS', serialLookup: 'البحث عن رقم تسلسلي',
    eventQuery: 'استعلام الأحداث', subscriptions: 'الاشتراكات', partners: 'الشركاء', shipments: 'الشحنات',
    b2bDocuments: 'مستندات B2B', supplierCompliance: 'امتثال الموردين', federation: 'نظراء الاتحاد',
    controlTower: 'برج المراقبة', agentApprovals: 'موافقات الوكلاء', cases: 'الحالات', assistant: 'المساعد',
    formsWorkflows: 'النماذج وسير العمل', alerts: 'التنبيهات التنظيمية', reports: 'التقارير التنظيمية', connectors: 'الموصلات',
    vrs: 'وحدة تحكم VRS', rulePacks: 'حزم القواعد', audit: 'سجل التدقيق', esignatures: 'التوقيعات الإلكترونية', lots: 'الدفعات',
    ctes: 'أحداث التتبع الحرجة', transformations: 'التحويلات', plots: 'قطع الأراضي', fsmaExport: 'تصدير FSMA 204',
    recalls: 'عمليات السحب', eudr: 'العناية الواجبة EUDR', products: 'البيانات الرئيسية', organizations: 'المؤسسات', sites: 'المواقع',
    licenses: 'التراخيص', users: 'المستخدمون والأدوار', apiKeys: 'مفاتيح API', branding: 'الهوية البصرية', usage: 'الاستخدام والخطة',
    dataExport: 'تصدير البيانات', tenants: 'المستأجرون', verify: 'المسح والتحقق', playground: 'ساحة تجربة EPCIS',
    marketplace: 'السوق', developerApps: 'تطبيقات المطورين', learn: 'مركز التعلم', benchmarks: 'المقارنات المعيارية',
    udiDevices: 'أجهزة UDI', coldChain: 'سلسلة التبريد',
  },
  action: {
    create: 'إنشاء', edit: 'تعديل', save: 'حفظ', cancel: 'إلغاء', delete: 'حذف', submit: 'إرسال', search: 'بحث',
    reset: 'إعادة تعيين', refresh: 'تحديث', upload: 'رفع', download: 'تنزيل', copy: 'نسخ', copied: 'تم النسخ',
    approve: 'موافقة', reject: 'رفض', revert: 'تراجع', resubmit: 'إعادة الإرسال', resolve: 'حل', send: 'إرسال',
    build: 'إنشاء', reconcile: 'مطابقة', verify: 'تحقق', validate: 'تحقق من الصحة', convert: 'تحويل',
    visualize: 'عرض مرئي', loadSample: 'تحميل مثال', signIn: 'تسجيل الدخول', signOut: 'تسجيل الخروج', signInSso: 'تسجيل الدخول عبر SSO',
    switchTenant: 'تبديل المستأجر', import: 'استيراد', export: 'تصدير', view: 'عرض', close: 'إغلاق', allocate: 'تخصيص',
    encode: 'ترميز', decode: 'فك الترميز', sign: 'توقيع', run: 'تشغيل', invite: 'دعوة', accept: 'قبول', revoke: 'إلغاء',
    impersonate: 'انتحال الهوية', scan: 'مسح', stopScan: 'إيقاف الكاميرا', lookup: 'بحث', query: 'تنفيذ الاستعلام', add: 'إضافة',
    remove: 'إزالة', preview: 'معاينة', verifyChain: 'التحقق من السلسلة', back: 'رجوع',
  },
  field: {
    id: 'المعرّف', name: 'الاسم', email: 'البريد الإلكتروني', password: 'كلمة المرور', tenant: 'المستأجر', status: 'الحالة', kind: 'النوع',
    type: 'النوع', country: 'الدولة', gtin: 'GTIN', gln: 'GLN', serial: 'الرقم التسلسلي', lot: 'رقم الدفعة', expiry: 'تاريخ الانتهاء',
    quantity: 'الكمية', createdAt: 'تاريخ الإنشاء', roles: 'الأدوار', reason: 'السبب', meaning: 'المعنى', market: 'السوق',
    product: 'المنتج', partner: 'الشريك', direction: 'الاتجاه', description: 'الوصف', severity: 'الخطورة',
    title: 'العنوان', actor: 'المنفّذ', action: 'الإجراء', entity: 'الكيان', entityId: 'معرّف الكيان', hash: 'البصمة', seq: 'التسلسل',
    at: 'الوقت', plan: 'الخطة', version: 'الإصدار', enabled: 'مفعّل', config: 'الإعدادات', code: 'الرمز', epc: 'EPC',
    eventTime: 'وقت الحدث', bizStep: 'خطوة العمل', disposition: 'الوضع', bizLocation: 'موقع العمل',
    eventType: 'نوع الحدث', destination: 'الوجهة', organization: 'المؤسسة', site: 'الموقع', number: 'الرقم',
    authority: 'الجهة المصدرة', validFrom: 'صالح من', validTo: 'صالح حتى', locale: 'اللغة', priority: 'الأولوية',
    commodity: 'السلعة', health: 'السلامة', usage: 'الاستخدام', events: 'الأحداث',
  },
  common: {
    loading: 'جارٍ التحميل…', empty: 'لا يوجد شيء بعد', error: 'حدث خطأ ما', yes: 'نعم', no: 'لا', all: 'الكل',
    open: 'مفتوح', resolved: 'تم الحل', language: 'اللغة', theme: 'المظهر', light: 'فاتح', dark: 'داكن',
    audited: 'يُسجَّل هذا الإجراء في سجل التدقيق المحمي من التلاعب.', required: 'مطلوب',
    invalidGtin: 'رمز GTIN-14 غير صالح (رقم التحقق)', invalidGln: 'رمز GLN-13 غير صالح (رقم التحقق)', success: 'تم',
    details: 'التفاصيل', result: 'النتيجة', total: 'الإجمالي', actions: 'الإجراءات', saved: 'تم الحفظ', notFound: 'الصفحة غير موجودة',
    comingFromContract: 'تُعرض البيانات كما تُرجعها واجهة البرمجة.', secretOnce: 'انسخ هذا السر الآن. لن يُعرض مرة أخرى.',
  },
  auth: {
    title: 'تسجيل الدخول إلى أوبن سبور', subtitle: 'التتبع · الربط · التنفيذ · الامتثال', tenantSlug: 'المستأجر',
    operatorTitle: 'تسجيل دخول المشغّل', operatorOnly: 'هذا الحساب ليس حساب مشغّل.', sessionExpired: 'انتهت صلاحية جلستك.',
  },
  esig: {
    title: 'التوقيع الإلكتروني', intro: 'أعد إدخال كلمة المرور للتوقيع. يرتبط توقيعك بسجل التدقيق.',
    meaningApprove: 'موافقة', meaningReview: 'مراجعة', meaningAuthor: 'تأليف', meaningResponsibility: 'مسؤولية',
    signed: 'تم تسجيل التوقيع',
  },
  verify: {
    title: 'المسح والتحقق', subtitle: 'تحقق من عبوة دواء أو رمز منتج', manual: 'إدخال الرمز يدويًا',
    manualPlaceholder: 'الصق نص DataMatrix أو (01)…(21)… أو رابط GS1 Digital Link', cameraUnsupported: 'المسح بالكاميرا غير مدعوم في هذا المتصفح. استخدم الإدخال اليدوي.',
    startCamera: 'المسح بالكاميرا', verified: 'تم التحقق', suspect: 'مشتبه به', notFound: 'غير موجود', recalled: 'مسحوب',
    expired: 'منتهي الصلاحية', unknown: 'غير معروف', scanAgain: 'مسح رمز آخر',
  },
  playground: {
    title: 'ساحة تجربة EPCIS', subtitle: 'تحقق من مستندات EPCIS 1.2 / 2.0 وحوّلها واعرضها مرئيًا',
    input: 'مستند EPCIS', valid: 'مستند صالح', invalid: 'مستند غير صالح', events: 'الأحداث', format: 'التنسيق',
    errors: 'الأخطاء', graph: 'المخطط', timeline: 'الخط الزمني', convertTo: 'تحويل إلى',
  },
  search: {
    placeholder: 'ابحث في الأرقام التسلسلية والمنتجات والشحنات…', shortcut: 'اضغط / للبحث', empty: 'لا توجد نتائج',
    kind: { serial: 'الأرقام التسلسلية', product: 'المنتجات', shipment: 'الشحنات', partner: 'الشركاء', lot: 'الدفعات', case: 'الحالات' },
  },
  table: {
    views: 'طرق العرض', saveView: 'حفظ طريقة العرض الحالية…', viewName: 'اسم طريقة العرض', noViews: 'لا توجد طرق عرض محفوظة',
    deleteView: 'حذف طريقة العرض', viewSaved: 'تم حفظ طريقة العرض', exportCsv: 'تصدير CSV', nothingToExport: 'لا توجد صفوف للتصدير',
  },
  operator: {
    tenants: 'المستأجرون', editTenant: 'تعديل المستأجر', impersonateReason: 'سبب الوصول للدعم',
    impersonated: 'بدأت جلسة الدعم', quotas: 'الحصص',
  },
};

export default ar;
