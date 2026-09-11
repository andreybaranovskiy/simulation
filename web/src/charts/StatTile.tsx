import type { RunKPI } from '@/api/types'
import { formatDuration } from '@/state/timeline'

/**
 * A headline number.
 *
 * A stat tile rather than a one-bar chart, because a single current value has
 * no magnitudes to compare. When a comparison exists the delta rides alongside,
 * coloured only by whether the change is an improvement, which the KPI itself
 * declares rather than the tile guessing.
 */
export function StatTile({ kpi, compareTo }: { kpi: RunKPI; compareTo?: number }) {
  const delta = compareTo === undefined ? null : kpi.value - compareTo
  const relative = compareTo ? (delta! / compareTo) * 100 : null

  // "Better" is a property of the measure, not of the sign. A shorter wait and
  // a higher throughput are both improvements, and only the KPI knows which.
  const improved =
    delta === null || delta === 0 || !kpi.better
      ? null
      : kpi.better === 'lower'
        ? delta < 0
        : delta > 0

  return (
    <div className="stat-tile">
      <div className="stat-label">{kpi.label}</div>

      <div className="stat-value">
        {formatKPI(kpi)}
        {kpi.unit && kpi.unit !== 's' && <span className="stat-unit">{kpi.unit}</span>}
      </div>

      {delta !== null && relative !== null && Number.isFinite(relative) && (
        <div className={`stat-delta ${improved === null ? '' : improved ? 'better' : 'worse'}`}>
          {/* The word carries the meaning; the colour only reinforces it, so a
              reader who cannot see the difference still gets the answer. */}
          {delta > 0 ? '▲' : '▼'} {Math.abs(relative).toFixed(1)}%
          {improved !== null && (
            <span className="stat-delta-word">{improved ? 'better' : 'worse'}</span>
          )}
        </div>
      )}

      {kpi.description && <div className="stat-note">{kpi.description}</div>}
    </div>
  )
}

/** Formats a KPI value in the unit it declares. */
export function formatKPI(kpi: RunKPI): string {
  if (kpi.unit === 's') return formatDuration(kpi.value)

  if (kpi.unit === '%') {
    return `${kpi.value.toFixed(kpi.decimals)}%`
  }

  return kpi.value.toLocaleString(undefined, {
    minimumFractionDigits: kpi.decimals,
    maximumFractionDigits: kpi.decimals,
  })
}

/**
 * A ratio against a limit.
 *
 * A meter rather than a two-slice pie, and drawn on the same ramp as the
 * heatmaps so that "busy" looks the same everywhere in the product.
 */
export function Meter({
  label,
  fraction,
  caption,
}: {
  label: string
  fraction: number
  caption?: string
}) {
  const percent = Math.min(100, Math.max(0, fraction * 100))

  return (
    <div className="meter">
      <div className="meter-head">
        <span className="meter-label">{label}</span>
        <span className="meter-value">{percent.toFixed(1)}%</span>
      </div>

      <div
        className="meter-track"
        role="meter"
        aria-valuenow={Math.round(percent)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={label}
      >
        <span style={{ width: `${percent}%` }} />
      </div>

      {caption && <div className="meter-caption">{caption}</div>}
    </div>
  )
}
