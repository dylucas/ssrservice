// Element Plus components that only some pages use.
//
// These used to sit in ./element.ts next to the application shell, which put
// them in the entry chunk: the login page and the dashboard both downloaded the
// table, the date picker and the pager for pages they never render. Exporting
// them from here instead lets Vite put them in one shared chunk that only the
// pages needing them load.
//
// Keep this file in sync with ./element-components.ts (elementParts):
// web/tests/element.test.ts checks that every component a template uses is
// either globally registered in ./element.ts or exported here, and that each one
// has its style sheet.
export {
  ElAlert,
  ElDatePicker,
  ElDescriptions,
  ElDescriptionsItem,
  ElInputNumber,
  ElOption,
  ElPagination,
  ElProgress,
  ElRadioButton,
  ElRadioGroup,
  ElSelect,
  ElSwitch,
  ElTable,
  ElTableColumn,
} from 'element-plus'

// Component styles, imported per component for their side effects. Each entry
// pulls in its own dependencies (base, scrollbar, tooltip, ...).
import 'element-plus/es/components/alert/style/css'
import 'element-plus/es/components/date-picker/style/css'
import 'element-plus/es/components/descriptions/style/css'
import 'element-plus/es/components/descriptions-item/style/css'
import 'element-plus/es/components/input-number/style/css'
import 'element-plus/es/components/option/style/css'
import 'element-plus/es/components/pagination/style/css'
import 'element-plus/es/components/progress/style/css'
import 'element-plus/es/components/radio-button/style/css'
import 'element-plus/es/components/radio-group/style/css'
import 'element-plus/es/components/select/style/css'
import 'element-plus/es/components/switch/style/css'
import 'element-plus/es/components/table/style/css'
import 'element-plus/es/components/table-column/style/css'
