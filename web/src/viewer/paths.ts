/** One entity's journey, as the server sampled it. */
export interface JourneyPath {
  id: number
  class: number
  start: number
  end: number
  /** Flattened x, y pairs in world metres. */
  points: number[]
  distance: number
}

export interface PathsFile {
  paths: JourneyPath[]
  /** How many were kept, and out of how many. The UI says both, so the picture
   *  never implies it shows every journey. */
  sampled: number
  total: number
}

/**
 * Draws entity journeys over the plan: the spaghetti diagram.
 *
 * This is one series, not many. Every line is the same kind of thing, so they
 * share one hue and the density does the talking: a route taken by two hundred
 * entities reads dark because two hundred translucent strokes overlap there,
 * not because anything was counted. Colouring each journey separately would
 * spend the identity channel on ids nobody is looking up.
 */
export class PathRenderer {
  private file: PathsFile | null = null
  private limit = 200

  setFile(file: PathsFile | null) {
    this.file = file
  }

  get available(): boolean {
    return (this.file?.paths.length ?? 0) > 0
  }

  get sampled(): number {
    return Math.min(this.file?.sampled ?? 0, this.limit)
  }

  get total(): number {
    return this.file?.total ?? 0
  }

  setLimit(limit: number) {
    this.limit = Math.max(1, limit)
  }

  /**
   * Renders the journeys.
   *
   * activeUntil, when given, draws only the part of each journey that had
   * happened by that moment, so the diagram can grow alongside playback rather
   * than sitting there as a finished picture.
   */
  render(
    ctx: CanvasRenderingContext2D,
    toScreen: (x: number, y: number) => { x: number; y: number },
    opacity: number,
    activeUntil?: number,
  ) {
    const file = this.file
    if (!file || file.paths.length === 0) return

    ctx.save()
    ctx.strokeStyle = '#3987e5'
    ctx.lineWidth = 1.5
    ctx.lineCap = 'round'
    ctx.lineJoin = 'round'

    // Each stroke is faint so that overlap, rather than any single line, is
    // what makes a route visible. The floor keeps a sparse run from vanishing.
    ctx.globalAlpha = Math.max(0.05, opacity * (14 / Math.max(this.sampled, 14)))

    const shown = Math.min(file.paths.length, this.limit)

    for (let i = 0; i < shown; i++) {
      const path = file.paths[i]

      if (activeUntil !== undefined && path.start > activeUntil) continue

      // A journey part-way through is drawn up to where it has got to, in
      // proportion to how much of its duration has elapsed.
      let points = path.points.length
      if (activeUntil !== undefined && path.end > activeUntil) {
        const span = path.end - path.start
        const fraction = span > 0 ? (activeUntil - path.start) / span : 1
        points = Math.max(4, Math.round((path.points.length / 2) * fraction) * 2)
      }
      if (points < 4) continue

      ctx.beginPath()
      for (let p = 0; p < points; p += 2) {
        const screen = toScreen(path.points[p], path.points[p + 1])
        if (p === 0) ctx.moveTo(screen.x, screen.y)
        else ctx.lineTo(screen.x, screen.y)
      }
      ctx.stroke()
    }

    ctx.restore()
  }

  /** Summary statistics for the caption beside the diagram. */
  stats(): { count: number; meanDistance: number; maxDistance: number } | null {
    const file = this.file
    if (!file || file.paths.length === 0) return null

    let sum = 0
    let max = 0
    for (const path of file.paths) {
      sum += path.distance
      if (path.distance > max) max = path.distance
    }

    return {
      count: file.paths.length,
      meanDistance: sum / file.paths.length,
      maxDistance: max,
    }
  }
}
