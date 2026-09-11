import { useState } from 'react'

import { CHROME, STATUS, formatNumber, rampColor } from './tokens'
import { formatSimTime } from '@/state/timeline'

export interface GanttInterval {
  start: number
  end: number
  busy: number
  queued: number
  down?: boolean
}

export interface GanttRow {
  id: string
  label: string
  capacity: number
  busyFraction: number
  idleFraction: number
  downFraction: number
  peakBusy: number
  peakQueue: number
  intervals: GanttInterval[]
  merged?: boolean
}

/**
 * Resource occupancy over time.
 *
 * Colour encodes magnitude, not category: each stretch is filled from the
 * sequential ramp by how much of the resource was in use. That is a deliberate
 * choice over naming the states. A four-colour "busy / idle / queued / down"
 * scheme would spend the identity channel on labels the row already carries,
 * and worse, it would imply that busy is good, when a resource pinned at
 * capacity is the bottleneck the reader is hunting for.
 *
 * Downtime is the one exception. It is a state rather than a quantity, so it
 * takes the reserved critical status colour and ships with a legend entry.
 */
export function GanttChart({
  rows,
  startTime,
  endTime,
}: {
  rows: GanttRow[]
  startTime: number
  endTime: number
}) {
  const [hover, setHover] = useState<{ row: GanttRow; interval: GanttInterval; x: number } | null>(null)
  const [showTable, setShowTable] = useState(false)

  const duration = Math.max(endTime - startTime, 1)
  const anyDown = rows.some((r) => r.downFraction > 0)

  return (
    <div className="gantt">
      <div className="gantt-rows">
        {rows.map((row) => (
          <div key={row.id} className="gantt-row">
            <div className="gantt-label" title={`Capacity ${row.capacity}`}>
              <span className="gantt-name">{row.label}</span>
              <span className="gantt-capacity">×{row.capacity}</span>
            </div>

            <div
              className="gantt-track"
              onPointerLeave={() => setHover(null)}
              role="img"
              aria-label={`${row.label}: ${(row.busyFraction * 100).toFixed(0)} percent in use`}
            >
              {row.intervals.map((interval, index) => {
                const left = ((interval.start - startTime) / duration) * 100
                const width = ((interval.end - interval.start) / duration) * 100
                if (width <= 0) return null

                const occupancy = row.capacity > 0 ? interval.busy / row.capacity : 0

                return (
                  <span
                    key={index}
                    className={`gantt-span ${interval.down ? 'down' : ''}`}
                    style={{
                      left: `${left}%`,
                      width: `${Math.max(width, 0.08)}%`,
                      background: interval.down ? STATUS.critical : rampColor(occupancy),
                      // An empty stretch fades into the surface rather than
                      // drawing a band of "nothing happened".
                      opacity: interval.down ? 0.9 : occupancy === 0 ? 0.18 : 1,
                    }}
                    onPointerEnter={(e) =>
                      setHover({
                        row,
                        interval,
                        x: e.nativeEvent.offsetX + (left / 100) * e.currentTarget.parentElement!.clientWidth,
                      })
                    }
                  />
                )
              })}
            </div>

            <div className="gantt-summary">
              <span className="gantt-util">{(row.busyFraction * 100).toFixed(0)}%</span>
              {row.peakQueue > 0 && (
                <span className="gantt-peak" title="Longest queue behind this resource">
                  peak {row.peakQueue}
                </span>
              )}
            </div>
          </div>
        ))}
      </div>

      <div className="gantt-axis">
        <span>{formatSimTime(startTime)}</span>
        <span>{formatSimTime(startTime + duration / 2)}</span>
        <span>{formatSimTime(endTime)}</span>
      </div>

      {hover && (
        <div className="chart-tooltip gantt-tooltip" style={{ left: hover.x }} role="status">
          <div className="tooltip-time">
            {formatSimTime(hover.interval.start)} – {formatSimTime(hover.interval.end)}
          </div>
          <div className="tooltip-row">
            <strong>
              {hover.interval.busy} of {hover.row.capacity}
            </strong>
            <span className="tooltip-label">in use</span>
          </div>
          {hover.interval.queued > 0 && (
            <div className="tooltip-row">
              <strong>{hover.interval.queued}</strong>
              <span className="tooltip-label">waiting</span>
            </div>
          )}
          {hover.interval.down && (
            <div className="tooltip-row">
              <span className="tooltip-key" style={{ background: STATUS.critical }} />
              <span className="tooltip-label">Out of service</span>
            </div>
          )}
        </div>
      )}

      <div className="chart-legend gantt-legend">
        <span className="legend-item">
          <span className="legend-ramp" />
          In use, none to full
        </span>
        {anyDown && (
          <span className="legend-item">
            <span className="legend-swatch" style={{ background: STATUS.critical }} />
            Out of service
          </span>
        )}
        {rows.some((r) => r.merged) && (
          <span className="legend-item faint" title="Very brief changes were combined so the bar stays readable. The percentages are exact.">
            simplified
          </span>
        )}
      </div>

      <button className="btn ghost small chart-table-toggle" onClick={() => setShowTable((v) => !v)}>
        {showTable ? 'Hide values' : 'Show values'}
      </button>

      {showTable && (
        <div className="chart-table">
          <table>
            <thead>
              <tr>
                <th>Resource</th>
                <th className="num">Capacity</th>
                <th className="num">In use</th>
                <th className="num">Idle</th>
                <th className="num">Down</th>
                <th className="num">Peak queue</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id}>
                  <td>{row.label}</td>
                  <td className="num mono">{row.capacity}</td>
                  <td className="num mono">{(row.busyFraction * 100).toFixed(1)}%</td>
                  <td className="num mono">{(row.idleFraction * 100).toFixed(1)}%</td>
                  <td className="num mono">{(row.downFraction * 100).toFixed(1)}%</td>
                  <td className="num mono">{formatNumber(row.peakQueue)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

export { CHROME }
