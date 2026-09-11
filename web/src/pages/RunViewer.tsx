import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { Manifest, SitePlan } from '@/api/types'
import { EntityState, stateColors, stateNames } from '@/api/types'
import { Playback, type EntityAt } from '@/viewer/playback'
import { Scene3D } from '@/viewer/scene3d'
import { DEFAULT_2D_OPTIONS, Scene2D } from '@/viewer/scene2d'
import { formatSimTime, useTimeline } from '@/state/timeline'

type Layout = 'split' | '3d' | '2d'

export function RunViewer() {
  const { projectId, runId } = useParams<{ projectId: string; runId: string }>()

  const run = useQuery({
    queryKey: ['run', projectId, runId],
    queryFn: () => api.runs.get(projectId!, runId!),
    enabled: !!projectId && !!runId,
  })

  const manifest = useQuery({
    queryKey: ['manifest', projectId, runId],
    queryFn: () => api.runs.manifest(projectId!, runId!),
    enabled: !!projectId && !!runId && run.data?.status === 'done',
    // Artifacts are immutable once a run is done, so this never needs refetching.
    staleTime: Infinity,
  })

  const plans = useQuery({
    queryKey: ['plans', projectId],
    queryFn: () => api.plans.list(projectId!),
    enabled: !!projectId,
  })

  if (!projectId || !runId) return null

  if (run.isLoading || (run.data?.status === 'done' && manifest.isLoading)) {
    return (
      <div className="loading-page">
        <span className="spinner" />
        <span>Loading the run</span>
      </div>
    )
  }

  if (run.data && run.data.status !== 'done') {
    return (
      <div className="page">
        <div className="page-narrow">
          <div className="banner warn">
            This run is {run.data.status}. There is nothing to view until it finishes.
            {run.data.error && <div style={{ marginTop: 6 }}>{run.data.error}</div>}
          </div>
          <Link to={`/projects/${projectId}`}>Back to the project</Link>
        </div>
      </div>
    )
  }

  if (manifest.error || !manifest.data) {
    return (
      <div className="page">
        <div className="page-narrow">
          <div className="banner error">
            {manifest.error instanceof Error
              ? manifest.error.message
              : 'Could not load this run’s playback data.'}
          </div>
          <Link to={`/projects/${projectId}`}>Back to the project</Link>
        </div>
      </div>
    )
  }

  return (
    <ViewerStage
      projectId={projectId}
      runId={runId}
      manifest={manifest.data}
      plans={plans.data ?? []}
      scenarioName={run.data?.scenarioName}
    />
  )
}

