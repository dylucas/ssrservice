import { describe, expect, it } from 'vitest'
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
  type PlotArea,
} from '../src/utils/chart'

// The chart is hand-rolled SVG, so these pure functions are the whole drawing
// logic. They are tested numerically: the previous canvas library was trusted to
// get the geometry right, and that trust has to be replaced by assertions.

const BOX = { width: 600, height: 300, top: 12, right: 12, bottom: 24, left: 56 }
const AREA: PlotArea = { x: 56, y: 12, width: 532, height: 264 }

describe('plotArea', () => {
  it('carves the axis gutters out of the box', () => {
    expect(plotArea(BOX)).toEqual(AREA)
  })

  it('never produces a negative or zero plot', () => {
    const tiny = plotArea({ width: 10, height: 10, top: 12, right: 12, bottom: 24, left: 56 })
    expect(tiny.width).toBeGreaterThan(0)
    expect(tiny.height).toBeGreaterThan(0)
  })
})

describe('niceMax', () => {
  it('covers the peak with a round number', () => {
    // The axis must never sit below the data, or the top of the line is clipped.
    for (const peak of [1, 9, 137, 1024, 4095, 1e4, 123456, 1.5e9, 3.3e9]) {
      expect(niceMax(peak), `peak ${peak}`).toBeGreaterThanOrEqual(peak)
    }
  })

  it('does not overshoot badly', () => {
    // The first cut used only 1/2/5/10 steps and pushed a 1024 peak to 2000,
    // squashing the line into the lower half of the plot.
    for (const peak of [1, 9, 137, 1024, 4095, 1e4, 123456, 1.5e9, 3.3e9]) {
      expect(niceMax(peak) / peak, `peak ${peak}`).toBeLessThan(3)
    }
  })

  it('maps typical peaks to readable axis maxima', () => {
    expect(niceMax(1024)).toBe(1200)
    expect(niceMax(137)).toBe(160)
    expect(niceMax(1e4)).toBe(1e4)
    expect(niceMax(1.5e9)).toBe(1.6e9)
  })

  it('falls back to 1 for degenerate input', () => {
    // A 0..0 axis would divide by zero in every y coordinate.
    expect(niceMax(0)).toBe(1)
    expect(niceMax(-5)).toBe(1)
    expect(niceMax(Number.NaN)).toBe(1)
    expect(niceMax(Number.POSITIVE_INFINITY)).toBe(1)
  })
})

describe('axisTicks', () => {
  it('spans zero to the maximum', () => {
    expect(axisTicks(1200)).toEqual([0, 300, 600, 900, 1200])
    expect(axisTicks(1)).toEqual([0, 0.25, 0.5, 0.75, 1])
  })

  it('honours a custom tick count', () => {
    expect(axisTicks(100, 2)).toEqual([0, 50, 100])
  })
})

describe('xAt and yAt', () => {
  it('places the first and last point on the plot edges', () => {
    expect(xAt(0, 3, AREA)).toBe(AREA.x)
    expect(xAt(2, 3, AREA)).toBe(AREA.x + AREA.width)
  })

  it('centres a single point', () => {
    // Otherwise a one-day series is a dot in the corner.
    expect(xAt(0, 1, AREA)).toBe(AREA.x + AREA.width / 2)
  })

  it('inverts y so larger values sit higher', () => {
    expect(yAt(0, 100, AREA)).toBe(AREA.y + AREA.height)
    expect(yAt(100, 100, AREA)).toBe(AREA.y)
    expect(yAt(50, 100, AREA)).toBeCloseTo(AREA.y + AREA.height / 2)
  })

  it('clamps values outside the axis instead of escaping the plot', () => {
    expect(yAt(-10, 100, AREA)).toBe(AREA.y + AREA.height)
    expect(yAt(500, 100, AREA)).toBe(AREA.y)
    // A zero maximum happens before the first sample arrives.
    expect(yAt(5, 0, AREA)).toBe(AREA.y)
    expect(Number.isFinite(yAt(Number.NaN, 100, AREA))).toBe(true)
  })
})

describe('pointsOf', () => {
  it('emits one point per value so indices stay aligned across series', () => {
    const points = pointsOf([0, 50, 100], 100, AREA)
    expect(points).toEqual([
      { x: AREA.x, y: AREA.y + AREA.height },
      { x: AREA.x + AREA.width / 2, y: AREA.y + AREA.height / 2 },
      { x: AREA.x + AREA.width, y: AREA.y },
    ])
  })

  it('handles an empty series', () => {
    expect(pointsOf([], 100, AREA)).toEqual([])
  })

  it('pins an all-zero series to the baseline', () => {
    const flat = pointsOf([0, 0, 0], niceMax(0), AREA)
    for (const point of flat) expect(point.y).toBe(AREA.y + AREA.height)
  })
})

