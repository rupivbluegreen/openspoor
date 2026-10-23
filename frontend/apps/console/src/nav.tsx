import { lazy, type ComponentType, type LazyExoticComponent, type ReactNode } from 'react';
import {
  ApartmentOutlined, AlertOutlined, ApiOutlined, AuditOutlined, BarcodeOutlined, BgColorsOutlined,
  CheckSquareOutlined, ClusterOutlined, CloudDownloadOutlined, CommentOutlined, ContainerOutlined, DashboardOutlined,
  DatabaseOutlined, DeploymentUnitOutlined, EnvironmentOutlined, ExperimentOutlined, FileDoneOutlined, FileProtectOutlined,
  FileSearchOutlined, FileTextOutlined, FormOutlined, GlobalOutlined, IdcardOutlined, InboxOutlined, KeyOutlined,
  LineChartOutlined, NodeIndexOutlined, NotificationOutlined, RadarChartOutlined,
  RobotOutlined, RollbackOutlined, SafetyCertificateOutlined, SafetyOutlined, SearchOutlined,
  SettingOutlined, ShopOutlined, SolutionOutlined, TagsOutlined, TeamOutlined, ThunderboltOutlined,
  TruckOutlined, UserOutlined, AimOutlined, ControlOutlined, SwapOutlined,
  AppstoreAddOutlined, AppstoreOutlined, BarChartOutlined, ProfileOutlined, CodeOutlined, FieldTimeOutlined, MedicineBoxOutlined, ReadOutlined,
} from '@ant-design/icons';

type Page = LazyExoticComponent<ComponentType>;

export interface NavItem {
  path: string;
  /** i18n key under `nav.` */
  label: string;
  icon: ReactNode;
  page: Page;
  /** Extra non-menu routes rendered by the same group (e.g. detail pages). */
  children?: { path: string; page: Page }[];
}

export interface NavGroup {
  key: 'trace' | 'link' | 'act' | 'comply' | 'food' | 'ecosystem' | 'settings';
  icon: ReactNode;
  items: NavItem[];
  foodOnly?: boolean;
}

const p = (f: () => Promise<{ default: ComponentType }>) => lazy(f);