function ViewerStage({
  projectId,
  runId,
  manifest,
  plans,
  scenarioName,
}: {
  projectId: string
  runId: string
  manifest: Manifest
  plans: SitePlan[]
  scenarioName?: string
}) {
  const pane3d = useRef<HTMLDivElement>(null)
  const canvas2d = useRef<HTMLCanvasElement>(null)

  const scene3d = useRef<Scene3D | null>(null)
  const scene2d = useRef<Scene2D | null>(null)
  const playback = useRef<Playback | null>(null)

  const [layout, setLayout] = useState<Layout>('split')
  const [colorByState, setColorByState] = useState(true)
  const [showPlan, setShowPlan] = useState(true)
  const [showTrails, setShowTrails] = useState(true)
  const [levelIndex, setLevelIndex] = useState(0)
  const [planId, setPlanId] = useState<string>('')
  const [loadError, setLoadError] = useState<string | null>(null)
  const [inspected, setInspected] = useState<EntityAt | null>(null)

  const timeline = useTimeline()

  // Entities resolved for the current frame, kept in a ref so the render loop
  // does not go through React state sixty times a second.
  const frameEntities = useRef<EntityAt[]>([])

  const selectedPlan = useMemo(() => plans.find((p) => p.id === planId), [plans, planId])

  // ---- set up the playback engine and both scenes -------------------------
  useEffect(() => {
    const engine = new Playback(projectId, runId, manifest)
    playback.current = engine

    // Pick a level the browser can actually draw. A run with 60,000 entities
    // on screen at once would stall at level 0 whatever the machine.
    const chosen = engine.autoLevel()
    engine.setLevel(chosen.index)
    setLevelIndex(chosen.index)

    timeline.setRange(manifest.startTime, manifest.endTime)

    const unsubscribe = engine.subscribe((state) => setLoadError(state.error ?? null))
    void engine.ensureLoaded(manifest.startTime)

    return () => {
      unsubscribe()
      playback.current = null
    }
  }, [projectId, runId, manifest])

  useEffect(() => {
    if (!pane3d.current) return

    const scene = new Scene3D(pane3d.current)
    scene.build(manifest)
    scene3d.current = scene

    return () => {
      scene.dispose()
      scene3d.current = null
    }
  }, [manifest])

  useEffect(() => {
    if (!canvas2d.current) return

    const scene = new Scene2D(canvas2d.current)
    scene.build(manifest)
    scene.setOptions({ ...DEFAULT_2D_OPTIONS })
    scene2d.current = scene

    return () => {
      scene2d.current = null
    }
  }, [manifest])

  // ---- keep the options in sync ------------------------------------------
  useEffect(() => {
    scene3d.current?.setOptions({ colorByState })
    scene2d.current?.setOptions({ colorByState, showPlan, showTrails })
  }, [colorByState, showPlan, showTrails])

  useEffect(() => {
    scene3d.current?.setHiddenClasses(timeline.hiddenClasses)
    scene2d.current?.setHiddenClasses(timeline.hiddenClasses)
  }, [timeline.hiddenClasses])

  useEffect(() => {
    scene3d.current?.setFollow(timeline.selectedId)
  }, [timeline.selectedId])

  // ---- load the plan into both views -------------------------------------
  useEffect(() => {
    if (!selectedPlan) return
    const url = api.assets.contentUrl(projectId, selectedPlan.assetId)

    void scene2d.current?.setPlan(selectedPlan, url)
    void scene3d.current?.loadPlanImage(url, selectedPlan).catch(() => undefined)
  }, [selectedPlan, projectId])

  // ---- resize -------------------------------------------------------------
  useEffect(() => {
    const onResize = () => {
      scene3d.current?.resize()
      scene2d.current?.resize()
    }
    window.addEventListener('resize', onResize)

    // The layout toggle changes pane sizes without a window resize.
    const timer = window.setTimeout(onResize, 50)

    return () => {
      window.removeEventListener('resize', onResize)
      window.clearTimeout(timer)
    }
  }, [layout])

  // ---- the render loop ----------------------------------------------------
  useEffect(() => {
    let frame = 0
    let lastFrameTime = performance.now()

    const tick = (now: number) => {
      frame = requestAnimationFrame(tick)

      const deltaReal = Math.min((now - lastFrameTime) / 1000, 0.25)
      lastFrameTime = now

      const state = useTimeline.getState()

      if (state.playing) {
        state.advance(deltaReal * state.speed)
      }

      const engine = playback.current
      if (!engine) return

      void engine.ensureLoaded(state.time)

      const entities = engine.at(state.time)
      frameEntities.current = entities

      scene3d.current?.update(entities, state.selectedId, state.hoveredId)
      scene3d.current?.render()

      scene2d.current?.render(entities, state.time, state.selectedId, state.hoveredId)
    }

    frame = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(frame)
  }, [])

  // ---- keyboard shortcuts -------------------------------------------------
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement
      if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT') {
        return
      }

      const state = useTimeline.getState()

      switch (e.key) {
        case ' ':
          e.preventDefault()
          state.togglePlay()
          break
        case 'ArrowRight':
          state.setTime(state.time + (e.shiftKey ? 300 : 30))
          break
        case 'ArrowLeft':
          state.setTime(state.time - (e.shiftKey ? 300 : 30))
          break
        case 'Home':
          state.setTime(state.startTime)
          break
        case 'End':
          state.setTime(state.endTime)
          break
        case 'l':
          state.toggleLoop()
          break
        case 'c':
          setColorByState((v) => !v)
          break
        case 'Escape':
          state.select(null)
          setInspected(null)
          break
      }
    }

    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // ---- the selection inspector -------------------------------------------
  useEffect(() => {
    if (timeline.selectedId === null) {
      setInspected(null)
      return
    }
    const found = frameEntities.current.find((e) => e.id === timeline.selectedId)
    setInspected(found ?? null)
  }, [timeline.selectedId, timeline.time])

  const level = playback.current?.currentLevel

  return (
    <div className="viewer">
      <div className="viewer-toolbar">
        <Link to={`/projects/${projectId}`} className="btn ghost small">
          ← Back
        </Link>

        <strong style={{ fontSize: 13 }}>{scenarioName ?? manifest.modelName}</strong>

        <span className="faint" style={{ fontSize: 12 }}>
          {manifest.counts.entities.toLocaleString()} entities · seed {manifest.seed}
        </span>

        <div className="spacer" />

        <div className="row" style={{ gap: 4 }}>
          {(['split', '3d', '2d'] as Layout[]).map((option) => (
            <button
              key={option}
              className={`btn small toggle ${layout === option ? 'on' : ''}`}
              onClick={() => setLayout(option)}
            >
              {option === 'split' ? 'Both' : option === '3d' ? '3D' : 'Plan'}
            </button>
          ))}
        </div>

        <button
          className={`btn small toggle ${colorByState ? 'on' : ''}`}
          onClick={() => setColorByState((v) => !v)}
          title="Colour entities by what they are doing, rather than by type"
        >
          Colour by state
        </button>

        <button
          className={`btn small toggle ${showTrails ? 'on' : ''}`}
          onClick={() => setShowTrails((v) => !v)}
        >
          Trails
        </button>

        {plans.length > 0 && (
          <>
            <select
              value={planId}
              onChange={(e) => setPlanId(e.target.value)}
              style={{ width: 'auto', fontSize: 12, padding: '5px 8px' }}
            >
              <option value="">No plan</option>
              {plans.map((plan) => (
                <option key={plan.id} value={plan.id}>
                  {plan.name}
                </option>
              ))}
            </select>

            {selectedPlan && (
              <button
                className={`btn small toggle ${showPlan ? 'on' : ''}`}
                onClick={() => setShowPlan((v) => !v)}
              >
                Show plan
              </button>
            )}
          </>
        )}

        {(manifest.levels ?? []).length > 1 && (
          <select
            value={levelIndex}
            onChange={(e) => {
              const index = Number(e.target.value)
              setLevelIndex(index)
              playback.current?.setLevel(index)
            }}
            style={{ width: 'auto', fontSize: 12, padding: '5px 8px' }}
            title="Fewer entities draw faster on a crowded run"
          >
            {(manifest.levels ?? []).map((l) => (
              <option key={l.index} value={l.index}>
                {l.stride === 1 ? 'All entities' : `1 in ${l.stride}`}
              </option>
            ))}
          </select>
        )}
      </div>

      {loadError && <div className="banner error" style={{ margin: 10 }}>{loadError}</div>}

      <div className={`viewer-body ${layout === 'split' ? 'split' : ''}`}>
        <div
          className="viewer-pane"
          ref={pane3d}
          style={{ display: layout === '2d' ? 'none' : undefined }}
          onClick={(e) => {
            const id = scene3d.current?.pick(e.clientX, e.clientY) ?? null
            useTimeline.getState().select(id)
          }}
        >
          <span className="pane-label">3D</span>
          {layout !== '2d' && <Legend manifest={manifest} />}
        </div>

        <div
          className="viewer-pane"
          style={{ display: layout === '3d' ? 'none' : undefined }}
          onWheel={(e) => {
            const rect = e.currentTarget.getBoundingClientRect()
            scene2d.current?.zoomAt(
              e.clientX - rect.left,
              e.clientY - rect.top,
              e.deltaY < 0 ? 1.12 : 1 / 1.12,
            )
          }}
          onPointerMove={(e) => {
            if (e.buttons === 1) {
              scene2d.current?.pan(e.movementX, e.movementY)
              return
            }
            const rect = e.currentTarget.getBoundingClientRect()
            const id = scene2d.current?.pick(
              frameEntities.current,
              e.clientX - rect.left,
              e.clientY - rect.top,
            )
            useTimeline.getState().hover(id ?? null)
          }}
          onPointerLeave={() => useTimeline.getState().hover(null)}
          onClick={(e) => {
            const rect = e.currentTarget.getBoundingClientRect()
            const id = scene2d.current?.pick(
              frameEntities.current,
              e.clientX - rect.left,
              e.clientY - rect.top,
            )
            useTimeline.getState().select(id ?? null)
          }}
        >
          <span className="pane-label">Plan</span>
          <canvas ref={canvas2d} />
          {layout === '2d' && <Legend manifest={manifest} />}
          {inspected && <Inspector entity={inspected} manifest={manifest} />}
        </div>
      </div>

      <TimelineBar manifest={manifest} level={level?.stride ?? 1} />
    </div>
  )
}

