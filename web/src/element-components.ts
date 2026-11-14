// Element Plus registration data.
//
// This module is intentionally plain data with no runtime imports so the
// guard test in web/tests can check it without loading the whole library.
//
// Whenever a view starts using a new `el-*` component, add it here (and to the
// matching registry/style imports in element.ts); web/tests/element.test.ts
// fails otherwise.

// elementComponents lists the globally registered Element Plus components.
export const elementComponents = [
  'ElAlert',
  'ElAside',
  'ElButton',
  'ElConfigProvider',
  'ElContainer',
  'ElDatePicker',
  'ElDescriptions',
  'ElDescriptionsItem',
  'ElDialog',
  'ElDropdown',
  'ElDropdownItem',
  'ElDropdownMenu',
  'ElForm',
  'ElFormItem',
  'ElHeader',
  'ElIcon',
  'ElInput',
  'ElInputNumber',
  'ElMain',
  'ElMenu',
  'ElMenuItem',
  'ElOption',
  'ElPagination',
  'ElProgress',
  'ElRadioButton',
  'ElRadioGroup',
  'ElSelect',
  'ElSwitch',
  'ElTable',
  'ElTableColumn',
  'ElTag',
] as const

// elementPlugins lists the plugins whose directives or global services are
// used in templates (for example v-loading needs ElLoading).
export const elementPlugins = ['ElLoading'] as const

// elementServices lists the import-on-demand helpers that are called from
// script code rather than used as template tags. They still need their styles.
export const elementServices = ['ElMessage', 'ElMessageBox'] as const

// elementStyleModules are imported for their side effects. ElMessage,
// ElMessageBox and ElLoading have no direct component tag but their styles are
// required too.
export const elementStyleComponents = [
  ...elementComponents,
  ...elementPlugins,
  ...elementServices,
] as const
