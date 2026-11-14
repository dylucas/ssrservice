// Element Plus is registered component by component instead of through the
// full element-plus bundle: `app.use(ElementPlus)` plus the complete
// dist/index.css pulled in every component and every style, which kept the
// entry chunk above 1 MB of JavaScript and 365 KB of CSS.
//
// Keep this file in sync with element-components.ts: web/tests/element.test.ts
// checks that every registered component also imports its style sheet and
// that no template uses a component that is missing here.
import type { App, Component } from 'vue'

import {
  ElAlert,
  ElAside,
  ElButton,
  ElConfigProvider,
  ElContainer,
  ElDatePicker,
  ElDescriptions,
  ElDescriptionsItem,
  ElDialog,
  ElDropdown,
  ElDropdownItem,
  ElDropdownMenu,
  ElForm,
  ElFormItem,
  ElHeader,
  ElIcon,
  ElInput,
  ElInputNumber,
  ElMain,
  ElMenu,
  ElMenuItem,
  ElOption,
  ElPagination,
  ElProgress,
  ElSelect,
  ElSwitch,
  ElTable,
  ElTableColumn,
  ElTag,
} from 'element-plus'
import { ElLoading } from 'element-plus'

// Component styles, imported per component for their side effects. Each
// style entry pulls in the styles of the components it depends on (base,
// tooltip, scrollbar, checkbox, ...), so listing the registered components is
// enough. Message, MessageBox and Loading have no component tag but still need
// their styles.
import 'element-plus/es/components/alert/style/css'
import 'element-plus/es/components/aside/style/css'
import 'element-plus/es/components/button/style/css'
import 'element-plus/es/components/config-provider/style/css'
import 'element-plus/es/components/container/style/css'
import 'element-plus/es/components/date-picker/style/css'
import 'element-plus/es/components/descriptions/style/css'
import 'element-plus/es/components/descriptions-item/style/css'
import 'element-plus/es/components/dialog/style/css'
import 'element-plus/es/components/dropdown/style/css'
import 'element-plus/es/components/dropdown-item/style/css'
import 'element-plus/es/components/dropdown-menu/style/css'
import 'element-plus/es/components/form/style/css'
import 'element-plus/es/components/form-item/style/css'
import 'element-plus/es/components/header/style/css'
import 'element-plus/es/components/icon/style/css'
import 'element-plus/es/components/input/style/css'
import 'element-plus/es/components/input-number/style/css'
import 'element-plus/es/components/main/style/css'
import 'element-plus/es/components/menu/style/css'
import 'element-plus/es/components/menu-item/style/css'
import 'element-plus/es/components/option/style/css'
import 'element-plus/es/components/pagination/style/css'
import 'element-plus/es/components/progress/style/css'
import 'element-plus/es/components/select/style/css'
import 'element-plus/es/components/switch/style/css'
import 'element-plus/es/components/table/style/css'
import 'element-plus/es/components/table-column/style/css'
import 'element-plus/es/components/tag/style/css'
import 'element-plus/es/components/loading/style/css'
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'

// registry maps the tag name used in templates to the component definition.
const registry: Record<string, Component> = {
  ElAlert,
  ElAside,
  ElButton,
  ElConfigProvider,
  ElContainer,
  ElDatePicker,
  ElDescriptions,
  ElDescriptionsItem,
  ElDialog,
  ElDropdown,
  ElDropdownItem,
  ElDropdownMenu,
  ElForm,
  ElFormItem,
  ElHeader,
  ElIcon,
  ElInput,
  ElInputNumber,
  ElMain,
  ElMenu,
  ElMenuItem,
  ElOption,
  ElPagination,
  ElProgress,
  ElSelect,
  ElSwitch,
  ElTable,
  ElTableColumn,
  ElTag,
}

// installElement registers the Element Plus components and directives the panel
// uses. Script-only helpers (ElMessage, ElMessageBox) are imported by the views
// that call them and do not need global registration.
export function installElement(app: App): void {
  for (const [name, component] of Object.entries(registry)) {
    app.component(name, component)
  }
  // Registers the v-loading directive and the $loading service.
  app.use(ElLoading)
}
