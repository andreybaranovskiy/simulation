import { useMemo, useRef, useState } from 'react'

import { CHROME, SERIES, formatNumber, niceMax } from './tokens'
import { formatSimTime } from '@/state/timeline'

export interface Series {
  key: string
  label: string
  values: number[]
  /** Which categorical slot this series takes. Assigned in order by the caller
   *  and never cycled, so a series keeps its colour when others are filtered. */
  slot?: number
}

interface Props {
  series: Series[]
  startTime: number
  bucketSeconds: number
  height?: number
  /** Units for the y axis, shown once beside the top tick. */
  unit?: string
  /** Drawn as a wash under a single series. Two or more series stay as lines,
   *  because overlapping washes read as a third colour that means nothing. */
  area?: boolean
  yMax?: number
  /** Marks the warm-up, which is excluded from the statistics. Without it a
   *  reader wonders why the first stretch looks unlike the rest. */
  warmUpUntil?: number
}

/**
 * A time series.
 *
 * The crosshair finds the X and reads every series at once, so the pointer
 * never has to land on a 2px line. Every value it shows is also in the table
 * view, so the tooltip enhances and never gates.
 */
export function LineChart({
  series,
  startTime,
  bucketSeconds,
  height = 180,
  unit,
  area = false,
  yMax,
  warmUpUntil,
}: Props) {
  const svgRef = useRef<SVGSVGElement>(null)
  const [hoverIndex, setHoverIndex] = useState<number | null>(null)
  const [showTable, setShowTable] = useState(false)

  const padding = { top: 12, right: 14, bottom: 24, left: 46 }
  const width = 720
  const plotWidth = width - padding.left - padding.right
  const plotHeight = height - padding.top - padding.bottom

  const buckets = Math.max(...series.map((s) => s.values.length), 1)

  const max = useMemo(() => {
    if (yMax !== undefined) return yMax
    let found = 0
    for (const s of series) {
      for (const v of s.values) if (Number.isFinite(v) && v > found) found = v
    }
    return niceMax(found || 1)
  }, [series, yMax])

  const x = (index: number) => padding.left + (index / Math.max(buckets - 1, 1)) * plotWidth
  const y = (value: number) => padding.top + plotHeight - (value / max) * plotHeight

  const ticks = [0, max / 2, max]

  const onMove = (event: React.PointerEvent<SVGSVGElement>) => {
    const svg = svgRef.current
    if (!svg) return

    const rect = svg.getBoundingClientRect()
    // The pointer is in CSS pixels but the chart is drawn in viewBox units.
    const viewX = ((event.clientX - rect.left) / rect.width) * width

    const index = Math.round(((viewX - padding.left) / plotWidth) * (buckets - 1))
    setHoverIndex(index >= 0 && index < buckets ? index : null)
  }

  const hoverTime = hoverIndex === null ? 0 : startTime + hoverIndex * bucketSeconds

  return (
    <div className="chart">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        className="chart-svg"
        onPointerMove={onMove}
        onPointerLeave={() => setHoverIndex(null)}
        role="img"
        aria-label={series.map((s) => s.label).join(', ')}
      >
        {/* The warm-up sits behind everything, in surface ink rather than a
            series colour, because it is chrome and not data. */}
        {warmUpUntil !== undefined && warmUpUntil > startTime && (
          <rect
            x={padding.left}
            y={padding.top}
            width={Math.min(
              plotWidth,
              ((warmUpUntil - startTime) / (buckets * bucketSeconds)) * plotWidth,
            )}
            height={plotHeight}
            fill={CHROME.grid}
            opacity={0.5}
          />
        )}

        {ticks.map((tick) => (
          <g key={tick}>
            <line
              x1={padding.left}
              x2={width - padding.right}
              y1={y(tick)}
              y2={y(tick)}
              stroke={CHROME.grid}
              strokeWidth={1}
            />
            <text x={padding.left - 8} y={y(tick) + 4} textAnchor="end" className="chart-tick">
              {formatNumber(tick, tick < 10 && tick !== 0 ? 1 : 0)}
            </text>
          </g>
        ))}

        {unit && (
          <text x={padding.left - 8} y={padding.top - 2} textAnchor="end" className="chart-unit">
            {unit}
          </text>
        )}

        {series.map((s, i) => {
          const color = SERIES[s.slot ?? i] ?? SERIES[0]
          const path = s.values
            .map((v, index) => `${index === 0 ? 'M' : 'L'} ${x(index)} ${y(v)}`)
            .join(' ')

          return (
            <g key={s.key}>
              {area && series.length === 1 && (
                <path
                  d={`${path} L ${x(s.values.length - 1)} ${y(0)} L ${x(0)} ${y(0)} Z`}
                  fill={color}
                  opacity={0.1}
                />
              )}
              <path
                d={path}
                fill="none"
                stroke={color}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            </g>
          )
        })}

        {/* Direct labels at the line ends, which is where series separate. */}
        {series.length > 1 &&
          series.length <= 4 &&
          series.map((s) => {
            const last = s.values[s.values.length - 1] ?? 0
            return (
              <text
                key={s.key}
                x={width - padding.right + 2}
                y={y(last) + 4}
                className="chart-end-label"
                textAnchor="start"
              >
                {formatNumber(last, 0)}
              </text>
            )
          })}

        {hoverIndex !== null && (
          <g pointerEvents="none">
            <line
              x1={x(hoverIndex)}
              x2={x(hoverIndex)}
              y1={padding.top}
              y2={padding.top + plotHeight}
              stroke={CHROME.axis}
              strokeWidth={1}
            />
            {series.map((s, i) => {
              const value = s.values[hoverIndex]
              if (value === undefined) return null
              const color = SERIES[s.slot ?? i] ?? SERIES[0]
              return (
                <circle
                  key={s.key}
                  cx={x(hoverIndex)}
                  cy={y(value)}
                  r={4}
                  fill={color}
                  // The surface ring keeps the dot legible where it crosses a
                  // line or another dot.
                  stroke="#0f1430"
                  strokeWidth={2}
                />
              )
            })}
          </g>
        )}

        <line
          x1={padding.left}
          x2={width - padding.right}
          y1={padding.top + plotHeight}
          y2={padding.top + plotHeight}
          stroke={CHROME.axis}
          strokeWidth={1}
        />

        <text x={padding.left} y={height - 6} className="chart-tick">
          {formatSimTime(startTime)}
        </text>
        <text x={width - padding.right} y={height - 6} textAnchor="end" className="chart-tick">
          {formatSimTime(startTime + buckets * bucketSeconds)}
        </text>
      </svg>

      {hoverIndex !== null && (
        <div
          className="chart-tooltip"
          style={{ left: `${(x(hoverIndex) / width) * 100}%` }}
          role="status"
        >
          <div className="tooltip-time">{formatSimTime(hoverTime)}</div>
          {series.map((s, i) => (
            <div key={s.key} className="tooltip-row">
              <span className="tooltip-key" style={{ background: SERIES[s.slot ?? i] ?? SERIES[0] }} />
              {/* Values lead, labels follow: here the reader has the series and
                  wants the number. */}
              <strong>{formatNumber(s.values[hoverIndex] ?? 0, 1)}</strong>
              <span className="tooltip-label">{s.label}</span>
            </div>
          ))}
        </div>
      )}

      {series.length > 1 && (
        <div className="chart-legend">
          {series.map((s, i) => (
            <span key={s.key} className="legend-item">
              <span className="legend-line" style={{ background: SERIES[s.slot ?? i] ?? SERIES[0] }} />
              {s.label}
            </span>
          ))}
        </div>
      )}

      <button className="btn ghost small chart-table-toggle" onClick={() => setShowTable((v) => !v)}>
        {showTable ? 'Hide values' : 'Show values'}
      </button>

      {showTable && (
        <div className="chart-table">
          <table>
            <thead>
              <tr>
                <th>Time</th>
                {series.map((s) => (
                  <th key={s.key} className="num">
                    {s.label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {Array.from({ length: buckets }, (_, index) => (
                <tr key={index}>
                  <td className="mono">{formatSimTime(startTime + index * bucketSeconds)}</td>
                  {series.map((s) => (
                    <td key={s.key} className="num mono">
                      {formatNumber(s.values[index] ?? 0, 2)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
