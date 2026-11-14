// Element Plus registration data.
//
// This module is intentionally plain data with no runtime imports so the
// guard test in web/tests can check it without loading the whole library.
//
// The panel registers two tiers of components:
//
//   - elementComponents: the application shell, registered globally in
//     ./element.ts because nearly every page renders them (layout, menu, button,
//     dialog, form controls). They live in the entry chunk.
//   - elementParts: page-specific components, exported from ./element-parts.ts
//     and imported by the views that use them. They live in a shared chunk that
//     only those pages load, so the login page and the dashboard do not download
//     the table and the date picker.
//
// Whenever a view starts using a new `el-*` component, add it to one of these
// lists and to the matching import/style in element.ts or element-parts.ts;
// web/tests/element.test.ts fails otherwise.

// elementComponents lists the globally registered Element Plus components.
export const elementComponents = [
  'ElAside',
  'ElButton',
  'ElConfigProvider',
  'ElContainer',
  'ElDialog',
  'ElDropdown',
  'ElDropdownItem',
  'ElDropdownMenu',
  'ElForm',
  'ElFormItem',
  'ElHeader',
  'ElIcon',
  'ElInput',
  'ElMain',
  'ElMenu',
  'ElMenuItem',
  'ElTag',
] as const

// elementParts lists the components exported from ./element-parts.ts for the
// views that render them.
export const elementParts = [
  'ElAlert',
  'ElDatePicker',
  'ElInputNumber',
  'ElOption',
  'ElPagination',
  'ElProgress',
  'ElRadioButton',
  'ElRadioGroup',
  'ElSelect',
  'ElSwitch',
  'ElTable',
  'ElTableColumn',
] as const

// elementPlugins lists the plugins whose directives or global services are
// used in templates (for example v-loading needs ElLoading).
export const elementPlugins = ['ElLoading'] as const

// elementServices lists the import-on-demand helpers that are called from
// script code rather than used as template tags. They still need their styles.
export const elementServices = ['ElMessage', 'ElMessageBox'] as const

// elementStyleComponents are the component style sheets the entry chunk needs:
// the global registry, the plugins and the services.
export const elementStyleComponents = [
  ...elementComponents,
  ...elementPlugins,
  ...elementServices,
] as const

// partStyleComponents are the component style sheets ./element-parts.ts imports.
export const partStyleComponents = [...elementParts] as const
