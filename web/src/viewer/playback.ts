import type { Level, Manifest } from '@/api/types'
import { decodeChunk, type Chunk, type Live } from './chunk'

/**
 * Playback turns a run's chunks into "where is everything at time t".
 *
 * The hard part is not interpolation, it is only fetching what is needed. A
 * week-long run is a thousand chunks; a viewer that loaded them all would stall
 * for a minute and then run out of memory. So chunks are fetched on demand,
 * the neighbours of the current position are prefetched so scrubbing stays
 * smooth, and a bounded cache evicts whatever is furthest from where the user
 * is looking.
 */

/** One entity's resolved state at a moment. */
export interface EntityAt {
  id: number
  cls: number
  state: number
  x: number
  y: number
  z: number
  /** Heading in radians, derived from the direction of travel. A model that
   *  declares no rotation still gets vehicles pointing the right way. */
  heading: number
  /** True while the entity is actually moving, which the 2D view uses to
   *  distinguish a queue from a road. */
  moving: boolean
}

/** How much of the run is loaded, for the progress indicator. */
export interface LoadState {
  ready: boolean
  loading: boolean
  error?: string
  loadedChunks: number
  totalChunks: number
}

const MAX_CACHED_CHUNKS = 24

export class Playback {
  readonly manifest: Manifest

  private readonly projectId: string
  private readonly runId: string

  private level: Level
  private cache = new Map<number, Chunk>()
  /** In-flight fetches, so scrubbing across a chunk twice does not fetch it twice. */
  private pending = new Map<number, Promise<Chunk>>()
  /** Insertion order for eviction, most recently used last. */
  private recency: number[] = []

  private listeners = new Set<(state: LoadState) => void>()
  private state: LoadState = { ready: false, loading: false, loadedChunks: 0, totalChunks: 0 }

  constructor(projectId: string, runId: string, manifest: Manifest) {
    this.projectId = projectId
    this.runId = runId
    this.manifest = manifest
    this.level = (manifest.levels ?? [])[0] ?? {
      index: 0, stride: 1, chunkSeconds: 60, chunkCount: 0, bytes: 0, peakLive: 0,
    }
    this.state.totalChunks = this.level.chunkCount
  }

  /** The levels available, coarsest last. */
  get levels(): Level[] {
    return this.manifest.levels ?? []
  }

  get currentLevel(): Level {
    return this.level
  }

  /**
   * Switches fidelity. Higher levels show fewer entities, which is how a run
   * with thousands on screen stays interactive.
   */
  setLevel(index: number) {
    const next = (this.manifest.levels ?? []).find((l) => l.index === index)
    if (!next || next.index === this.level.index) return

    this.level = next
    this.cache.clear()
    this.pending.clear()
    this.recency = []
    this.state = { ...this.state, ready: false, loadedChunks: 0, totalChunks: next.chunkCount }
    this.emit()
  }

  /**
   * Picks the coarsest level that still shows enough detail, given how many
   * entities a browser can draw at once.
   */
  autoLevel(maxOnScreen = 4000): Level {
    const levels = this.manifest.levels ?? []
    for (const level of levels) {
      if (level.peakLive <= maxOnScreen) return level
    }
    return levels[levels.length - 1] ?? this.level
  }

  subscribe(fn: (state: LoadState) => void): () => void {
    this.listeners.add(fn)
    fn(this.state)
    return () => this.listeners.delete(fn)
  }

  private emit() {
    for (const fn of this.listeners) fn(this.state)
  }

  private chunkIndexFor(time: number): number {
    const { startTime } = this.manifest
    const idx = Math.floor((time - startTime) / this.level.chunkSeconds)
    return Math.max(0, Math.min(idx, this.level.chunkCount - 1))
  }

  /**
   * Makes sure the chunk covering a moment is loaded, along with its
   * neighbours. Prefetching one either side is what keeps playback from
   * stuttering every time the clock crosses a boundary.
   */
  async ensureLoaded(time: number): Promise<void> {
    const index = this.chunkIndexFor(time)

    await this.load(index)

    // Neighbours are fetched without being waited on: they are an optimisation,
    // and blocking on them would defeat the point.
    void this.load(index + 1)
    void this.load(index - 1)
  }

  private async load(index: number): Promise<Chunk | undefined> {
    if (index < 0 || index >= this.level.chunkCount) return undefined

    const cached = this.cache.get(index)
    if (cached) {
      this.touch(index)
      return cached
    }

    const inFlight = this.pending.get(index)
    if (inFlight) return inFlight

    const promise = this.fetchChunk(index)
    this.pending.set(index, promise)

    try {
      const chunk = await promise
      this.cache.set(index, chunk)
      this.touch(index)
      this.evict()

      this.state = {
        ...this.state,
        ready: true,
        loading: this.pending.size > 1,
        loadedChunks: this.cache.size,
        error: undefined,
      }
      this.emit()
      return chunk
    } catch (err) {
      this.state = {
        ...this.state,
        loading: false,
        error: err instanceof Error ? err.message : 'Could not load the playback data.',
      }
      this.emit()
      return undefined
    } finally {
      this.pending.delete(index)
    }
  }

