import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { api } from '@/api/client'
import { heatScale, rampRGB } from '@/charts/tokens'
import { formatHeatValue, type HeatmapFile } from '@/viewer/heatmap'
import { usePrintGate } from './PrintDocument'

/**
 * A single run's density field, for a report.
 *
 * The interactive viewer draws the heatmap over the calibrated plan image; a
 * report cannot carry the whole viewer, so this draws the same grid on its own,
 * from the same file. It loses the plan underneath but keeps the thing the
 * section is about: where the activity concentrated, on the same ramp and
 * scale the screen uses, with the metric's own legend.
 */
export function ReportHeatmap({ projectId, runId }: { projectId: string; runId: string }) {
  const canvas = useRef<HTMLCanvasElement | null>(null)
  const [metric, setMetric] = useState<string>('')

  const heat = useQuery({
    queryKey: ['heatmaps', projectId, runId],
    queryFn: () => api.runs.aggregate<HeatmapFile>(projectId, runId, 'heatmaps.json'),
    staleTime: Infinity,
  })

  usePrintGate(!heat.isLoading)

  const layer = useMemo(() => {
    const file = heat.data
    if (!file || file.layers.length === 0) return null
    return file.layers.find((l) => l.metric === metric) ?? file.layers[0]
  }, [heat.data, metric])

  useEffect(() => {
    const element = canvas.current
    const file = heat.data
    if (!element || !file || !layer) return

    element.width = file.cols
    element.height = file.rows
    const ctx = element.getContext('2d')
    if (!ctx) return

    const image = ctx.createImageData(file.cols, file.rows)
    for (let row = 0; row < file.rows; row++) {
      // Grid row zero is the southern edge; image row zero is the top.
      const imageRow = file.rows - 1 - row
      for (let col = 0; col < file.cols; col++) {
        const value = layer.totals[row * file.cols + col] ?? 0
        const offset = (imageRow * file.cols + col) * 4
        if (value <= 0) {
          image.data[offset + 3] = 0
          continue
        }
        const t = heatScale(value, layer.scale)
        const [r, g, b] = rampRGB(t)
        image.data[offset] = r
        image.data[offset + 1] = g
        image.data[offset + 2] = b
        image.data[offset + 3] = Math.round(Math.min(1, t * 1.15) * 235)
      }
    }
    ctx.putImageData(image, 0, 0)
  }, [heat.data, layer])

  if (!heat.data || heat.data.layers.length === 0 || !layer) return null

  const file = heat.data

  return (
    <section className="card print-block">
      <div className="card-head">
        <h3 style={{ flex: 1 }}>Density on the plan</h3>
        {file.layers.length > 1 && (
          <select
            className="select-inline"
            value={layer.metric}
            onChange={(event) => setMetric(event.target.value)}
          >
            {file.layers.map((l) => (
              <option key={l.metric} value={l.metric}>
                {l.label}
              </option>
            ))}
          </select>
        )}
      </div>

      <div
        className="diff-canvas"
        style={{ aspectRatio: `${file.cols} / ${Math.max(file.rows, 1)}` }}
      >
        <canvas ref={canvas} />
      </div>

      <div className="chart-legend">
        <span className="legend-item">
          <span className="legend-ramp" /> {layer.label.toLowerCase()}, low to high
        </span>
        <span className="legend-item">
          up to {formatHeatValue(layer.max, layer.unit)} in a {file.cellMeters} m cell
        </span>
      </div>

      {layer.description && <p className="chart-note">{layer.description}</p>}
    </section>
  )
}
