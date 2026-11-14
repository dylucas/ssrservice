<template>
  <div>
    <!-- The legend sits outside the measured box so the chart's height stays the
         drawing height. Legend and tooltip share the swatch classes; they are
         plain HTML outside the <svg>, where the [data-variant] attribute selector
         does not reach, which is why the swatches used to render transparent. -->
    <div v-if="hasTraffic" class="chart-legend">
      <span v-for="entry in series" :key="entry.name" class="chart-legend-item">
        <span :class="['chart-key', keyClass(entry.variant)]" />
        {{ entry.name }}
      </span>
    </div>
    <div ref="rootEl" class="traffic-chart">
      <!-- An empty series and an all-zero series both mean "no traffic in this
           window". Drawing an axis scaled 0..1 B for them would print nonsense
           labels like "256 B" per tick, so the empty state is the honest answer. -->
      <div v-if="!hasTraffic" class="empty-state">该区间还没有流量记录</div>
      <template v-else>
        <svg
          v-if="width > 0"
          :viewBox="`0 0 ${width} ${height}`"
          :width="width"
          :height="height"
          role="img"
          :aria-label="ariaLabel || '上传与下载流量趋势'"
          @pointermove="onPointerMove"
          @pointerleave="onPointerLeave"
        >
          <!-- Grid and y axis labels -->
          <g>
            <line
              v-for="tick in ticks"
              :key="`grid-${tick.value}`"
              :x1="area.x"
              :x2="area.x + area.width"
              :y1="tick.y"
              :y2="tick.y"
              class="chart-grid"
            />
            <text
              v-for="tick in ticks"
              :key="`y-${tick.value}`"
              :x="area.x - 8"
              :y="tick.y"
              class="chart-axis-text"
              text-anchor="end"
              dominant-baseline="middle"
            >
              {{ formatBytes(tick.value) }}
            </text>
          </g>

          <!-- x axis labels -->
          <text
            v-for="index in xLabels"
            :key="`x-${index}`"
            :x="xAt(index, labels.length, area)"
            :y="height - 6"
            class="chart-axis-text"
            text-anchor="middle"
          >
            {{ labels[index] }}
          </text>

          <!-- Every area before every line, so no fill can cover a stroke. -->
          <path
            v-for="entry in geometry"
            :key="`area-${entry.series.variant}`"
            :d="entry.area"
            class="chart-area"
            :data-variant="entry.series.variant"
          />
          <path
            v-for="entry in geometry"
            :key="`line-${entry.series.variant}`"
            :d="entry.line"
            class="chart-line"
            :data-variant="entry.series.variant"
          />

          <!-- Hover marker: a vertical guide plus one dot per series. -->
          <g v-if="active >= 0">
            <line
              :x1="hoverX"
              :x2="hoverX"
              :y1="area.y"
              :y2="area.y + area.height"
              class="chart-marker"
            />
            <circle
              v-for="entry in hoverDots"
              :key="`dot-${entry.variant}`"
              :cx="hoverX"
              :cy="entry.y"
              r="3"
              class="chart-dot"
              :data-variant="entry.variant"
            />
          </g>
        </svg>

        <!-- Fixed in the top-right corner: a tooltip that follows the pointer would
             have to flip sides at the edges and would cover the curve it explains. -->
        <div v-if="active >= 0" class="chart-tooltip">
          <div class="chart-tooltip-title">{{ labels[active] }}</div>
          <div v-for="series in series" :key="series.name" class="chart-tooltip-row">
            <span :class="['chart-key', keyClass(series.variant)]" />
            <span class="chart-tooltip-name">{{ series.name }}</span>
            <strong>{{ formatBytes(series.values[active] ?? 0) }}</strong>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { formatBytes } from '@/utils/format'
