/**
 * Thin wrapper around @ant-design/pro-components (v3 prerelease at the time of writing).
 * Apps import ProComponents ONLY from here, so the dependency can be swapped or pinned
 * in a single place.
 */
export {
  ProLayout,
  PageContainer,
  ProTable,
  ProForm,
  ModalForm,
  DrawerForm,
  StepsForm,
  ProFormText,
  ProFormTextArea,
  ProFormSelect,
  ProFormDigit,
  ProFormSwitch,
  ProFormDatePicker,
  ProFormDateTimePicker,
  ProFormDateTimeRangePicker,
  ProFormDateRangePicker,
  ProFormList,
  ProFormGroup,
  ProFormDependency,
  ProFormRadio,
  ProFormCheckbox,
  ProFormColorPicker,
  ProFormUploadButton,
  ProCard,
  ProDescriptions,
  StatisticCard,
  ProConfigProvider,
} from '@ant-design/pro-components';
export type {
  ProColumns,
  ProTableProps,
  ActionType,
  ProFormInstance,
  ProLayoutProps,
  MenuDataItem,
  ProDescriptionsItemProps,
} from '@ant-design/pro-components';
