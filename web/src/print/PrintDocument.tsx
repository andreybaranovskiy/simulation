import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'

/**
 * The print document frame.
 *
 * A report is printed by a headless browser that has to know when the page has
 * finished drawing, so it prints a complete document rather than a half-loaded
 * one. That is what this file coordinates: sections register while their data
 * is in flight, release when it has settled and painted, and the frame raises
 * a global flag once nothing is outstanding. The renderer waits on that flag.
 *
 * The whole scheme no-ops outside a print context, so the same chart components
 * work unchanged on the interactive pages, where there is no gate to register
 * with.
 */

interface Gate {
  register: () => () => void
}

const GateContext = createContext<Gate | null>(null)

/**
 * Holds a section back from "ready" until its data has settled.
 *
 * Pass `done` as false while loading and true once the query has resolved,
 * error included: a failed section should not hang the whole export. Nested
 * charts that fetch their own data use this too, which is why the coordination
 * lives in a context rather than in a lifted list of queries.
 */
export function usePrintGate(done: boolean) {
  const gate = useContext(GateContext)
  const release = useRef<null | (() => void)>(null)

  useEffect(() => {
    if (!gate) return
    // Register once, on mount, so the frame counts this section as outstanding
    // from the first render rather than after its first data arrives.
    release.current = gate.register()
    return () => release.current?.()
  }, [gate])

  useEffect(() => {
    if (done && release.current) {
      release.current()
      release.current = null
    }
  }, [done])
}

/**
 * The frame. It watches the outstanding count and, once it settles at zero,
 * gives the charts two animation frames to paint before raising the flag.
 *
 * A hard fallback raises the flag regardless after a while, so a single wedged
 * query yields a report missing one block rather than no report at all.
 */
export function PrintDocument({
  title,
  children,
  fallbackMs = 20000,
}: {
  title: string
  children: React.ReactNode
  fallbackMs?: number
}) {
  const [pending, setPending] = useState(0)
  const [armed, setArmed] = useState(false)
  const readyRef = useRef(false)

  const register = useCallback(() => {
    // The first registration arms the frame. Until something has registered,
    // a count of zero means "nothing has mounted yet", not "everything is
    // done": a saved report spends its first moments fetching its definition,
    // with no section mounted to hold the gate open, and without this it would
    // print that empty moment.
    setArmed(true)
    setPending((n) => n + 1)
    let released = false
    return () => {
      if (released) return
      released = true
      setPending((n) => n - 1)
    }
  }, [])

  const markReady = useCallback(() => {
    if (readyRef.current) return
    readyRef.current = true
    ;(window as unknown as { __PRINT_READY__?: boolean }).__PRINT_READY__ = true
  }, [])

  // The count reaching zero is necessary but not sufficient: a chart's data has
  // arrived, but the SVG or canvas has not painted yet. Two frames covers the
  // React commit and the browser's own paint.
  useEffect(() => {
    if (!armed || pending > 0) return
    let raf2 = 0
    const raf1 = requestAnimationFrame(() => {
      raf2 = requestAnimationFrame(markReady)
    })
    return () => {
      cancelAnimationFrame(raf1)
      cancelAnimationFrame(raf2)
    }
  }, [armed, pending, markReady])

  useEffect(() => {
    const timer = setTimeout(markReady, fallbackMs)
    return () => clearTimeout(timer)
  }, [markReady, fallbackMs])

  useEffect(() => {
    document.title = title
  }, [title])

  // Reports always render on the dark surface the charts are validated against,
  // whatever theme the viewer chose, so an exported PDF looks the same for
  // everyone and matches how the report was designed.
  useEffect(() => {
    const root = document.documentElement
    const previous = root.getAttribute('data-theme')
    root.setAttribute('data-theme', 'dark')
    return () => {
      if (previous) root.setAttribute('data-theme', previous)
      else root.removeAttribute('data-theme')
    }
  }, [])

  // The value has to keep a stable identity: it is the dependency every
  // section's registration effect watches, and a fresh object each render would
  // make them all unregister and re-register on every state change, which is
  // exactly the churn that stops the pending count from ever settling at zero.
  const value = useMemo(() => ({ register }), [register])

  return (
    <GateContext.Provider value={value}>
      <div className="print-root">{children}</div>
    </GateContext.Provider>
  )
}
