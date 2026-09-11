import type { ClassInfo, Manifest, SitePlan } from '@/api/types'
import { EntityState, stateColors } from '@/api/types'
import type { EntityAt } from './playback'
import type { HeatmapRenderer } from './heatmap'
import type { PathRenderer } from './paths'

/**
 * The top-down plan view.
 *
 * Canvas rather than WebGL, deliberately. What this view has to do well is draw
 * a georeferenced image, thousands of small marks, and crisp text at any zoom.
 * A 2D context does all three without a shader, and the entity count that would
 * make it struggle is far past the count that makes the picture unreadable
 * anyway.
 *
 * It reads the same clock as the 3D scene, so the two are one view of a run
 * shown two ways rather than two viewers that happen to agree.
 */

export interface View2DOptions {
  colorByState: boolean
  showNodes: boolean
  showZones: boolean
  showLabels: boolean
  showPlan: boolean
  /** Draws a short tail behind each moving entity, which turns a field of dots
   *  into something a reader can see direction and speed in. */
  showTrails: boolean
  planOpacity: number

  /** Aggregate overlays. These show the whole run at once, which is a
   *  different question from where things are at this instant, so turning one
   *  on dims the entity marks rather than competing with them. */
  showHeatmap: boolean
  heatmapOpacity: number
  showPaths: boolean
  /** Grows the journey traces alongside playback instead of showing the
   *  finished picture from the first frame. */
  pathsFollowClock: boolean
}

export const DEFAULT_2D_OPTIONS: View2DOptions = {
  colorByState: true,
  showNodes: true,
  showZones: true,
  showLabels: true,
  showPlan: true,
  showTrails: true,
  planOpacity: 0.55,
  showHeatmap: false,
  heatmapOpacity: 0.85,
  showPaths: false,
  pathsFollowClock: false,
}

/** The pan and zoom state, in world metres. */
export interface Camera2D {
  /** Metres per CSS pixel. Smaller is more zoomed in. */
  scale: number
  centerX: number
  centerY: number
}

export class Scene2D {
  private canvas: HTMLCanvasElement
  private ctx: CanvasRenderingContext2D

  private manifest: Manifest | null = null
  private plan: SitePlan | null = null
  private planImage: HTMLImageElement | null = null

  private heatmap: HeatmapRenderer | null = null
  private paths: PathRenderer | null = null

  private options: View2DOptions = { ...DEFAULT_2D_OPTIONS }
  private hiddenClasses = new Set<number>()

  private camera: Camera2D = { scale: 1, centerX: 0, centerY: 0 }
  private dpr = 1

  /** Recent positions per entity, for the motion trails. */
  private trails = new Map<number, number[]>()
  private trailTime = 0

  /** Whether the view has been framed against a canvas that actually had a
   *  size. A scene built before the browser has laid the canvas out would
   *  otherwise frame itself against a one-pixel viewport and open zoomed out
   *  by four orders of magnitude. */
  private framed = false

  constructor(canvas: HTMLCanvasElement) {
    this.canvas = canvas

    const ctx = canvas.getContext('2d', { alpha: false })
    if (!ctx) throw new Error('This browser cannot draw the plan view.')
    this.ctx = ctx

    this.resize()
  }

  setOptions(options: Partial<View2DOptions>) {
    this.options = { ...this.options, ...options }
  }

  setHiddenClasses(hidden: Set<number>) {
    this.hiddenClasses = hidden
  }

  setOverlays(heatmap: HeatmapRenderer | null, paths: PathRenderer | null) {
    this.heatmap = heatmap
    this.paths = paths
  }

  /** Whether an aggregate overlay is on, which the entity marks defer to. */
  private get overlayActive(): boolean {
    return (this.options.showHeatmap && !!this.heatmap?.current) || this.options.showPaths
  }

  build(manifest: Manifest) {
    this.manifest = manifest
    this.trails.clear()
    this.framed = false
    this.fitToBounds()
  }

