import { useState } from 'react'

import { CHROME, SERIES, niceMax } from './tokens'

export interface DistributionRow {
  id: string
  label: string
  values: number[]
  mean: number
  ciLow: number
  ciHigh: number
  hasInterval: boolean
}

/**
 * Every replication of a measure, one row per scenario.
 *
 * This is the chart that makes a verdict checkable. A delta table says a
 * difference is or is not distinguishable; this shows the spread that claim
 * rests on, so a reader can see two clouds that plainly overlap rather than
 * taking the word for it.
 *
 * Identity is carried by row position, not by colour. That keeps the form
 * honest past three scenarios, where colour on an all-pairs chart stops being
 * reliably distinguishable, and it means the hue can do a different job: the
 * baseline is the accent and the rest are context.
 */
export function DistributionChart({
  rows,
  baselineId,
  unit,
  format,
}: {
  rows: DistributionRow[]
  baselineId: string
  unit?: string
  format: (value: number) => string
}) {
  const [hover, setHover] = useState<{ row: string; value: number; x: number; y: number } | null>(null)

  const all = rows.flatMap((r) => r.values).filter((v) => Number.isFinite(v))
  if (all.length === 0) return null

  const min = Math.min(...all, ...rows.map((r) => r.ciLow))
  const max = Math.max(...all, ...rows.map((r) => r.ciHigh))

  // The axis starts at zero when the values sit near it, so a reader is not
  // shown a magnified sliver of the range and left to assume it is the whole
  // story. When the values instead cluster far above zero — a truck count near
  // 313, say — it frames them tightly with a margin. The distinction matters:
  // niceMax is for a zero-based axis, and applying it to a high, tight cluster
  // would round the top out to a number far past the data and strand every
  // point against the left edge.
  const span = max - min || Math.max(Math.abs(max), 1)
  const zeroBased = min > 0 && min < span
  const lower = zeroBased ? 0 : min - span * 0.12
  const upper = zeroBased ? niceMax(max + span * 0.12) : max + span * 0.12

  const width = 720
  const rowHeight = 34
  const padding = { top: 8, right: 16, bottom: 22, left: 150 }
  const height = padding.top + rows.length * rowHeight + padding.bottom
  const plotWidth = width - padding.left - padding.right

  const x = (value: number) =>
    padding.left + ((value - lower) / Math.max(upper - lower, 1e-9)) * plotWidth

  return (
    <div className="chart distribution">
      <svg viewBox={`0 0 ${width} ${height}`} className="chart-svg" role="img"
        aria-label={`Every replication of each scenario${unit ? `, in ${unit}` : ''}`}>
        {[lower, (lower + upper) / 2, upper].map((tick, i) => (
          <g key={i}>
            <line
              x1={x(tick)} x2={x(tick)}
              y1={padding.top} y2={padding.top + rows.length * rowHeight}
              stroke={CHROME.grid} strokeWidth={1}
            />
            <text x={x(tick)} y={height - 6} textAnchor="middle" className="chart-tick">
              {format(tick)}
            </text>
          </g>
        ))}

        {rows.map((row, index) => {
          const y = padding.top + index * rowHeight + rowHeight / 2
          const isBaseline = row.id === baselineId

          // The baseline takes the accent; the rest recede. That is emphasis,
          // not identity, and it survives any number of scenarios.
          const color = isBaseline ? SERIES[0] : SERIES[1]

          return (
            <g key={row.id}>
              <text x={padding.left - 10} y={y + 4} textAnchor="end" className="dist-label">
                {row.label}
              </text>

              {/* The confidence interval, drawn behind the points it came
                  from so the points stay the loudest thing. */}
              {row.hasInterval && (
                <rect
                  x={x(row.ciLow)}
                  y={y - 7}
                  width={Math.max(x(row.ciHigh) - x(row.ciLow), 1)}
                  height={14}
                  fill={color}
                  opacity={0.16}
                  rx={2}
                />
              )}

              {row.values.map((value, i) => (
                <circle
                  key={i}
                  cx={x(value)}
                  // A slight vertical scatter so identical values do not hide
                  // behind one another and imply fewer replications than ran.
                  cy={y + ((i % 3) - 1) * 3.5}
                  r={3}
                  fill={color}
                  opacity={0.65}
                  onPointerEnter={() => setHover({ row: row.label, value, x: x(value), y })}
                  onPointerLeave={() => setHover(null)}
                />
              ))}

              {/* The mean, ringed in the surface colour so it reads on top of
                  the points it summarises. */}
              <line
                x1={x(row.mean)} x2={x(row.mean)}
                y1={y - 9} y2={y + 9}
                stroke="#0f1430" strokeWidth={4}
              />
              <line
                x1={x(row.mean)} x2={x(row.mean)}
                y1={y - 9} y2={y + 9}
                stroke={color} strokeWidth={2}
              />
            </g>
          )
        })}
      </svg>

      {hover && (
        <div
          className="chart-tooltip"
          style={{ left: `${(hover.x / width) * 100}%`, top: hover.y - 6 }}
          role="status"
        >
          <div className="tooltip-row">
            <strong>{format(hover.value)}</strong>
            <span className="tooltip-label">one replication</span>
          </div>
          <div className="tooltip-time">{hover.row}</div>
        </div>
      )}

      <div className="chart-legend">
        <span className="legend-item">
          <span className="legend-dot" /> one replication
        </span>
        <span className="legend-item">
          <span className="legend-mean" /> mean
        </span>
        <span className="legend-item">
          <span className="legend-band" /> 95% confidence in the mean
        </span>
      </div>
    </div>
  )
}
