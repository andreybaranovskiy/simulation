import { heatScale, rampRGB } from '@/charts/tokens'

/** One metric's grid, as the server writes it. */
export interface HeatmapLayer {
  metric: string
  label: string
  unit: string
  description: string
  max: number
  /** The value the ramp saturates at: the 95th percentile, not the maximum.
   *  Scaling to the peak washes everything else out. */
  scale: number
  total: number
  totals: number[]
  buckets?: number[][]
}

export interface HeatmapFile {
  cols: number
  rows: number
  cellMeters: number
  minX: number
  minY: number
  buckets: number
  bucketSeconds: number
  startTime: number
  layers: HeatmapLayer[]
}

/**
 * Renders a heatmap grid over the plan.
 *
 * The grid is painted once into an offscreen canvas at one pixel per cell and
 * then scaled up, rather than drawn as thousands of rectangles every frame.
 * Browser image smoothing does the interpolation, which both looks better than
 * hard cells and costs nothing: a 400 by 100 grid is one draw call instead of
 * forty thousand.
 */
export class HeatmapRenderer {
  private file: HeatmapFile | null = null
  private layer: HeatmapLayer | null = null
  private bucket: number | null = null

  /** The painted grid, reused until the layer or the time window changes. */
  private buffer: HTMLCanvasElement | null = null
  private bufferKey = ''

  setFile(file: HeatmapFile | null) {
    this.file = file
    this.layer = file?.layers[0] ?? null
    this.bufferKey = ''
  }

  get layers(): HeatmapLayer[] {
    return this.file?.layers ?? []
  }

  get current(): HeatmapLayer | null {
    return this.layer
  }

  get grid(): HeatmapFile | null {
    return this.file
  }

  selectLayer(metric: string | null) {
    this.layer = metric ? (this.file?.layers.find((l) => l.metric === metric) ?? null) : null
    this.bufferKey = ''
  }

  /** Shows one time slice instead of the whole run. Null shows the total. */
  selectBucket(bucket: number | null) {
    this.bucket = bucket
    this.bufferKey = ''
  }

  get bucketCount(): number {
    return this.file?.buckets ?? 0
  }

  /** The time window a bucket covers, for the scrubber's label. */
  bucketRange(bucket: number): [number, number] {
    const file = this.file
    if (!file) return [0, 0]

    const start = file.startTime + bucket * file.bucketSeconds
    return [start, start + file.bucketSeconds]
  }

  /** Reads the value under a world position, for the pointer tooltip. */
  valueAt(worldX: number, worldY: number): number | null {
    const file = this.file
    const layer = this.layer
    if (!file || !layer) return null

    const col = Math.floor((worldX - file.minX) / file.cellMeters)
    const row = Math.floor((worldY - file.minY) / file.cellMeters)
    if (col < 0 || row < 0 || col >= file.cols || row >= file.rows) return null

    const values = this.activeValues()
    return values?.[row * file.cols + col] ?? null
  }

  private activeValues(): number[] | undefined {
    const layer = this.layer
    if (!layer) return undefined

    if (this.bucket !== null && layer.buckets) {
      return layer.buckets[this.bucket]
    }
    return layer.totals
  }

  /**
   * Draws the grid.
   *
   * toScreen converts world metres to canvas pixels, and is passed in rather
   * than duplicated so the heatmap can never drift out of register with the
   * entities drawn over it.
   */
  render(
    ctx: CanvasRenderingContext2D,
    toScreen: (x: number, y: number) => { x: number; y: number },
    opacity: number,
  ) {
    const file = this.file
    const layer = this.layer
    if (!file || !layer) return

    const buffer = this.ensureBuffer(file, layer)
    if (!buffer) return

    // The grid's bottom-left and top-right corners in world space. Row zero is
    // the southern edge, so the image has to be placed from the top down.
    const topLeft = toScreen(file.minX, file.minY + file.rows * file.cellMeters)
    const bottomRight = toScreen(file.minX + file.cols * file.cellMeters, file.minY)

    const width = bottomRight.x - topLeft.x
    const height = bottomRight.y - topLeft.y
    if (width <= 0 || height <= 0) return

    ctx.save()
    ctx.globalAlpha = opacity

    // Smoothing is what turns a coarse grid into a field. The alternative,
    // hard cells, reads as a spreadsheet rather than a density.
    ctx.imageSmoothingEnabled = true
    ctx.imageSmoothingQuality = 'high'

    // Screen blending keeps the plan underneath visible: the heatmap adds
    // light rather than painting over the drawing it is explaining.
    ctx.globalCompositeOperation = 'screen'
    ctx.drawImage(buffer, topLeft.x, topLeft.y, width, height)
    ctx.restore()
  }

  /** Paints the grid into an offscreen canvas, one pixel per cell. */
  private ensureBuffer(file: HeatmapFile, layer: HeatmapLayer): HTMLCanvasElement | null {
    const key = `${layer.metric}:${this.bucket ?? 'all'}`
    if (this.buffer && this.bufferKey === key) return this.buffer

    const values = this.activeValues()
    if (!values) return null

    const canvas = this.buffer ?? document.createElement('canvas')
    canvas.width = file.cols
    canvas.height = file.rows

    const ctx = canvas.getContext('2d')
    if (!ctx) return null

    const image = ctx.createImageData(file.cols, file.rows)
    const data = image.data

    // A bucket holds a fraction of the run's total, so scaling it against the
    // whole-run scale would make every slice look empty.
    const scale =
      this.bucket !== null && file.buckets > 0 ? layer.scale / Math.sqrt(file.buckets) : layer.scale

    for (let row = 0; row < file.rows; row++) {
      // Grid row zero is the southern edge; image row zero is the top.
      const imageRow = file.rows - 1 - row

      for (let col = 0; col < file.cols; col++) {
        const value = values[row * file.cols + col] ?? 0
        const offset = (imageRow * file.cols + col) * 4

        if (value <= 0) {
          data[offset + 3] = 0
          continue
        }

        const t = heatScale(value, scale)
        const [r, g, b] = rampRGB(t)

        data[offset] = r
        data[offset + 1] = g
        data[offset + 2] = b
        // Alpha climbs with the value as well as lightness, so an empty cell
        // disappears completely instead of tinting the whole site.
        data[offset + 3] = Math.round(Math.min(1, t * 1.15) * 235)
      }
    }

    ctx.putImageData(image, 0, 0)

    this.buffer = canvas
    this.bufferKey = key
    return canvas
  }
}

/** Formats a heatmap value with its unit, for a legend or a tooltip. */
export function formatHeatValue(value: number, unit: string): string {
  const rounded =
    value >= 10_000
      ? `${Math.round(value / 1000).toLocaleString()}k`
      : value >= 100
        ? Math.round(value).toLocaleString()
        : value.toFixed(1)

  switch (unit) {
    case 's':
      return value >= 3600 ? `${(value / 3600).toFixed(1)} h` : `${rounded} s`
    case 'm':
      return value >= 1000 ? `${(value / 1000).toFixed(1)} km` : `${rounded} m`
    case 'entity-s':
      return `${rounded} entity-s`
    default:
      return rounded
  }
}