  async setPlan(plan: SitePlan | null, imageUrl: string | null) {
    this.plan = plan
    this.planImage = null

    if (!plan || !imageUrl) return

    const image = new Image()
    image.src = imageUrl
    await image.decode().catch(() => undefined)
    this.planImage = image
  }

  /** Frames the whole site, which is where the view should open. */
  fitToBounds() {
    if (!this.manifest) return

    const b = this.manifest.bounds
    const width = Math.max(b.maxX - b.minX, 1)
    const height = Math.max(b.maxY - b.minY, 1)

    const viewWidth = this.canvas.clientWidth
    const viewHeight = this.canvas.clientHeight

    // Nothing sensible can be computed against a canvas with no size. Leaving
    // framed false means the next render tries again once layout has happened.
    if (viewWidth < 2 || viewHeight < 2) return
    this.framed = true

    // A margin keeps entities at the extreme edge from being clipped.
    this.camera.scale = Math.max(width / viewWidth, height / viewHeight) * 1.12
    this.camera.centerX = (b.minX + b.maxX) / 2
    this.camera.centerY = (b.minY + b.maxY) / 2
  }

  /**
   * Zooms about a screen point, so the point under the cursor stays put.
   *
   * factor is a magnification: above one moves closer. The camera stores
   * metres per pixel, which runs the other way, so the factor is inverted
   * here rather than at each call site. Getting that backwards made the wheel
   * zoom out, which every map in the world has taught people means the
   * opposite.
   */
  zoomAt(screenX: number, screenY: number, factor: number) {
    const before = this.toWorld(screenX, screenY)

    this.camera.scale = clamp(this.camera.scale / factor, 0.002, 500)

    const after = this.toWorld(screenX, screenY)
    this.camera.centerX += before.x - after.x
    this.camera.centerY += before.y - after.y
  }

  pan(dxPixels: number, dyPixels: number) {
    this.camera.centerX -= dxPixels * this.camera.scale
    // Screen Y grows downward while world Y grows north, so a drag down moves
    // the camera north.
    this.camera.centerY += dyPixels * this.camera.scale
  }

  centerOn(x: number, y: number) {
    this.camera.centerX = x
    this.camera.centerY = y
  }

  getCamera(): Camera2D {
    return { ...this.camera }
  }

  /** Converts a screen point to world metres. */
  toWorld(screenX: number, screenY: number): { x: number; y: number } {
    const cx = (this.canvas.clientWidth || 1) / 2
    const cy = (this.canvas.clientHeight || 1) / 2

    return {
      x: this.camera.centerX + (screenX - cx) * this.camera.scale,
      y: this.camera.centerY - (screenY - cy) * this.camera.scale,
    }
  }

  /** Converts world metres to a screen point. */
  toScreen(x: number, y: number): { x: number; y: number } {
    const cx = (this.canvas.clientWidth || 1) / 2
    const cy = (this.canvas.clientHeight || 1) / 2

    return {
      x: cx + (x - this.camera.centerX) / this.camera.scale,
      y: cy - (y - this.camera.centerY) / this.camera.scale,
    }
  }

  /** Finds the entity nearest a screen point, within a tolerance. */
  pick(entities: EntityAt[], screenX: number, screenY: number): number | null {
    const world = this.toWorld(screenX, screenY)
    // The tolerance is in pixels, so picking is equally easy at any zoom.
    const tolerance = 14 * this.camera.scale

    let best: number | null = null
    let bestDistance = tolerance

    for (const e of entities) {
      if (this.hiddenClasses.has(e.cls)) continue

      const distance = Math.hypot(e.x - world.x, e.y - world.y)
      if (distance < bestDistance) {
        bestDistance = distance
        best = e.id
      }
    }
    return best
  }

  resize() {
    // Backing store at device resolution, CSS size in layout pixels: without
    // this the plan and its labels are blurry on any high-density display.
    this.dpr = Math.min(window.devicePixelRatio || 1, 2)

    const width = this.canvas.clientWidth || 1
    const height = this.canvas.clientHeight || 1

    this.canvas.width = Math.round(width * this.dpr)
    this.canvas.height = Math.round(height * this.dpr)
  }

