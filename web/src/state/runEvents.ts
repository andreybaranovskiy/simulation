import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { RunEvent } from '@/api/types'

/**
 * Subscribes to a project's live run progress.
 *
 * Server-sent events rather than polling: a run that takes twelve seconds
 * deserves a progress bar that moves, and polling fast enough to look live
 * would mean a query every few hundred milliseconds per open tab.
 *
 * EventSource reconnects on its own after a dropped connection, which is the
 * other reason to prefer it here. When it does, the server replays the current
 * state of every unfinished run, so nothing is missed by being away.
 */
export function useRunEvents(projectId: string | undefined) {
  const queryClient = useQueryClient()
  const [events, setEvents] = useState<Map<string, RunEvent>>(new Map())
  const [connected, setConnected] = useState(false)

  // Held in a ref so the effect does not re-subscribe whenever a run reports.
  const latest = useRef(new Map<string, RunEvent>())

  useEffect(() => {
    if (!projectId) return

    const source = new EventSource(api.events.url(projectId), { withCredentials: true })
    let finishTimer: number | undefined

    source.addEventListener('open', () => setConnected(true))

    source.addEventListener('run', (message) => {
      let event: RunEvent
      try {
        event = JSON.parse((message as MessageEvent).data) as RunEvent
      } catch {
        return
      }

      latest.current.set(event.runId, event)
      setEvents(new Map(latest.current))

      if (event.type === 'finished') {
        // A finished run changes the lists, the KPI rows and the scenario
        // summaries. Refetching immediately would race the server's own final
        // write, so it waits a beat.
        window.clearTimeout(finishTimer)
        finishTimer = window.setTimeout(() => {
          void queryClient.invalidateQueries({ queryKey: ['runs', projectId] })
          void queryClient.invalidateQueries({ queryKey: ['scenarios', projectId] })
        }, 250)
      }
    })

    source.addEventListener('error', () => {
      // EventSource retries by itself; this only reflects the state so the UI
      // can say it is reconnecting rather than silently going stale.
      setConnected(false)
    })

    return () => {
      window.clearTimeout(finishTimer)
      source.close()
      setConnected(false)
    }
  }, [projectId, queryClient])

  return { events, connected }
}

/** Merges a run's stored progress with anything newer from the live stream. */
export function mergeProgress(
  stored: { status: string; progress: number; entityCount: number; simTime: number },
  live: RunEvent | undefined,
) {
  if (!live) return stored

  return {
    status: live.status ?? stored.status,
    progress: live.progress ?? stored.progress,
    entityCount: live.entities ?? stored.entityCount,
    simTime: live.simTime ?? stored.simTime,
  }
}