function Legend({ manifest }: { manifest: Manifest }) {
  const hidden = useTimeline((s) => s.hiddenClasses)
  const toggle = useTimeline((s) => s.toggleClass)

  return (
    <div className="legend">
      {(manifest.classes ?? []).map((info, index) => (
        <button
          key={info.id}
          className={`chip ${hidden.has(index) ? 'off' : ''}`}
          onClick={() => toggle(index)}
          title={`${info.count.toLocaleString()} in this run. Click to hide.`}
        >
          <span className="swatch" style={{ background: info.color }} />
          {info.label}
        </button>
      ))}

      <span className="chip" style={{ cursor: 'default', opacity: 0.8 }}>
        {Object.entries(stateColors).map(([state, color]) => (
          <span key={state} className="swatch" style={{ background: color }} title={stateNames[Number(state) as EntityState]} />
        ))}
        States
      </span>
    </div>
  )
}

function Inspector({ entity, manifest }: { entity: EntityAt; manifest: Manifest }) {
  const info = (manifest.classes ?? [])[entity.cls]

  return (
    <div className="inspector">
      <div className="row" style={{ marginBottom: 8 }}>
        <span className="swatch" style={{ background: info?.color, width: 10, height: 10, borderRadius: 2 }} />
        <strong style={{ fontSize: 12 }}>{info?.label ?? 'Entity'} #{entity.id}</strong>
      </div>

      <dl>
        <dt>State</dt>
        <dd style={{ color: stateColors[entity.state as EntityState] }}>
          {stateNames[entity.state as EntityState] ?? 'Unknown'}
        </dd>

        <dt>Position</dt>
        <dd>
          {entity.x.toFixed(0)}, {entity.y.toFixed(0)} m
        </dd>

        <dt>Moving</dt>
        <dd>{entity.moving ? 'Yes' : 'No'}</dd>
      </dl>

      <button
        className="btn ghost small"
        style={{ width: '100%', marginTop: 10 }}
        onClick={() => useTimeline.getState().select(null)}
      >
        Clear selection
      </button>
    </div>
  )
}

