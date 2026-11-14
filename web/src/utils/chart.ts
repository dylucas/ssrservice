// Geometry for the panel's inline SVG line chart.
//
// The dashboard draws two series of at most ~90 points as a line plus an area
// fill. Pulling in a canvas charting library for that cost 167 kB of gzip on the
// landing page, which was 41% of it. Everything here is a pure function with no
// imports, so the shape of the drawing can be unit tested without a DOM and the
// module cannot drag a dependency back in.

/** One drawn series: a label, a colour variant and its values. */
export interface ChartSeries {
  name: string
  variant: 'up' | 'down'
  values: number[]
}

/** A point in SVG user units. */
export interface ChartPoint {
  x: number
  y: number
}

/** Padded drawing area inside the SVG. */
export interface ChartBox {
  width: number
  height: number
  top: number
  right: number
  bottom: number
  left: number
}

/** Inner plot dimensions once the axis gutters are removed. */
export interface PlotArea {
  x: number
  y: number
  width: number
  height: number
}

/** plotArea carves the axis gutters out of the full box. */
export function plotArea(box: ChartBox): PlotArea {
  return {
    x: box.left,
    y: box.top,
    width: Math.max(1, box.width - box.left - box.right),
    height: Math.max(1, box.height - box.top - box.bottom),
  }
}

/**
 * Axis steps that read as round numbers at any magnitude.
 *
 * The usual 1/2/5/10 set is too coarse for a chart: it pushed a 1024 peak up to
 * a 2000 axis and squashed the line into the lower half. Adding 1.5/2.5/3/4/6/8
 * keeps the overshoot small (measured 1.07x-1.47x across 1 B .. 3.3 GB).
 */
const AXIS_STEPS = [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]

/**
 * niceMax rounds max up to the nearest axis maximum built from round steps, so
 * the top label is a readable number instead of the exact peak.
 */
export function niceMax(max: number, ticks = 4): number {
  if (!Number.isFinite(max) || max <= 0) return 1
  const rough = max / ticks
  const magnitude = Math.pow(10, Math.floor(Math.log10(rough)))
  for (const scale of [magnitude / 10, magnitude, magnitude * 10]) {
    for (const step of AXIS_STEPS) {
      const candidate = step * scale * ticks
      if (candidate >= max) return candidate
    }
  }
  return max
}

/** axisTicks returns evenly spaced values from 0 to max, ascending. */
export function axisTicks(max: number, count = 4): number[] {
  const ticks: number[] = []
  for (let index = 0; index <= count; index++) {
    ticks.push((max / count) * index)
  }
  return ticks
}

/**
 * xAt maps a series index to its horizontal position. A single point sits in the
 * middle rather than at the left edge, so a one-day series is not a dot in the
 * corner.
 */
export function xAt(index: number, count: number, area: PlotArea): number {
  if (count <= 1) return area.x + area.width / 2
  return area.x + (area.width * index) / (count - 1)
}

/** yAt maps a value to its vertical position, clamped to the plot area. */
export function yAt(value: number, max: number, area: PlotArea): number {
  const safeMax = max > 0 ? max : 1
  const ratio = Math.min(1, Math.max(0, (Number(value) || 0) / safeMax))
  return area.y + area.height * (1 - ratio)
}

/** pointsOf places one series, one point per value so index alignment holds. */
export function pointsOf(values: number[], max: number, area: PlotArea): ChartPoint[] {
  return values.map((value, index) => ({
    x: xAt(index, values.length, area),
    y: yAt(value, max, area),
  }))
}

/** linePath renders the series as an unbroken polyline. */
export function linePath(points: ChartPoint[]): string {
  if (points.length === 0) return ''
  return points
    .map((point, index) => `${index === 0 ? 'M' : 'L'}${round(point.x)} ${round(point.y)}`)
    .join(' ')
}

/**
 * areaPath closes the line down to the baseline so it can be filled.
 *
 * The path starts at the first x on the baseline, so every point is an L and the
 * first point is not repeated.
 */
export function areaPath(points: ChartPoint[], baseline: number): string {
  if (points.length === 0) return ''
  const first = points[0]
  const last = points[points.length - 1]
  const top = points.map((point) => `L${round(point.x)} ${round(point.y)}`).join(' ')
  return `M${round(first.x)} ${round(baseline)} ${top} L${round(last.x)} ${round(baseline)} Z`
}

/**
 * nearestIndex returns the index of the point closest to a horizontal position,
 * which is what a pointer hover snaps to. Out-of-range positions clamp instead of
 * returning -1, so hovering past the last point still shows the last value.
 */
export function nearestIndex(x: number, count: number, area: PlotArea): number {
  if (count <= 0) return -1
  if (count === 1) return 0
  const ratio = (x - area.x) / area.width
  const scaled = Math.round(ratio * (count - 1))
  return Math.min(count - 1, Math.max(0, scaled))
}

/**
 * labelIndices picks which x labels to render so they never overlap: all of them
 * when they fit, otherwise as many evenly spaced ones as fit, always including
 * the last day so the series never ends on an unlabelled point.
 */
export function labelIndices(count: number, maxLabels = 7): number[] {
  if (count <= 0) return []
  if (count <= maxLabels) {
    return Array.from({ length: count }, (_, index) => index)
  }
  const step = Math.ceil((count - 1) / (maxLabels - 1))
  const indices: number[] = []
  for (let index = 0; index < count; index += step) indices.push(index)
  if (indices[indices.length - 1] !== count - 1) indices.push(count - 1)
  return indices
}

/** peakOf returns the largest value across every series. */
export function peakOf(series: ChartSeries[]): number {
  let peak = 0
  for (const entry of series) {
    for (const value of entry.values) {
      const number = Number(value)
      if (Number.isFinite(number) && number > peak) peak = number
    }
  }
  return peak
}

/** round trims float noise from SVG attributes so the markup stays small. */
function round(value: number): number {
  return Math.round(value * 100) / 100
}