export const NAV: NavGroup[] = [
  {
    key: 'trace',
    icon: <AimOutlined />,
    items: [
      { path: '/trace/dashboard', label: 'dashboard', icon: <DashboardOutlined />, page: p(() => import('./pages/trace/Dashboard')) },
      { path: '/trace/serialization', label: 'serialization', icon: <BarcodeOutlined />, page: p(() => import('./pages/trace/Serialization')) },
      {
        path: '/trace/epcis', label: 'epcisInbox', icon: <InboxOutlined />, page: p(() => import('./pages/trace/EpcisInbox')),
        children: [{ path: '/trace/epcis/:id', page: p(() => import('./pages/trace/CaptureDetail')) }],
      },
      { path: '/trace/lookup', label: 'serialLookup', icon: <SearchOutlined />, page: p(() => import('./pages/trace/SerialLookup')) },
      { path: '/trace/events', label: 'eventQuery', icon: <FileSearchOutlined />, page: p(() => import('./pages/trace/EventQuery')) },
      { path: '/trace/subscriptions', label: 'subscriptions', icon: <NotificationOutlined />, page: p(() => import('./pages/trace/Subscriptions')) },
    ],
  },
  {
    key: 'link',
    icon: <ClusterOutlined />,
    items: [
      { path: '/link/partners', label: 'partners', icon: <TeamOutlined />, page: p(() => import('./pages/link/Partners')) },
      { path: '/link/shipments', label: 'shipments', icon: <TruckOutlined />, page: p(() => import('./pages/link/Shipments')) },
      { path: '/link/b2b', label: 'b2bDocuments', icon: <FileTextOutlined />, page: p(() => import('./pages/link/B2BDocuments')) },
      { path: '/link/suppliers', label: 'supplierCompliance', icon: <FileProtectOutlined />, page: p(() => import('./pages/link/SupplierCompliance')) },
      { path: '/link/federation', label: 'federation', icon: <DeploymentUnitOutlined />, page: p(() => import('./pages/link/Federation')) },
    ],
  },
  {
    key: 'act',
    icon: <ThunderboltOutlined />,
    items: [
      { path: '/act/control-tower', label: 'controlTower', icon: <RadarChartOutlined />, page: p(() => import('./pages/act/ControlTower')) },
      { path: '/act/approvals', label: 'agentApprovals', icon: <RobotOutlined />, page: p(() => import('./pages/act/AgentApprovals')) },
      { path: '/act/cases', label: 'cases', icon: <SolutionOutlined />, page: p(() => import('./pages/act/Cases')) },
      { path: '/act/assistant', label: 'assistant', icon: <CommentOutlined />, page: p(() => import('./pages/act/Assistant')) },
      { path: '/act/designer', label: 'formsWorkflows', icon: <FormOutlined />, page: p(() => import('./pages/act/Designer')) },
    ],
  },
  {
    key: 'comply',
    icon: <SafetyOutlined />,
    items: [
      { path: '/comply/alerts', label: 'alerts', icon: <AlertOutlined />, page: p(() => import('./pages/comply/Alerts')) },
      { path: '/comply/reports', label: 'reports', icon: <FileDoneOutlined />, page: p(() => import('./pages/comply/Reports')) },
      { path: '/comply/connectors', label: 'connectors', icon: <ApiOutlined />, page: p(() => import('./pages/comply/Connectors')) },
      { path: '/comply/vrs', label: 'vrs', icon: <CheckSquareOutlined />, page: p(() => import('./pages/comply/Vrs')) },
      { path: '/comply/rulepacks', label: 'rulePacks', icon: <ControlOutlined />, page: p(() => import('./pages/comply/RulePacks')) },
      { path: '/comply/audit', label: 'audit', icon: <AuditOutlined />, page: p(() => import('./pages/comply/Audit')) },
      { path: '/comply/esignatures', label: 'esignatures', icon: <SafetyCertificateOutlined />, page: p(() => import('./pages/comply/ESignatures')) },
      { path: '/comply/consumer-info', label: 'consumerInfo', icon: <ProfileOutlined />, page: p(() => import('./pages/comply/ConsumerInfo')) },
    ],
  },
  {
    key: 'food',
    icon: <ExperimentOutlined />,
    foodOnly: true,
    items: [
      { path: '/food/lots', label: 'lots', icon: <TagsOutlined />, page: p(() => import('./pages/food/Lots')) },
      { path: '/food/ctes', label: 'ctes', icon: <NodeIndexOutlined />, page: p(() => import('./pages/food/Ctes')) },
      { path: '/food/transformations', label: 'transformations', icon: <SwapOutlined />, page: p(() => import('./pages/food/Transformations')) },
      { path: '/food/plots', label: 'plots', icon: <EnvironmentOutlined />, page: p(() => import('./pages/food/Plots')) },
      { path: '/food/fsma', label: 'fsmaExport', icon: <CloudDownloadOutlined />, page: p(() => import('./pages/food/Fsma')) },
      { path: '/food/recalls', label: 'recalls', icon: <RollbackOutlined />, page: p(() => import('./pages/food/Recalls')) },
      { path: '/food/eudr', label: 'eudr', icon: <GlobalOutlined />, page: p(() => import('./pages/food/Eudr')) },
      { path: '/food/cold-chain', label: 'coldChain', icon: <FieldTimeOutlined />, page: p(() => import('./pages/food/ColdChain')) },
    ],
  },
  {
    key: 'ecosystem',
    icon: <AppstoreAddOutlined />,
    items: [
      { path: '/ecosystem/marketplace', label: 'marketplace', icon: <AppstoreOutlined />, page: p(() => import('./pages/ecosystem/Marketplace')) },
      { path: '/ecosystem/developer', label: 'developerApps', icon: <CodeOutlined />, page: p(() => import('./pages/ecosystem/DeveloperApps')) },
      {
        path: '/ecosystem/learn', label: 'learn', icon: <ReadOutlined />, page: p(() => import('./pages/ecosystem/Learn')),
        children: [{ path: '/ecosystem/learn/:slug', page: p(() => import('./pages/ecosystem/LearnArticle')) }],
      },
      { path: '/ecosystem/benchmarks', label: 'benchmarks', icon: <BarChartOutlined />, page: p(() => import('./pages/ecosystem/Benchmarks')) },
      { path: '/ecosystem/udi', label: 'udiDevices', icon: <MedicineBoxOutlined />, page: p(() => import('./pages/ecosystem/UdiDevices')) },
    ],
  },
  {
    key: 'settings',
    icon: <SettingOutlined />,
    items: [
      {
        path: '/settings/products', label: 'products', icon: <DatabaseOutlined />, page: p(() => import('./pages/settings/Products')),
        children: [{ path: '/settings/products/:id', page: p(() => import('./pages/settings/ProductDetail')) }],
      },
      { path: '/settings/orgs', label: 'organizations', icon: <ApartmentOutlined />, page: p(() => import('./pages/settings/Organizations')) },
      { path: '/settings/sites', label: 'sites', icon: <ShopOutlined />, page: p(() => import('./pages/settings/Sites')) },
      { path: '/settings/licenses', label: 'licenses', icon: <IdcardOutlined />, page: p(() => import('./pages/settings/Licenses')) },
      { path: '/settings/users', label: 'users', icon: <UserOutlined />, page: p(() => import('./pages/settings/Users')) },
      { path: '/settings/api-keys', label: 'apiKeys', icon: <KeyOutlined />, page: p(() => import('./pages/settings/ApiKeys')) },
      { path: '/settings/branding', label: 'branding', icon: <BgColorsOutlined />, page: p(() => import('./pages/settings/Branding')) },
      { path: '/settings/usage', label: 'usage', icon: <LineChartOutlined />, page: p(() => import('./pages/settings/Usage')) },
      { path: '/settings/export', label: 'dataExport', icon: <ContainerOutlined />, page: p(() => import('./pages/settings/DataExport')) },
    ],
  },
];