function TimelineBar({ manifest, level }: { manifest: Manifest; level: number }) {
  const { time, playing, speed, loop, startTime, endTime } = useTimeline()
  const { togglePlay, setTime, setSpeed, toggleLoop } = useTimeline()

  return (
    <div className="timeline-bar">
      <button className="btn small" onClick={togglePlay} style={{ minWidth: 74 }}>
        {playing ? '⏸ Pause' : '▶ Play'}
      </button>

      <button className="btn ghost small" onClick={() => setTime(startTime)} title="Home">
        ⟲
      </button>

      <input
        type="range"
        min={startTime}
        max={endTime}
        step={Math.max((endTime - startTime) / 2000, 0.01)}
        value={time}
        onChange={(e) => setTime(Number(e.target.value))}
      />

      <span className="clock">
        {formatSimTime(time)} / {formatSimTime(endTime)}
      </span>

      <div className="row" style={{ gap: 6 }}>
        <span className="faint" style={{ fontSize: 11 }}>
          Speed
        </span>
        <select
          value={speed}
          onChange={(e) => setSpeed(Number(e.target.value))}
          style={{ width: 'auto', fontSize: 12, padding: '4px 6px' }}
        >
          {[1, 10, 30, 60, 120, 300, 600, 1800].map((value) => (
            <option key={value} value={value}>
              {value}×
            </option>
          ))}
        </select>
      </div>

      <button className={`btn small toggle ${loop ? 'on' : ''}`} onClick={toggleLoop}>
        Loop
      </button>

      {level > 1 && (
        <span className="faint" style={{ fontSize: 11 }} title="A subset is drawn so the view stays responsive">
          showing 1 in {level}
        </span>
      )}

      {manifest.warnings && manifest.warnings.length > 0 && (
        <span className="pill" style={{ color: 'var(--warn)' }} title={manifest.warnings.join('\n')}>
          {manifest.warnings.length} note{manifest.warnings.length > 1 ? 's' : ''}
        </span>
      )}
    </div>
  )
}