import {
  areaPath,
  axisTicks,
  clampActive,
  labelIndices,
  linePath,
  nearestIndex,
  niceMax,
  peakOf,
  plotArea,
  pointsOf,
  xAt,
  yAt,
  type ChartBox,
  type ChartSeries,
} from '@/utils/chart'

const props = defineProps<{
  labels: string[]
  series: ChartSeries[]
  ariaLabel?: string
}>()

/** Axis gutters: room for a byte label on the left and a date on the bottom. */
const GUTTERS: Omit<ChartBox, 'width' | 'height'> = { top: 12, right: 12, bottom: 24, left: 56 }
/** Fallback height until the container has been measured. */
const DEFAULT_HEIGHT = 240

const rootEl = ref<HTMLDivElement>()
const width = ref(0)
const height = ref(DEFAULT_HEIGHT)
const active = ref(-1)
let observer: ResizeObserver | null = null

const hasTraffic = computed(() => props.labels.length > 0 && peakOf(props.series) > 0)

const area = computed(() => plotArea({ ...GUTTERS, width: width.value, height: height.value }))

const axisMax = computed(() => niceMax(peakOf(props.series)))

const ticks = computed(() =>
  axisTicks(axisMax.value).map((value) => ({ value, y: yAt(value, axisMax.value, area.value) })),
)

const geometry = computed(() => {
  const box = area.value
  return props.series.map((entry) => {
    const points = pointsOf(entry.values, axisMax.value, box)
    return {
      series: entry,
      points,
      line: linePath(points),
      area: areaPath(points, box.y + box.height),
    }
  })
})

const xLabels = computed(() => labelIndices(props.labels.length))

// hoverDots is the marker positions for the active index. A series shorter than
// labels (or a hover index from before a data change) yields no dot instead of
// an undefined point: the marker is decoration and must never throw.
const hoverDots = computed(() => {
  const index = active.value
  if (index < 0) return []
  return geometry.value
    .filter((entry) => entry.points[index] !== undefined)
    .map((entry) => ({ variant: entry.series.variant, y: entry.points[index].y }))
})

const hoverX = computed(() => xAt(active.value, props.labels.length, area.value))

/**
 * keyClass maps a series variant to its swatch class.
 *
 * The <svg> paths are coloured by an attribute selector ([data-variant='up']),
 * which cannot reach the HTML legend and tooltip swatches. A static class per
 * variant is what gives those two the same colour as the line.
 */
function keyClass(variant: ChartSeries['variant']): string {
  return variant === 'down' ? 'chart-key-down' : 'chart-key-up'
}

// The hover index is a stale coordinate once the series shrinks (30 -> 7 days,
// a poll returning a shorter window): entry.points[active] would be undefined
// and reading .y from it throws during render. Reset or clamp it whenever the
// data changes, so the marker can never point outside the new points array.
watch(
  () => [props.labels.length, props.series] as const,
  () => {
    active.value = clampActive(active.value, props.labels.length)
  },
)

/** measure reads the rendered size; the SVG uses it as its viewBox. */
function measure() {
  const el = rootEl.value
  if (!el) return
  width.value = Math.max(0, Math.floor(el.clientWidth))
  height.value = Math.max(DEFAULT_HEIGHT, Math.floor(el.clientHeight))
}

function onPointerMove(event: PointerEvent) {
  const el = rootEl.value
  if (!el || !hasTraffic.value) return
  const rect = el.getBoundingClientRect()
  active.value = nearestIndex(event.clientX - rect.left, props.labels.length, area.value)
}

function onPointerLeave() {
  active.value = -1
}

onMounted(() => {
  measure()
  if (typeof ResizeObserver !== 'undefined' && rootEl.value) {
    // ResizeObserver covers the sidebar collapsing and the dialog opening, which
    // a window resize listener would miss.
    observer = new ResizeObserver(measure)
    observer.observe(rootEl.value)
    return
  }
  window.addEventListener('resize', measure)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
  window.removeEventListener('resize', measure)
})
</script>
