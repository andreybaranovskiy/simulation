import { create } from 'zustand'

/**
 * The playback clock, shared by every view of a run.
 *
 * Both viewers reading one store is what makes them synchronised rather than
 * merely similar. Neither owns the clock; a scrub in the 2D plan and a scrub
 * on the timeline are the same action.
 */
export interface TimelineState {
  /** Current position in simulated seconds. */
  time: number
  startTime: number
  endTime: number

  playing: boolean
  /** Simulated seconds per real second. */
  speed: number
  loop: boolean

  /** The entity the user has selected, followed in 3D and highlighted in 2D. */
  selectedId: number | null
  /** The entity under the pointer, highlighted in both views at once. */
  hoveredId: number | null

  /** Entity classes hidden via the legend. */
  hiddenClasses: Set<number>

  setRange: (startTime: number, endTime: number) => void
  setTime: (time: number) => void
  advance: (deltaSeconds: number) => void
  play: () => void
  pause: () => void
  togglePlay: () => void
  setSpeed: (speed: number) => void
  toggleLoop: () => void
  select: (id: number | null) => void
  hover: (id: number | null) => void
  toggleClass: (cls: number) => void
  reset: () => void
}

export const useTimeline = create<TimelineState>((set, get) => ({
  time: 0,
  startTime: 0,
  endTime: 0,
  playing: false,
  speed: 60,
  loop: false,
  selectedId: null,
  hoveredId: null,
  hiddenClasses: new Set<number>(),

  setRange: (startTime, endTime) =>
    set({ startTime, endTime, time: startTime, playing: false }),

  setTime: (time) => {
    const { startTime, endTime } = get()
    set({ time: clamp(time, startTime, endTime) })
  },

  /**
   * Advances the clock by one frame's worth of simulated time.
   *
   * Reaching the end pauses rather than sticking, unless looping is on. A
   * viewer that silently stops at the end with the play button still lit is a
   * small lie about what it is doing.
   */
  advance: (deltaSeconds) => {
    const { time, startTime, endTime, loop, playing } = get()
    if (!playing) return

    const next = time + deltaSeconds

    if (next >= endTime) {
      if (loop) {
        set({ time: startTime })
      } else {
        set({ time: endTime, playing: false })
      }
      return
    }
    set({ time: next })
  },

  play: () => {
    const { time, endTime, startTime } = get()
    // Pressing play at the end restarts rather than doing nothing.
    set({ playing: true, time: time >= endTime ? startTime : time })
  },

  pause: () => set({ playing: false }),
  togglePlay: () => (get().playing ? get().pause() : get().play()),

  setSpeed: (speed) => set({ speed: clamp(speed, 0.1, 10000) }),
  toggleLoop: () => set({ loop: !get().loop }),

  select: (id) => set({ selectedId: id }),
  hover: (id) => set({ hoveredId: id }),

  toggleClass: (cls) => {
    const hidden = new Set(get().hiddenClasses)
    if (hidden.has(cls)) hidden.delete(cls)
    else hidden.add(cls)
    set({ hiddenClasses: hidden })
  },

  reset: () =>
    set({
      time: get().startTime,
      playing: false,
      selectedId: null,
      hoveredId: null,
      hiddenClasses: new Set<number>(),
    }),
}))

function clamp(v: number, min: number, max: number) {
  if (Number.isNaN(v)) return min
  return Math.min(max, Math.max(min, v))
}

/** Formats simulated seconds the way a planner reads a clock. */
export function formatSimTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '0:00'

  const total = Math.floor(seconds)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60

  if (h > 0) {
    return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
  }
  return `${m}:${String(s).padStart(2, '0')}`
}

/** Formats a duration in words, for KPI values and summaries. */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds)) return '—'
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)} s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min ${Math.round(seconds % 60)} s`

  const hours = Math.floor(seconds / 3600)
  const minutes = Math.round((seconds % 3600) / 60)
  return `${hours} h ${minutes} min`
}