  render(entities: EntityAt[], time: number, selectedId: number | null, hoveredId: number | null) {
    const { ctx } = this
    const width = this.canvas.clientWidth || 1
    const height = this.canvas.clientHeight || 1

    ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0)
    ctx.fillStyle = '#0b1020'
    ctx.fillRect(0, 0, width, height)

    if (!this.manifest) return

    // The first render that happens after the canvas has real dimensions is
    // the earliest moment the view can be framed correctly.
    if (!this.framed) this.fitToBounds()

    const toScreen = (x: number, y: number) => this.toScreen(x, y)

    this.drawPlan(ctx)

    // The heatmap sits directly on the plan, under everything that names a
    // place, so labels and markers stay readable on top of it.
    if (this.options.showHeatmap && this.heatmap) {
      this.heatmap.render(ctx, toScreen, this.options.heatmapOpacity)
    }

    if (this.options.showPaths && this.paths) {
      this.paths.render(
        ctx,
        toScreen,
        1,
        this.options.pathsFollowClock ? time : undefined,
      )
    }

    this.drawZones(ctx)
    this.drawNodes(ctx)

    if (this.options.showTrails && !this.overlayActive) {
      this.updateTrails(entities, time)
      this.drawTrails(ctx)
    }

    this.drawEntities(ctx, entities, selectedId, hoveredId)
    this.drawScaleBar(ctx, width, height)
  }

  private drawPlan(ctx: CanvasRenderingContext2D) {
    if (!this.options.showPlan || !this.plan || !this.planImage) return

    const plan = this.plan
    const widthMeters = plan.imageWidth * plan.metersPerPixel
    const heightMeters = plan.imageHeight * plan.metersPerPixel

    // The image's origin pixel sits at the world origin, which is what makes
    // the drawing line up with the model rather than merely sit behind it.
    const left = -plan.originPxX * plan.metersPerPixel
    const top = plan.flipY
      ? plan.originPxY * plan.metersPerPixel
      : -plan.originPxY * plan.metersPerPixel

    const topLeft = this.toScreen(left, top)
    const drawWidth = widthMeters / this.camera.scale
    const drawHeight = heightMeters / this.camera.scale

    ctx.save()
    ctx.globalAlpha = this.options.planOpacity

    if (plan.rotationDeg !== 0) {
      const origin = this.toScreen(0, 0)
      ctx.translate(origin.x, origin.y)
      ctx.rotate((-plan.rotationDeg * Math.PI) / 180)
      ctx.translate(-origin.x, -origin.y)
    }

    ctx.imageSmoothingEnabled = true
    ctx.imageSmoothingQuality = 'high'
    ctx.drawImage(this.planImage, topLeft.x, topLeft.y, drawWidth, drawHeight)
    ctx.restore()
  }

  private drawZones(ctx: CanvasRenderingContext2D) {
    if (!this.options.showZones || !this.manifest?.zones) return

    for (const zone of this.manifest.zones) {
      const topLeft = this.toScreen(zone.x, zone.y + zone.height)
      const w = zone.width / this.camera.scale
      const h = zone.height / this.camera.scale

      ctx.save()
      ctx.fillStyle = zone.color || '#3fc98a'
      ctx.globalAlpha = 0.1
      ctx.fillRect(topLeft.x, topLeft.y, w, h)

      ctx.globalAlpha = 0.5
      ctx.strokeStyle = zone.color || '#3fc98a'
      ctx.lineWidth = 1
      ctx.strokeRect(topLeft.x, topLeft.y, w, h)

      if (this.options.showLabels && w > 60) {
        ctx.globalAlpha = 0.85
        ctx.fillStyle = '#cfe0ff'
        ctx.font = '11px system-ui, sans-serif'
        ctx.fillText(zone.label, topLeft.x + 6, topLeft.y + 16)
      }
      ctx.restore()
    }
  }

  private drawNodes(ctx: CanvasRenderingContext2D) {
    if (!this.options.showNodes || !this.manifest) return

    const resourceAt = new Map<string, string>()
    for (const r of this.manifest.resources ?? []) {
      resourceAt.set(`${r.x.toFixed(2)},${r.y.toFixed(2)}`, r.label)
    }

    ctx.save()
    ctx.font = '11px system-ui, sans-serif'

    for (const node of this.manifest.nodes ?? []) {
      const p = this.toScreen(node.x, node.y)
      if (p.x < -50 || p.y < -50 || p.x > this.canvas.clientWidth + 50 || p.y > this.canvas.clientHeight + 50) {
        continue
      }

      const label = resourceAt.get(`${node.x.toFixed(2)},${node.y.toFixed(2)}`)
      const isResource = label !== undefined

      ctx.beginPath()
      ctx.arc(p.x, p.y, isResource ? 6 : 3.5, 0, Math.PI * 2)
      ctx.fillStyle = isResource ? 'rgba(242, 193, 78, 0.9)' : 'rgba(76, 107, 191, 0.75)'
      ctx.fill()

      if (isResource) {
        ctx.strokeStyle = 'rgba(242, 193, 78, 0.45)'
        ctx.lineWidth = 1.5
        ctx.beginPath()
        ctx.arc(p.x, p.y, 11, 0, Math.PI * 2)
        ctx.stroke()
      }

      // Labels appear only when zoomed in enough to read them without
      // colliding, which is what keeps a site with sixty nodes legible.
      if (this.options.showLabels && this.camera.scale < 1.2) {
        ctx.fillStyle = isResource ? '#f2c14e' : '#8fa3d8'
        ctx.fillText(label ?? node.label, p.x + 10, p.y + 4)
      }
    }
    ctx.restore()
  }

  /**
   * Keeps a short history per entity.
   *
   * Trails are rebuilt from scratch when the clock jumps, because a trail
   * stitched across a scrub would draw a line between two unrelated places.
   */
  private updateTrails(entities: EntityAt[], time: number) {
    const jumped = Math.abs(time - this.trailTime) > 30
    if (jumped) this.trails.clear()
    this.trailTime = time

    const present = new Set<number>()

    for (const e of entities) {
      present.add(e.id)
      if (!e.moving) continue

      let trail = this.trails.get(e.id)
      if (!trail) {
        trail = []
        this.trails.set(e.id, trail)
      }

      trail.push(e.x, e.y)
      // Six points is enough to show direction and speed without turning the
      // view into a scribble.
      if (trail.length > 12) trail.splice(0, trail.length - 12)
    }

    for (const id of this.trails.keys()) {
      if (!present.has(id)) this.trails.delete(id)
    }
  }

  private drawTrails(ctx: CanvasRenderingContext2D) {
    ctx.save()
    ctx.strokeStyle = 'rgba(120, 160, 255, 0.28)'
    ctx.lineWidth = 1.5
    ctx.lineCap = 'round'

    for (const trail of this.trails.values()) {
      if (trail.length < 4) continue

      ctx.beginPath()
      for (let i = 0; i < trail.length; i += 2) {
        const p = this.toScreen(trail[i], trail[i + 1])
        if (i === 0) ctx.moveTo(p.x, p.y)
        else ctx.lineTo(p.x, p.y)
      }
      ctx.stroke()
    }
    ctx.restore()
  }

  private drawEntities(
    ctx: CanvasRenderingContext2D,
    entities: EntityAt[],
    selectedId: number | null,
    hoveredId: number | null,
  ) {
    if (!this.manifest) return

    const classes = this.manifest.classes ?? []
    const width = this.canvas.clientWidth
    const height = this.canvas.clientHeight

    ctx.save()

    // With an aggregate overlay up, the marks become context for it rather
    // than the subject, so they step back instead of fighting it for
    // attention.
    if (this.overlayActive) ctx.globalAlpha = 0.55

    for (const e of entities) {
      if (this.hiddenClasses.has(e.cls)) continue

      const p = this.toScreen(e.x, e.y)
      if (p.x < -20 || p.y < -20 || p.x > width + 20 || p.y > height + 20) continue

      const info: ClassInfo | undefined = classes[e.cls]
      const emphasised = e.id === selectedId || e.id === hoveredId

      ctx.fillStyle = emphasised
        ? '#ffffff'
        : this.options.colorByState
          ? stateColors[e.state as EntityState] ?? '#6b7590'
          : info?.color ?? '#4c8dff'

      // Entities are drawn at their real size once zoomed in, and as a
      // readable dot when zoomed out. Drawing a 16 metre truck at true scale
      // across a whole terminal would make it a single pixel.
      const trueLength = (info?.length ?? 3) / this.camera.scale
      const trueWidth = (info?.width ?? 2) / this.camera.scale

      if (trueLength > 6) {
        ctx.save()
        ctx.translate(p.x, p.y)
        ctx.rotate(-e.heading)
        ctx.fillRect(-trueWidth / 2, -trueLength / 2, trueWidth, trueLength)
        ctx.restore()
      } else {
        const radius = emphasised ? 5 : 3

        // A surface ring keeps a mark legible wherever it crosses a heatmap
        // cell or another mark.
        if (this.overlayActive) {
          ctx.beginPath()
          ctx.arc(p.x, p.y, radius + 1.5, 0, Math.PI * 2)
          ctx.fillStyle = '#0b1020'
          ctx.fill()
          ctx.fillStyle = emphasised
            ? '#ffffff'
            : this.options.colorByState
              ? stateColors[e.state as EntityState] ?? '#6b7590'
              : info?.color ?? '#4c8dff'
        }

        ctx.beginPath()
        ctx.arc(p.x, p.y, radius, 0, Math.PI * 2)
        ctx.fill()
      }

      if (emphasised) {
        ctx.strokeStyle = 'rgba(255, 255, 255, 0.85)'
        ctx.lineWidth = 1.5
        ctx.beginPath()
        ctx.arc(p.x, p.y, 12, 0, Math.PI * 2)
        ctx.stroke()
      }
    }

    ctx.restore()
  }

  /**
   * Draws a scale bar.
   *
   * This is the payoff from calibrating the plan. Without it the view is a
   * picture; with it every distance on screen is a real one, and a reader can
   * judge whether a queue is ten metres or a hundred.
   */
  private drawScaleBar(ctx: CanvasRenderingContext2D, width: number, height: number) {
    const targetPixels = Math.min(160, width * 0.25)
    const rawMeters = targetPixels * this.camera.scale
    const meters = niceRound(rawMeters)
    const pixels = meters / this.camera.scale

    const x = 16
    const y = height - 22

    ctx.save()
    ctx.strokeStyle = 'rgba(207, 224, 255, 0.85)'
    ctx.fillStyle = 'rgba(207, 224, 255, 0.85)'
    ctx.lineWidth = 1.5

    ctx.beginPath()
    ctx.moveTo(x, y - 5)
    ctx.lineTo(x, y)
    ctx.lineTo(x + pixels, y)
    ctx.lineTo(x + pixels, y - 5)
    ctx.stroke()

    ctx.font = '11px system-ui, sans-serif'
    ctx.fillText(meters >= 1000 ? `${meters / 1000} km` : `${meters} m`, x + pixels + 8, y + 3)
    ctx.restore()
  }
}

/** Rounds to 1, 2 or 5 times a power of ten, so a scale bar reads "50 m"
 *  rather than "47.38 m". */
function niceRound(value: number): number {
  if (value <= 0) return 1

  const exponent = Math.floor(Math.log10(value))
  const base = Math.pow(10, exponent)
  const f = value / base

  if (f < 1.5) return base
  if (f < 3.5) return 2 * base
  if (f < 7.5) return 5 * base
  return 10 * base
}

function clamp(v: number, min: number, max: number) {
  return Math.min(max, Math.max(min, v))
}