  private async fetchChunk(index: number): Promise<Chunk> {
    const path = `frames/lod${this.level.index}/${String(index).padStart(5, '0')}.bin`
    const url = `/api/projects/${this.projectId}/runs/${this.runId}/artifacts/${path}`

    const response = await fetch(url, { credentials: 'same-origin' })
    if (!response.ok) {
      throw new Error(`Could not load playback chunk ${index} (${response.status}).`)
    }

    return decodeChunk(await response.arrayBuffer())
  }

  private touch(index: number) {
    const at = this.recency.indexOf(index)
    if (at !== -1) this.recency.splice(at, 1)
    this.recency.push(index)
  }

  /** Drops the least recently used chunks, which are the ones furthest from
   *  wherever the user is looking. */
  private evict() {
    while (this.recency.length > MAX_CACHED_CHUNKS) {
      const oldest = this.recency.shift()
      if (oldest !== undefined) this.cache.delete(oldest)
    }
  }

  /**
   * Resolves every entity's position at a moment.
   *
   * This is the algorithm the format is designed around. Each entity starts
   * from the chunk's opening snapshot, or from a spawn inside the window, then
   * walks its chain of spans. A span carries only its endpoint because it
   * begins where the previous one ended, so the walk stops at the first span
   * still in progress at the requested time.
   */
  at(time: number): EntityAt[] {
    const index = this.chunkIndexFor(time)
    const chunk = this.cache.get(index)
    if (!chunk) return []

    // Start from the entities already present when the window opened.
    const current = new Map<number, Live>()
    for (const l of chunk.live) {
      current.set(l.id, { ...l })
    }

    // Entities that appeared during the window, up to the requested moment.
    for (const s of chunk.spawns) {
      if (s.time > time) continue
      current.set(s.id, {
        id: s.id, cls: s.cls, state: 0,
        spanStart: s.time, sx: s.x, sy: s.y, sz: s.z,
        spanEnd: s.time, ex: s.x, ey: s.y, ez: s.z,
      })
    }

    // Advance each entity through its spans. Spans are in emission order,
    // which for one entity is chronological.
    for (const span of chunk.spans) {
      const entity = current.get(span.id)
      if (!entity) continue

      // Still part-way through the current span, so later ones have not begun.
      if (time < entity.spanEnd) continue

      entity.spanStart = entity.spanEnd
      entity.sx = entity.ex
      entity.sy = entity.ey
      entity.sz = entity.ez
      entity.spanEnd = span.endTime
      entity.ex = span.x
      entity.ey = span.y
      entity.ez = span.z
    }

    for (const change of chunk.states) {
      if (change.time > time) continue
      const entity = current.get(change.id)
      if (entity) entity.state = change.state
    }

    for (const exit of chunk.exits) {
      if (exit.time <= time) current.delete(exit.id)
    }

    const out: EntityAt[] = []
    for (const e of current.values()) {
      out.push(resolve(e, time))
    }
    return out
  }

  /** How much of the run's playback data is cached, as a fraction. */
  get loaded(): number {
    if (this.level.chunkCount === 0) return 1
    return Math.min(1, this.cache.size / Math.min(this.level.chunkCount, MAX_CACHED_CHUNKS))
  }
}

/** Interpolates one entity along its current span. */
function resolve(e: Live, time: number): EntityAt {
  const duration = e.spanEnd - e.spanStart

  let x = e.ex
  let y = e.ey
  let z = e.ez
  let moving = false

  if (duration > 0 && time < e.spanEnd) {
    const f = Math.max(0, (time - e.spanStart) / duration)
    x = e.sx + (e.ex - e.sx) * f
    y = e.sy + (e.ey - e.sy) * f
    z = e.sz + (e.ez - e.sz) * f
    moving = true
  }

  const dx = e.ex - e.sx
  const dy = e.ey - e.sy

  // Below a few centimetres the direction is noise, not a heading, and letting
  // it through makes stationary vehicles spin.
  const heading = Math.hypot(dx, dy) > 0.05 ? Math.atan2(dx, dy) : 0

  if (Math.hypot(dx, dy) <= 0.05) moving = false

  return { id: e.id, cls: e.cls, state: e.state, x, y, z, heading, moving }
}
