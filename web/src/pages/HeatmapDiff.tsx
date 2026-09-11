import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { api } from '@/api/client'
import { diffScale, divergingRGBA } from '@/charts/tokens'
import { formatHeatValue, type HeatmapFile } from '@/viewer/heatmap'

interface Side {
  id: string
  name: string
}

/**
 * Where on the site the two scenarios differ.
 *
 * A delta table says how much a measure moved; this says where it moved, which
 * is the question a plan change is actually about. One cell of one scenario's
 * grid minus the same cell of the other, drawn on the diverging ramp, so a
 * reader sees congestion move from one aisle to the next rather than only that
 * a mean went down.
 *
 * Both grids come from one replication each, so this is an illustration rather
 * than evidence. The delta table above it is what carries the claim, and the
 * note under the picture says so.
 */
export function HeatmapDiff({
  projectId,
  baseline,
  other,
}: {
  projectId: string
  baseline: Side
  other: Side
}) {
  const [metric, setMetric] = useState<string>('')
  const [hover, setHover] = useState<{ x: number; y: number; text: string } | null>(null)
  const canvas = useRef<HTMLCanvasElement | null>(null)

  const left = useQuery({
    queryKey: ['heatmaps', projectId, baseline.id],
    queryFn: () => api.runs.aggregate<HeatmapFile>(projectId, baseline.id, 'heatmaps.json'),
  })

  const right = useQuery({
    queryKey: ['heatmaps', projectId, other.id],
    queryFn: () => api.runs.aggregate<HeatmapFile>(projectId, other.id, 'heatmaps.json'),
  })

  // Only a metric both runs recorded can be differenced, and only if the grids
  // line up. Two scenarios on different plans produce grids of different
  // shapes, and subtracting those cell by cell would be nonsense drawn
  // convincingly.
  const shared = useMemo(() => {
    if (!left.data || !right.data) return []
    if (!sameGrid(left.data, right.data)) return []

    const names = new Set(right.data.layers.map((l) => l.metric))
    return left.data.layers.filter((l) => names.has(l.metric))
  }, [left.data, right.data])

  const active = shared.find((l) => l.metric === metric) ?? shared[0] ?? null

  const field = useMemo(() => {
    if (!left.data || !right.data || !active) return null

    const otherLayer = right.data.layers.find((l) => l.metric === active.metric)
    if (!otherLayer) return null

    const cells = left.data.cols * left.data.rows
    const values = new Float64Array(cells)

    for (let i = 0; i < cells; i++) {
      values[i] = (otherLayer.totals[i] ?? 0) - (active.totals[i] ?? 0)
    }

    return { values, scale: symmetricScale(values), unit: active.unit }
  }, [left.data, right.data, active])

  useEffect(() => {
    const element = canvas.current
    if (!element || !field || !left.data) return

    const grid = left.data
    element.width = grid.cols
    element.height = grid.rows

    const ctx = element.getContext('2d')
    if (!ctx) return

    const image = ctx.createImageData(grid.cols, grid.rows)

    for (let row = 0; row < grid.rows; row++) {
      // Grid row zero is the southern edge; image row zero is the top.
      const imageRow = grid.rows - 1 - row

      for (let col = 0; col < grid.cols; col++) {
        const value = field.values[row * grid.cols + col]
        const offset = (imageRow * grid.cols + col) * 4

        const [r, g, b, a] = divergingRGBA(diffScale(value, field.scale))
        image.data[offset] = r
        image.data[offset + 1] = g
        image.data[offset + 2] = b
        image.data[offset + 3] = a
      }
    }

    ctx.putImageData(image, 0, 0)
  }, [field, left.data])

  if (left.isLoading || right.isLoading) return null
  if (!left.data || !right.data) return null

  if (shared.length === 0) {
    return (
      <div className="card">
        <h3 style={{ marginBottom: 8 }}>Where they differ</h3>
        <p className="chart-note" style={{ marginTop: 0 }}>
          {sameGrid(left.data, right.data)
            ? 'These runs have no heatmap metric in common.'
            : 'These scenarios were run on different plan geometry, so their grids cannot be subtracted cell by cell.'}
        </p>
      </div>
    )
  }

  const grid = left.data

  return (
    <div className="card">
      <div className="card-head">
        <h3 style={{ flex: 1 }}>Where they differ</h3>

        <select
          className="select-inline"
          value={active?.metric ?? ''}
          onChange={(event) => setMetric(event.target.value)}
        >
          {shared.map((layer) => (
            <option key={layer.metric} value={layer.metric}>
              {layer.label}
            </option>
          ))}
        </select>
      </div>

      <div
        className="diff-canvas"
        style={{ aspectRatio: `${grid.cols} / ${Math.max(grid.rows, 1)}` }}
        onPointerLeave={() => setHover(null)}
        onPointerMove={(event) => {
          const box = event.currentTarget.getBoundingClientRect()
          const col = Math.floor(((event.clientX - box.left) / box.width) * grid.cols)
          // The picture is drawn top-down, so the row has to be flipped back.
          const row =
            grid.rows - 1 - Math.floor(((event.clientY - box.top) / box.height) * grid.rows)

          if (!field || col < 0 || row < 0 || col >= grid.cols || row >= grid.rows) {
            setHover(null)
            return
          }

          const value = field.values[row * grid.cols + col]
          const magnitude = formatHeatValue(Math.abs(value), field.unit)

          setHover({
            x: event.clientX - box.left,
            y: event.clientY - box.top,
            text:
              value === 0
                ? 'No change in this cell'
                : value > 0
                  ? `${magnitude} more in ${other.name}`
                  : `${magnitude} more in ${baseline.name}`,
          })
        }}
      >
        <canvas ref={canvas} />

        {hover && (
          <div className="chart-tooltip" style={{ left: hover.x, top: hover.y - 8 }} role="status">
            <div className="tooltip-row">
              <strong>{hover.text}</strong>
            </div>
            <div className="tooltip-time">{grid.cellMeters} m square</div>
          </div>
        )}
      </div>

      <div className="chart-legend diff-legend">
        <span className="legend-item">
          <span className="legend-swatch low" /> more in {baseline.name}
        </span>
        <span className="legend-item">
          <span className="legend-swatch mid" /> no change
        </span>
        <span className="legend-item">
          <span className="legend-swatch high" /> more in {other.name}
        </span>
      </div>

      <p className="chart-note">
        One replication of each scenario, not the whole set, so read this as where a change shows up
        rather than as evidence that it is real. The table above is what settles that.
      </p>
    </div>
  )
}

/** Two grids can only be subtracted if they cover the same ground. */
function sameGrid(a: HeatmapFile, b: HeatmapFile): boolean {
  return (
    a.cols === b.cols &&
    a.rows === b.rows &&
    a.cellMeters === b.cellMeters &&
    Math.abs(a.minX - b.minX) < 1e-6 &&
    Math.abs(a.minY - b.minY) < 1e-6
  )
}

/**
 * The value the ramp saturates at, taken from the 95th percentile of the cells
 * that changed rather than from the largest one. One extreme cell is normal in
 * a difference field, and scaling to it would flatten everything else.
 */
function symmetricScale(values: Float64Array): number {
  const magnitudes: number[] = []
  for (let i = 0; i < values.length; i++) {
    if (values[i] !== 0) magnitudes.push(Math.abs(values[i]))
  }

  if (magnitudes.length === 0) return 0

  magnitudes.sort((a, b) => a - b)
  return magnitudes[Math.min(magnitudes.length - 1, Math.floor(magnitudes.length * 0.95))]
}
