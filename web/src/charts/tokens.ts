/**
 * Chart colour tokens.
 *
 * Every value here came out of the palette validator run against this app's own
 * card surface (#0f1430) in dark mode, not out of taste. Re-run it before
 * changing any of them:
 *
 *   node scripts/validate_palette.js "#3987e5,#d95926,#199e70" \
 *     --mode dark --surface "#0f1430" --pairs all
 *
 * Results on record: lightness band, chroma floor, CVD separation (worst
 * all-pairs deltaE 9.4), normal-vision floor (20.9) and contrast all pass.
 */

/** The chart surface. Charts sit on a card, not on the page background. */
export const SURFACE = '#0f1430'

/**
 * Categorical slots, in fixed order. They are assigned in sequence and never
 * cycled: a chart that would need a fourth colour folds its tail into "other"
 * or becomes small multiples instead.
 *
 * Three is the cap here on purpose. That is the number that validates under
 * all-pairs, which is the test that applies when any two marks can end up side
 * by side.
 */
export const SERIES = ['#3987e5', '#d95926', '#199e70'] as const

/**
 * The sequential ramp for magnitude: one hue, dark to light.
 *
 * Dark-anchored because the surface is dark. Near-zero recedes into the
 * background, which is exactly what an empty heatmap cell should do, and the
 * value climbs towards light. Validated for lightness monotonicity, which is
 * the check that applies to a continuous ramp; the ordinal step-gap check does
 * not, and fails by design on any ramp meant to be read as continuous.
 */
export const SEQUENTIAL = [
  '#0d366b',
  '#104281',
  '#184f95',
  '#1c5cab',
  '#256abf',
  '#2a78d6',
  '#3987e5',
  '#5598e7',
  '#6da7ec',
  '#86b6ef',
  '#9ec5f4',
  '#b7d3f6',
  '#cde2fb',
] as const

/**
 * Status colours, reserved. They never stand in for a series, and they never
 * carry meaning on their own: everything using them ships a label beside it.
 */
export const STATUS = {
  good: '#0ca30c',
  warning: '#fab219',
  serious: '#ec835a',
  critical: '#d03b3b',
} as const

/** Chart chrome. Gridlines are one step off the surface and stay recessive. */
export const CHROME = {
  grid: '#1c2450',
  axis: '#243068',
  textPrimary: '#e8eefc',
  textSecondary: '#a9b6e8',
  textMuted: '#6b7590',
} as const

/** Samples the sequential ramp at a normalised position. */
export function rampColor(t: number): string {
  if (!Number.isFinite(t)) return SEQUENTIAL[0]

  const clamped = Math.min(1, Math.max(0, t))
  const index = Math.round(clamped * (SEQUENTIAL.length - 1))
  return SEQUENTIAL[index]
}

/**
 * Samples the ramp as red, green and blue components, for a canvas image
 * buffer where a per-pixel string lookup would be far too slow.
 */
export function rampRGB(t: number): [number, number, number] {
  const hex = rampColor(t)
  return [
    parseInt(hex.slice(1, 3), 16),
    parseInt(hex.slice(3, 5), 16),
    parseInt(hex.slice(5, 7), 16),
  ]
}

/**
 * Compresses a heatmap value into the ramp.
 *
 * A square root rather than a straight ratio, because a heatmap's values are
 * almost always dominated by a few extreme cells. Scaled linearly, everything
 * but the hotspot collapses into the lowest step and the picture says only
 * "one cell is busy", which the reader already knew.
 */
export function heatScale(value: number, scale: number): number {
  if (scale <= 0 || value <= 0) return 0
  return Math.min(1, Math.sqrt(value / scale))
}

/** Formats a number for an axis tick or a value label. */
export function formatNumber(value: number, decimals = 0): string {
  if (!Number.isFinite(value)) return '—'

  const abs = Math.abs(value)
  if (abs >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (abs >= 10_000) return `${Math.round(value / 1000)}k`

  return value.toLocaleString(undefined, {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  })
}

/** Rounds an axis maximum up to a clean number, so ticks read 0 / 20 / 40. */
export function niceMax(value: number): number {
  if (value <= 0) return 1

  const exponent = Math.floor(Math.log10(value))
  const base = Math.pow(10, exponent)
  const f = value / base

  if (f <= 1) return base
  if (f <= 2) return 2 * base
  if (f <= 2.5) return 2.5 * base
  if (f <= 5) return 5 * base
  return 10 * base
}