describe('linePath and areaPath', () => {
  it('renders a polyline', () => {
    expect(linePath(pointsOf([0, 50, 100], 100, AREA))).toBe('M56 276 L322 144 L588 12')
  })

  it('renders an empty string for no points', () => {
    expect(linePath([])).toBe('')
    expect(areaPath([], 0)).toBe('')
  })

  it('closes the area down to the baseline without repeating the first point', () => {
    const path = areaPath(pointsOf([0, 50, 100], 100, AREA), AREA.y + AREA.height)
    expect(path).toBe('M56 276 L56 276 L322 144 L588 12 L588 276 Z')
    // One M, then only L commands: a duplicated M would make the fill path
    // ambiguous on some renderers.
    expect(path.match(/M/g)).toHaveLength(1)
  })

  it('rounds coordinates to keep the markup small', () => {
    const path = linePath([{ x: 1.234567, y: 2.987654 }])
    expect(path).toBe('M1.23 2.99')
  })
})

describe('nearestIndex', () => {
  it('snaps to the closest point', () => {
    const count = 30
    expect(nearestIndex(AREA.x, count, AREA)).toBe(0)
    expect(nearestIndex(AREA.x + AREA.width / 2, count, AREA)).toBe(15)
    expect(nearestIndex(AREA.x + AREA.width, count, AREA)).toBe(29)
  })

  it('clamps a pointer outside the plot', () => {
    // Hovering past the last point should still read the last value, not -1.
    expect(nearestIndex(-500, 30, AREA)).toBe(0)
    expect(nearestIndex(9999, 30, AREA)).toBe(29)
  })

  it('returns -1 only for an empty series', () => {
    expect(nearestIndex(100, 0, AREA)).toBe(-1)
    expect(nearestIndex(100, 1, AREA)).toBe(0)
  })
})

describe('labelIndices', () => {
  it('labels every day when they fit', () => {
    expect(labelIndices(7)).toEqual([0, 1, 2, 3, 4, 5, 6])
    expect(labelIndices(1)).toEqual([0])
  })

  it('samples evenly and always includes the last day', () => {
    // A series that ends on an unlabelled point reads as if it stopped early.
    for (const count of [8, 30, 90, 365]) {
      const indices = labelIndices(count)
      expect(indices.length, `count ${count}`).toBeLessThanOrEqual(8)
      expect(indices[0]).toBe(0)
      expect(indices[indices.length - 1], `count ${count}`).toBe(count - 1)
      for (let i = 1; i < indices.length; i++) {
        expect(indices[i]).toBeGreaterThan(indices[i - 1])
      }
    }
  })

  it('handles an empty series', () => {
    expect(labelIndices(0)).toEqual([])
  })
})

describe('peakOf', () => {
  it('finds the largest value across every series', () => {
    expect(peakOf([
      { name: 'a', variant: 'up', values: [1, 9, 3] },
      { name: 'b', variant: 'down', values: [2, 4, 40] },
    ])).toBe(40)
  })

  it('returns 0 for empty or invalid input', () => {
    expect(peakOf([])).toBe(0)
    expect(peakOf([{ name: 'a', variant: 'up', values: [] }])).toBe(0)
    expect(peakOf([{ name: 'a', variant: 'up', values: [Number.NaN, -5] }])).toBe(0)
  })
})

describe('clampActive', () => {
  // The hover index survives a data change. Switching 30 -> 7 days used to leave
  // active=20 and the marker read points[20].y from a seven-point array, which
  // threw while rendering.
  const cases: Array<{ active: number; length: number; expected: number; why: string }> = [
    { active: 20, length: 7, expected: 6, why: 'shrunk window clamps to the last point' },
    { active: 6, length: 7, expected: 6, why: 'last valid index is kept' },
    { active: 0, length: 7, expected: 0, why: 'first index is kept' },
    { active: -1, length: 7, expected: -1, why: 'no hover stays no hover' },
    { active: -5, length: 7, expected: -1, why: 'negative indices never render' },
    { active: 0, length: 0, expected: -1, why: 'empty series has no point' },
    { active: 3, length: 0, expected: -1, why: 'empty series has no point' },
    { active: 2.7, length: 5, expected: 2, why: 'fractional index truncates' },
    { active: Number.NaN, length: 5, expected: -1, why: 'NaN would index nothing' },
    { active: Number.POSITIVE_INFINITY, length: 5, expected: -1, why: 'infinite index is invalid' },
  ]

  for (const testCase of cases) {
    it(`maps active=${testCase.active} over length=${testCase.length} to ${testCase.expected} (${testCase.why})`, () => {
      expect(clampActive(testCase.active, testCase.length)).toBe(testCase.expected)
    })
  }

  it('always returns an index that can address a point', () => {
    for (let active = -2; active <= 40; active++) {
      for (const length of [1, 2, 7, 30, 90]) {
        const clamped = clampActive(active, length)
        if (active < 0) {
          expect(clamped).toBe(-1)
          continue
        }
        expect(clamped).toBeGreaterThanOrEqual(0)
        expect(clamped).toBeLessThan(length)
      }
    }
  })
})
