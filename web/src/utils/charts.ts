// Shared ECharts registration.
//
// Every chart in the panel imports this module instead of registering its own
// components, so the library is included once and only the parts actually used
// (line chart, grid, legend, tooltip, canvas renderer) are pulled in.
//
// Views that render charts on demand (the client usage dialog) import this
// module dynamically so opening the clients page does not load ECharts at all.
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { ECharts } from 'echarts/core'

echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

export { echarts }
export type { ECharts }
