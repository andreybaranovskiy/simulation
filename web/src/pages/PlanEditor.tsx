import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { SimModel, SitePlan } from '@/api/types'

/**
 * The plan layout editor.
 *
 * Elements are not painted onto the drawing; they have real coordinates in
 * metres, and the calibrated plan is a backdrop those coordinates land on. So
 * this editor drags the coordinates, not the picture: a node moved here writes
 * a new x and y into the model, and the plan stays exactly where its
 * calibration put it. The numbers are always visible and editable, because the
 * drag is a convenience over the numbers, not a replacement for them.
 *
 * Only a spec model has geometry to arrange. A template is a generator, so the
 * editor offers to resolve it into an editable copy first, leaving the original
 * that scenarios reference untouched.
 */

interface Node {
  id: string
  label?: string
  x: number
  y: number
  z?: number
}
interface Link {
  from: string
  to: string
  bidirectional?: boolean
}
interface Zone {
  id: string
  label?: string
  x: number
  y: number
  width: number
  height: number
  color?: string
}
type DistKind = 'constant' | 'uniform' | 'normal' | 'exponential' | 'triangular' | 'lognormal' | 'empirical'
interface Dist {
  distribution?: DistKind
  value?: number
  min?: number
  max?: number
  mean?: number
  sd?: number
  mode?: number
  values?: number[]
  weights?: number[]
}
interface QueueSpec {
  discipline?: 'fifo' | 'lifo' | 'priority'
  capacity?: number
  maxWait?: number
}
interface Shift {
  label?: string
  start: number
  end: number
  days?: number[]
}
interface Failure {
  uptime?: Dist
  repair?: Dist
}
interface Resource {
  id: string
  label?: string
  node?: string
  capacity?: number
  service?: Dist
  queue?: QueueSpec
  shifts?: Shift[]
  failure?: Failure | null
  [key: string]: unknown
}
type StepType = 'travel' | 'use' | 'seize' | 'release' | 'delay' | 'branch' | 'exit'
interface Branch {
  weight: number
  label?: string
  steps?: Step[]
  [key: string]: unknown
}
interface Step {
  type: StepType
  label?: string
  to?: string
  resource?: string
  duration?: Dist
  branches?: Branch[]
  [key: string]: unknown
}
interface Route {
  id: string
  label?: string
  source?: string
  steps?: Step[]
  [key: string]: unknown
}
interface EntityType {
  id: string
  label?: string
}
interface Source {
  id: string
  label?: string
  entity?: string
  node?: string
  arrival?: Dist
  batch?: Dist
  start?: number
  stop?: number
  limit?: number
  route?: string
  [key: string]: unknown
}

// The spec is kept whole and only its geometry and operating parameters are
// touched, so saving round trips everything the editor does not understand back
// unchanged.
interface SpecModel {
  nodes?: Node[]
  links?: Link[]
  zones?: Zone[]
  resources?: Resource[]
  routes?: Route[]
  sources?: Source[]
  entityTypes?: EntityType[]
  [key: string]: unknown
}

type Tool = 'select' | 'node' | 'zone' | 'link'
type Selection = { kind: 'node' | 'zone'; id: string } | null

interface Camera {
  scale: number // metres per pixel
  cx: number
  cy: number
}

export function PlanEditor() {
  const { projectId, modelId } = useParams<{ projectId: string; modelId: string }>()
  const [params] = useSearchParams()
  const planParam = params.get('plan') ?? ''

  const model = useQuery({
    queryKey: ['model', projectId, modelId],
    queryFn: () => api.models.get(projectId!, modelId!),
    enabled: !!projectId && !!modelId,
  })

  const plans = useQuery({
    queryKey: ['plans', projectId],
    queryFn: () => api.plans.list(projectId!),
    enabled: !!projectId,
  })

  if (!projectId || !modelId) return null

  if (model.isLoading) {
    return (
      <div className="loading-page" style={{ height: 300 }}>
        <span className="spinner" />
      </div>
    )
  }

  if (model.data && model.data.source !== 'spec') {
    return <Materialise projectId={projectId} model={model.data} />
  }

  if (!model.data) {
    return (
      <div className="page">
        <div className="empty">
          <h2>That model was not found</h2>
        </div>
      </div>
    )
  }

  return (
    <Editor
      projectId={projectId}
      model={model.data}
      plans={plans.data ?? []}
      initialPlanId={planParam}
    />
  )
}

/** Offered when a template is opened: it has no geometry to drag until it is
 *  resolved into a concrete spec. */
function Materialise({ projectId, model }: { projectId: string; model: SimModel }) {
  const navigate = useNavigate()
  const create = useMutation({
    mutationFn: () => api.models.makeEditable(projectId, model.id),
    onSuccess: (editable) => navigate(`/projects/${projectId}/models/${editable.id}/layout`),
  })

  return (
    <div className="page">
      <div className="page-narrow">
        <div className="empty">
          <h2>Arrange this layout</h2>
          <p style={{ maxWidth: '56ch', margin: '0 auto 18px' }}>
            <strong>{model.name}</strong> is generated from a template, so it has no fixed layout to
            drag. Create an editable copy to arrange its nodes and zones on a plan. The original is
            left as it is, so scenarios that use it keep working.
          </p>
          <button className="btn primary" onClick={() => create.mutate()} disabled={create.isPending}>
            {create.isPending && <span className="spinner" />}
            Create an editable copy
          </button>
          <div style={{ marginTop: 14 }}>
            <Link to={`/projects/${projectId}`}>Back to the project</Link>
          </div>
        </div>
      </div>
    </div>
  )
}

const NODE_R = 6
const HANDLE = 9

function Editor({
  projectId,
  model,
  plans,
  initialPlanId,
}: {
  projectId: string
  model: SimModel
  plans: SitePlan[]
  initialPlanId: string
}) {
  const queryClient = useQueryClient()
  const paneRef = useRef<HTMLDivElement | null>(null)
  const [size, setSize] = useState({ w: 800, h: 600 })

  const [spec, setSpec] = useState<SpecModel>(() => (model.spec as SpecModel) ?? {})
  const [camera, setCamera] = useState<Camera>({ scale: 0.5, cx: 0, cy: 0 })
  const [tool, setTool] = useState<Tool>('select')
  const [selection, setSelection] = useState<Selection>(null)
  const [linkFrom, setLinkFrom] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)
  const [planId, setPlanId] = useState(initialPlanId || plans[0]?.id || '')

  const plan = useMemo(() => plans.find((p) => p.id === planId) ?? null, [plans, planId])
  const [planImage, setPlanImage] = useState<HTMLImageElement | null>(null)

  const [inspectorTab, setInspectorTab] = useState<'element' | 'sources' | 'routes'>('element')

  const nodes = spec.nodes ?? []
  const links = spec.links ?? []
  const zones = spec.zones ?? []
  const resources = spec.resources ?? []
  const routes = spec.routes ?? []
  const sources = spec.sources ?? []
  const entityTypes = spec.entityTypes ?? []
  const resourceNodes = useMemo(() => {
    const set = new Set<string>()
    for (const r of spec.resources ?? []) if (r.node) set.add(r.node)
    return set
  }, [spec.resources])

  // ---- sizing -------------------------------------------------------------
  useEffect(() => {
    const el = paneRef.current
    if (!el) return
    const update = () => setSize({ w: el.clientWidth, h: el.clientHeight })
    update()
    const observer = new ResizeObserver(update)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  // Frame the layout when it first has a size to frame against.
  const framed = useRef(false)
  useEffect(() => {
    if (framed.current || size.w < 2) return
    const pts: Array<[number, number]> = []
    for (const n of nodes) pts.push([n.x, n.y])
    for (const z of zones) {
      pts.push([z.x, z.y])
      pts.push([z.x + z.width, z.y + z.height])
    }
    if (pts.length === 0) {
      framed.current = true
      return
    }
    const xs = pts.map((p) => p[0])
    const ys = pts.map((p) => p[1])
    const minX = Math.min(...xs)
    const maxX = Math.max(...xs)
    const minY = Math.min(...ys)
    const maxY = Math.max(...ys)
    const spanX = Math.max(maxX - minX, 10)
    const spanY = Math.max(maxY - minY, 10)
    setCamera({
      scale: Math.max(spanX / size.w, spanY / size.h) * 1.3,
      cx: (minX + maxX) / 2,
      cy: (minY + maxY) / 2,
    })
    framed.current = true
  }, [size, nodes, zones])

  // ---- plan image ---------------------------------------------------------
  useEffect(() => {
    setPlanImage(null)
    if (!plan) return
    const image = new Image()
    image.src = api.assets.contentUrl(projectId, plan.assetId)
    image.decode().then(() => setPlanImage(image)).catch(() => undefined)
  }, [plan, projectId])

  // ---- coordinate transforms ---------------------------------------------
  const toScreen = useCallback(
    (wx: number, wy: number) => ({
      x: size.w / 2 + (wx - camera.cx) / camera.scale,
      y: size.h / 2 - (wy - camera.cy) / camera.scale,
    }),
    [size, camera],
  )
  const toWorld = useCallback(
    (sx: number, sy: number) => ({
      x: camera.cx + (sx - size.w / 2) * camera.scale,
      y: camera.cy - (sy - size.h / 2) * camera.scale,
    }),
    [size, camera],
  )

  const localPoint = (e: React.PointerEvent) => {
    const box = paneRef.current!.getBoundingClientRect()
    return { x: e.clientX - box.left, y: e.clientY - box.top }
  }

  // ---- mutation helpers ---------------------------------------------------
  const patch = useCallback((change: Partial<SpecModel>) => {
    setSpec((prev) => ({ ...prev, ...change }))
    setDirty(true)
  }, [])

  const updateNode = (id: string, dx: number, dy: number) =>
    patch({ nodes: nodes.map((n) => (n.id === id ? { ...n, x: n.x + dx, y: n.y + dy } : n)) })
  const setNode = (id: string, values: Partial<Node>) =>
    patch({ nodes: nodes.map((n) => (n.id === id ? { ...n, ...values } : n)) })
  const setZone = (id: string, values: Partial<Zone>) =>
    patch({ zones: zones.map((z) => (z.id === id ? { ...z, ...values } : z)) })
  const moveZone = (id: string, dx: number, dy: number) =>
    patch({ zones: zones.map((z) => (z.id === id ? { ...z, x: z.x + dx, y: z.y + dy } : z)) })

  const setResource = (id: string, values: Partial<Resource>) =>
    patch({ resources: resources.map((r) => (r.id === id ? { ...r, ...values } : r)) })
  const addResource = (nodeId: string) => {
    const id = uniqueId('resource', resources.map((r) => r.id))
    patch({
      resources: [
        ...resources,
        { id, label: id, node: nodeId, capacity: 1, service: { distribution: 'constant', value: 60 }, queue: { discipline: 'fifo' } },
      ],
    })
  }
  const removeResource = (id: string) => patch({ resources: resources.filter((r) => r.id !== id) })
  const setRoute = (id: string, values: Partial<Route>) =>
    patch({ routes: routes.map((r) => (r.id === id ? { ...r, ...values } : r)) })
  const setSource = (id: string, values: Partial<Source>) =>
    patch({ sources: sources.map((sc) => (sc.id === id ? { ...sc, ...values } : sc)) })

  // ---- pointer interaction ------------------------------------------------
  const drag = useRef<
    | { kind: 'pan'; startCx: number; startCy: number; startX: number; startY: number }
    | { kind: 'node' | 'zone'; id: string; lastX: number; lastY: number }
    | { kind: 'zone-resize'; id: string; lastX: number; lastY: number }
    | null
  >(null)

  const onElementDown = (e: React.PointerEvent, kind: 'node' | 'zone' | 'zone-resize', id: string) => {
    e.stopPropagation()
    if (tool === 'link' && kind === 'node') {
      // Link tool: first node is the source, second completes the edge.
      if (linkFrom === null) setLinkFrom(id)
      else if (linkFrom !== id) {
        patch({ links: [...links, { from: linkFrom, to: id }] })
        setLinkFrom(null)
      }
      return
    }
    const p = localPoint(e)
    drag.current = { kind, id, lastX: p.x, lastY: p.y }
    setSelection(kind === 'zone-resize' ? { kind: 'zone', id } : { kind: kind as 'node' | 'zone', id })
    paneRef.current?.setPointerCapture(e.pointerId)
  }

  const onBackgroundDown = (e: React.PointerEvent) => {
    const p = localPoint(e)
    const world = toWorld(p.x, p.y)

    if (tool === 'node') {
      const id = uniqueId('node', nodes.map((n) => n.id))
      patch({ nodes: [...nodes, { id, label: id, x: round(world.x), y: round(world.y) }] })
      setSelection({ kind: 'node', id })
      setTool('select')
      return
    }
    if (tool === 'zone') {
      const id = uniqueId('zone', zones.map((z) => z.id))
      const side = camera.scale * 60
      patch({
        zones: [
          ...zones,
          { id, label: id, x: round(world.x - side / 2), y: round(world.y - side / 2), width: round(side), height: round(side) },
        ],
      })
      setSelection({ kind: 'zone', id })
      setTool('select')
      return
    }
    // Select tool (or link tool on empty space): pan, and clear any selection.
    setSelection(null)
    setLinkFrom(null)
    drag.current = { kind: 'pan', startCx: camera.cx, startCy: camera.cy, startX: p.x, startY: p.y }
    paneRef.current?.setPointerCapture(e.pointerId)
  }

  const onPointerMove = (e: React.PointerEvent) => {
    const d = drag.current
    if (!d) return
    const p = localPoint(e)

    if (d.kind === 'pan') {
      setCamera((c) => ({
        ...c,
        cx: d.startCx - (p.x - d.startX) * c.scale,
        cy: d.startCy + (p.y - d.startY) * c.scale,
      }))
      return
    }

    const dxWorld = (p.x - d.lastX) * camera.scale
    const dyWorld = -(p.y - d.lastY) * camera.scale
    d.lastX = p.x
    d.lastY = p.y

    if (d.kind === 'node') updateNode(d.id, dxWorld, dyWorld)
    else if (d.kind === 'zone') moveZone(d.id, dxWorld, dyWorld)
    else if (d.kind === 'zone-resize') {
      const zone = zones.find((z) => z.id === d.id)
      if (zone) {
        setZone(d.id, {
          width: Math.max(2, zone.width + dxWorld),
          // The handle is the top-right corner in screen terms, so growing it
          // raises the top edge: height grows and y moves with it.
          height: Math.max(2, zone.height + dyWorld),
          y: zone.y,
        })
      }
    }
  }

  const onPointerUp = (e: React.PointerEvent) => {
    if (drag.current) {
      // Round a dragged element's numbers so the inspector shows tidy metres.
      const d = drag.current
      if (d.kind === 'node') setNode(d.id, roundNode(nodes.find((n) => n.id === d.id)))
      if (d.kind === 'zone' || d.kind === 'zone-resize') setZone(d.id, roundZone(zones.find((z) => z.id === d.id)))
      drag.current = null
    }
    paneRef.current?.releasePointerCapture(e.pointerId)
  }

  const onWheel = (e: React.WheelEvent) => {
    const p = localPoint(e as unknown as React.PointerEvent)
    const before = toWorld(p.x, p.y)
    const factor = e.deltaY < 0 ? 1.12 : 1 / 1.12
    setCamera((c) => {
      const scale = Math.min(50, Math.max(0.01, c.scale / factor))
      const after = {
        x: c.cx + (p.x - size.w / 2) * scale,
        y: c.cy - (p.y - size.h / 2) * scale,
      }
      return { scale, cx: c.cx + (before.x - after.x), cy: c.cy + (before.y - after.y) }
    })
  }

  // ---- delete via keyboard ------------------------------------------------
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement
      if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA') return
      if ((e.key === 'Delete' || e.key === 'Backspace') && selection) {
        e.preventDefault()
        remove(selection)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  const remove = (target: NonNullable<Selection>) => {
    if (target.kind === 'node') {
      patch({
        nodes: nodes.filter((n) => n.id !== target.id),
        // A link to a removed node would dangle, so it goes too.
        links: links.filter((l) => l.from !== target.id && l.to !== target.id),
      })
    } else {
      patch({ zones: zones.filter((z) => z.id !== target.id) })
    }
    setSelection(null)
  }

  const save = useMutation({
    mutationFn: () => api.models.update(projectId, model.id, { spec }),
    onSuccess: () => {
      setDirty(false)
      void queryClient.invalidateQueries({ queryKey: ['model', projectId, model.id] })
    },
  })
  const saveError = save.error instanceof Error ? save.error.message : null

  // ---- plan backdrop geometry --------------------------------------------
  const planLayout = useMemo(() => {
    if (!plan || !planImage) return null
    const widthMeters = plan.imageWidth * plan.metersPerPixel
    const heightMeters = plan.imageHeight * plan.metersPerPixel
    const left = -plan.originPxX * plan.metersPerPixel
    const top = plan.flipY ? plan.originPxY * plan.metersPerPixel : -plan.originPxY * plan.metersPerPixel
    const topLeft = toScreen(left, top)
    const origin = toScreen(0, 0)
    return {
      x: topLeft.x,
      y: topLeft.y,
      width: widthMeters / camera.scale,
      height: heightMeters / camera.scale,
      rotation: plan.rotationDeg,
      origin,
      href: api.assets.contentUrl(projectId, plan.assetId),
    }
  }, [plan, planImage, toScreen, camera.scale, projectId])

  const cursor = tool === 'node' || tool === 'zone' ? 'crosshair' : tool === 'link' ? 'copy' : 'grab'

  return (
    <div className="editor">
      <div className="editor-bar">
        <Link to={`/projects/${projectId}`} className="btn ghost small">
          ← Back
        </Link>
        <strong style={{ marginLeft: 4 }}>{model.name}</strong>

        <div className="editor-tools">
          {(['select', 'node', 'zone', 'link'] as Tool[]).map((t) => (
            <button
              key={t}
              className={`btn small ${tool === t ? 'toggle on' : 'ghost'}`}
              onClick={() => {
                setTool(t)
                setLinkFrom(null)
              }}
              title={TOOL_HINT[t]}
            >
              {TOOL_LABEL[t]}
            </button>
          ))}
        </div>

        <div className="spacer" />

        <select className="select-inline" value={planId} onChange={(e) => setPlanId(e.target.value)}>
          <option value="">No plan</option>
          {plans.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>

        <button className="btn primary small" onClick={() => save.mutate()} disabled={!dirty || save.isPending}>
          {save.isPending && <span className="spinner" />}
          {dirty ? 'Save layout' : 'Saved'}
        </button>
      </div>

      <div className="editor-body">
        <div
          className="editor-canvas"
          ref={paneRef}
          style={{ cursor }}
          onPointerDown={onBackgroundDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onWheel={onWheel}
        >
          <svg width={size.w} height={size.h}>
            {planLayout && (
              <image
                href={planLayout.href}
                x={planLayout.x}
                y={planLayout.y}
                width={planLayout.width}
                height={planLayout.height}
                opacity={0.6}
                preserveAspectRatio="none"
                transform={
                  planLayout.rotation
                    ? `rotate(${-planLayout.rotation} ${planLayout.origin.x} ${planLayout.origin.y})`
                    : undefined
                }
              />
            )}

            {links.map((l, i) => {
              const a = nodes.find((n) => n.id === l.from)
              const b = nodes.find((n) => n.id === l.to)
              if (!a || !b) return null
              const pa = toScreen(a.x, a.y)
              const pb = toScreen(b.x, b.y)
              return (
                <line
                  key={i}
                  x1={pa.x}
                  y1={pa.y}
                  x2={pb.x}
                  y2={pb.y}
                  className="edit-link"
                  onPointerDown={(e) => {
                    e.stopPropagation()
                    if (tool === 'select') patch({ links: links.filter((_, j) => j !== i) })
                  }}
                />
              )
            })}

            {zones.map((z) => {
              const p = toScreen(z.x, z.y + z.height)
              const w = z.width / camera.scale
              const h = z.height / camera.scale
              const selected = selection?.kind === 'zone' && selection.id === z.id
              return (
                <g key={z.id}>
                  <rect
                    x={p.x}
                    y={p.y}
                    width={w}
                    height={h}
                    className={`edit-zone ${selected ? 'sel' : ''}`}
                    style={{ fill: z.color || '#3fc98a' }}
                    onPointerDown={(e) => onElementDown(e, 'zone', z.id)}
                  />
                  <text x={p.x + 5} y={p.y + 14} className="edit-zone-label">
                    {z.label ?? z.id}
                  </text>
                  {selected && (
                    <rect
                      x={p.x + w - HANDLE / 2}
                      y={p.y - HANDLE / 2}
                      width={HANDLE}
                      height={HANDLE}
                      className="edit-handle"
                      onPointerDown={(e) => onElementDown(e, 'zone-resize', z.id)}
                    />
                  )}
                </g>
              )
            })}

            {nodes.map((n) => {
              const p = toScreen(n.x, n.y)
              const selected = selection?.kind === 'node' && selection.id === n.id
              const isResource = resourceNodes.has(n.id)
              const isLinkFrom = linkFrom === n.id
              return (
                <g key={n.id}>
                  {isResource && (
                    <circle cx={p.x} cy={p.y} r={NODE_R + 5} className="edit-resource-ring" />
                  )}
                  <circle
                    cx={p.x}
                    cy={p.y}
                    r={NODE_R}
                    className={`edit-node ${isResource ? 'resource' : ''} ${selected ? 'sel' : ''} ${
                      isLinkFrom ? 'link-from' : ''
                    }`}
                    onPointerDown={(e) => onElementDown(e, 'node', n.id)}
                  />
                  <text x={p.x + 10} y={p.y + 4} className="edit-node-label">
                    {n.label ?? n.id}
                  </text>
                </g>
              )
            })}
          </svg>

          <div className="editor-hint">{TOOL_HINT[tool]}</div>
        </div>

        <Inspector
          tab={inspectorTab}
          setTab={setInspectorTab}
          selection={selection}
          nodes={nodes}
          zones={zones}
          resources={resources}
          routes={routes}
          sources={sources}
          onNode={setNode}
          onZone={setZone}
          onDelete={remove}
          onResource={setResource}
          onAddResource={addResource}
          onRemoveResource={removeResource}
          onRoute={setRoute}
          entityTypes={entityTypes}
          onSource={setSource}
          saveError={saveError}
        />
      </div>
    </div>
  )
}

interface InspectorProps {
  tab: 'element' | 'sources' | 'routes'
  setTab: (tab: 'element' | 'sources' | 'routes') => void
  selection: Selection
  nodes: Node[]
  zones: Zone[]
  resources: Resource[]
  routes: Route[]
  sources: Source[]
  entityTypes: EntityType[]
  onNode: (id: string, values: Partial<Node>) => void
  onZone: (id: string, values: Partial<Zone>) => void
  onDelete: (target: NonNullable<Selection>) => void
  onResource: (id: string, values: Partial<Resource>) => void
  onAddResource: (nodeId: string) => void
  onRemoveResource: (id: string) => void
  onRoute: (id: string, values: Partial<Route>) => void
  onSource: (id: string, values: Partial<Source>) => void
  saveError: string | null
}

function Inspector(props: InspectorProps) {
  const { tab, setTab, saveError } = props
  return (
    <aside className="editor-inspector">
      <div className="segmented" style={{ marginBottom: 14, width: '100%' }}>
        <button className={`segment ${tab === 'element' ? 'on' : ''}`} style={{ flex: 1 }} onClick={() => setTab('element')}>
          Selection
        </button>
        <button className={`segment ${tab === 'sources' ? 'on' : ''}`} style={{ flex: 1 }} onClick={() => setTab('sources')}>
          Sources
        </button>
        <button className={`segment ${tab === 'routes' ? 'on' : ''}`} style={{ flex: 1 }} onClick={() => setTab('routes')}>
          Routes
        </button>
      </div>

      {saveError && <div className="banner error">{saveError}</div>}

      {tab === 'routes' ? (
        <RoutesPanel {...props} />
      ) : tab === 'sources' ? (
        <SourcesPanel {...props} />
      ) : (
        <SelectionPanel {...props} />
      )}
    </aside>
  )
}

/**
 * Edits the sources: where entities enter the model and how often. The arrival
 * is a distribution of the gap between arrivals, so an exponential gap with a
 * mean of 45 seconds is a Poisson stream averaging one every 45 seconds — the
 * usual way demand is described. Batch, a start-and-stop window, and a total
 * limit shape it further.
 */
function SourcesPanel({ sources, nodes, entityTypes, routes, onSource }: InspectorProps) {
  const [openId, setOpenId] = useState<string | null>(sources[0]?.id ?? null)

  if (sources.length === 0) {
    return <p className="faint">This model has no sources to edit.</p>
  }

  const source = sources.find((s) => s.id === openId) ?? sources[0]
  const batched = source.batch !== undefined
  const windowed = (source.start ?? 0) > 0 || (source.stop ?? 0) > 0

  return (
    <>
      <Field label="Source">
        <select value={source.id} onChange={(e) => setOpenId(e.target.value)}>
          {sources.map((s) => (
            <option key={s.id} value={s.id}>
              {s.label ?? s.id}
            </option>
          ))}
        </select>
      </Field>

      <div className="row" style={{ gap: 8 }}>
        <Field label="Entity">
          <select value={source.entity ?? ''} onChange={(e) => onSource(source.id, { entity: e.target.value })}>
            <option value="">Choose…</option>
            {entityTypes.map((t) => (
              <option key={t.id} value={t.id}>
                {t.label ?? t.id}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Enters at">
          <select value={source.node ?? ''} onChange={(e) => onSource(source.id, { node: e.target.value })}>
            <option value="">Choose a node…</option>
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.label ?? n.id}
              </option>
            ))}
          </select>
        </Field>
      </div>

      <span className="field-label" style={{ marginTop: 4 }}>
        Gap between arrivals
      </span>
      <DistEditor
        dist={source.arrival ?? { distribution: 'exponential', mean: 60 }}
        onChange={(d) => onSource(source.id, { arrival: d })}
      />

      <Field label="Route">
        <select value={source.route ?? ''} onChange={(e) => onSource(source.id, { route: e.target.value })}>
          <option value="">The route that names this source</option>
          {routes.map((r) => (
            <option key={r.id} value={r.id}>
              {r.label ?? r.id}
            </option>
          ))}
        </select>
      </Field>

      <label className="toggle-row">
        <input
          type="checkbox"
          checked={batched}
          onChange={(e) => onSource(source.id, { batch: e.target.checked ? { distribution: 'constant', value: 2 } : undefined })}
        />
        <span>Arrive in batches</span>
      </label>
      {batched && (
        <>
          <span className="field-label">Batch size</span>
          <DistEditor dist={source.batch ?? { distribution: 'constant', value: 2 }} onChange={(d) => onSource(source.id, { batch: d })} />
        </>
      )}

      <label className="toggle-row">
        <input
          type="checkbox"
          checked={windowed}
          onChange={(e) => onSource(source.id, e.target.checked ? { start: 0, stop: 3600 } : { start: 0, stop: 0 })}
        />
        <span>Only active for a window</span>
      </label>
      {windowed && (
        <div className="row" style={{ gap: 8 }}>
          <Field label="Start (s)">
            <input type="number" min={0} value={source.start ?? 0} onChange={(e) => onSource(source.id, { start: Math.max(0, Number(e.target.value) || 0) })} />
          </Field>
          <Field label="Stop (s)">
            <input type="number" min={0} value={source.stop ?? 0} onChange={(e) => onSource(source.id, { stop: Math.max(0, Number(e.target.value) || 0) })} />
          </Field>
        </div>
      )}

      <Field label="Limit (0 = unlimited)">
        <input
          type="number"
          min={0}
          value={source.limit ?? 0}
          onChange={(e) => onSource(source.id, { limit: Math.max(0, Math.round(Number(e.target.value) || 0)) })}
        />
      </Field>
    </>
  )
}

function SelectionPanel({ selection, nodes, zones, resources, onNode, onZone, onDelete, onResource, onAddResource, onRemoveResource }: InspectorProps) {
  if (!selection) {
    return (
      <p className="faint">
        Select a node or a zone to edit its numbers, or add a resource to a node. Every position
        here is in metres, on the same grid the plan is calibrated to.
      </p>
    )
  }

  if (selection.kind === 'node') {
    const node = nodes.find((n) => n.id === selection.id)
    if (!node) return null
    const here = resources.filter((r) => r.node === node.id)
    return (
      <>
        <h3>Node</h3>
        <Field label="Name">
          <input value={node.label ?? ''} onChange={(e) => onNode(node.id, { label: e.target.value })} />
        </Field>
        <div className="row" style={{ gap: 8 }}>
          <Field label="X (m)">
            <input type="number" value={node.x} onChange={(e) => onNode(node.id, { x: Number(e.target.value) })} />
          </Field>
          <Field label="Y (m)">
            <input type="number" value={node.y} onChange={(e) => onNode(node.id, { y: Number(e.target.value) })} />
          </Field>
        </div>
        <button className="btn danger small" onClick={() => onDelete(selection)}>
          Delete node
        </button>

        <div className="inspector-section">
          <div className="row" style={{ marginBottom: 8 }}>
            <h3 style={{ flex: 1, margin: 0 }}>Resources here</h3>
            <button className="btn small ghost" onClick={() => onAddResource(node.id)}>
              + Add
            </button>
          </div>
          {here.length === 0 && (
            <p className="faint" style={{ fontSize: 12 }}>
              A resource is where entities are served: a gate, a crane, an inspection bay. Add one to
              give this node a capacity and a service time.
            </p>
          )}
          {here.map((r) => (
            <ResourceEditor key={r.id} resource={r} onResource={onResource} onRemove={onRemoveResource} />
          ))}
        </div>
      </>
    )
  }

  const zone = zones.find((z) => z.id === selection.id)
  if (!zone) return null
  return (
    <>
      <h3>Zone</h3>
      <Field label="Name">
        <input value={zone.label ?? ''} onChange={(e) => onZone(zone.id, { label: e.target.value })} />
      </Field>
      <div className="row" style={{ gap: 8 }}>
        <Field label="X (m)">
          <input type="number" value={zone.x} onChange={(e) => onZone(zone.id, { x: Number(e.target.value) })} />
        </Field>
        <Field label="Y (m)">
          <input type="number" value={zone.y} onChange={(e) => onZone(zone.id, { y: Number(e.target.value) })} />
        </Field>
      </div>
      <div className="row" style={{ gap: 8 }}>
        <Field label="Width (m)">
          <input type="number" value={zone.width} onChange={(e) => onZone(zone.id, { width: Number(e.target.value) })} />
        </Field>
        <Field label="Height (m)">
          <input type="number" value={zone.height} onChange={(e) => onZone(zone.id, { height: Number(e.target.value) })} />
        </Field>
      </div>
      <button className="btn danger small" onClick={() => onDelete(selection)}>
        Delete zone
      </button>
    </>
  )
}

/** Edits one resource: its capacity, service time and queue. */
function ResourceEditor({
  resource,
  onResource,
  onRemove,
}: {
  resource: Resource
  onResource: (id: string, values: Partial<Resource>) => void
  onRemove: (id: string) => void
}) {
  return (
    <div className="resource-card">
      <Field label="Name">
        <input value={resource.label ?? ''} onChange={(e) => onResource(resource.id, { label: e.target.value })} />
      </Field>
      <div className="row" style={{ gap: 8 }}>
        <Field label="Capacity">
          <input
            type="number"
            min={1}
            value={resource.capacity ?? 1}
            onChange={(e) => onResource(resource.id, { capacity: Math.max(1, Math.round(Number(e.target.value) || 1)) })}
          />
        </Field>
        <Field label="Queue">
          <select
            value={resource.queue?.discipline ?? 'fifo'}
            onChange={(e) =>
              onResource(resource.id, { queue: { ...resource.queue, discipline: e.target.value as QueueSpec['discipline'] } })
            }
          >
            <option value="fifo">First in, first out</option>
            <option value="lifo">Last in, first out</option>
            <option value="priority">By priority</option>
          </select>
        </Field>
      </div>
      <span className="field-label" style={{ marginTop: 4 }}>
        Service time
      </span>
      <DistEditor dist={resource.service ?? { distribution: 'constant', value: 60 }} onChange={(d) => onResource(resource.id, { service: d })} />

      <ShiftsEditor
        shifts={resource.shifts ?? []}
        onChange={(shifts) => onResource(resource.id, { shifts: shifts.length ? shifts : undefined })}
      />

      <FailureEditor
        failure={resource.failure ?? null}
        onChange={(failure) => onResource(resource.id, { failure: failure ?? null })}
      />

      <button className="btn danger small" style={{ marginTop: 10 }} onClick={() => onRemove(resource.id)}>
        Remove resource
      </button>
    </div>
  )
}

/**
 * Edits a resource's breakdowns. With none the resource never fails; turn it on
 * and it works for an uptime, then is down for a repair, then works again — the
 * unplanned downtime a real crane or gate has, which a plan that assumes
 * everything runs forever quietly ignores.
 *
 * Both are distributions: an exponential uptime with an 8-hour mean is the
 * memoryless failure a reliability figure usually describes, and the repair is
 * how long it is out. The mean uptime has to be positive, or the resource would
 * break the instant it started and never come back.
 */
function FailureEditor({ failure, onChange }: { failure: Failure | null; onChange: (failure: Failure | null) => void }) {
  const on = failure != null

  return (
    <div className="inspector-section" style={{ marginTop: 14, paddingTop: 12 }}>
      <label className="toggle-row" style={{ margin: 0 }}>
        <input
          type="checkbox"
          checked={on}
          onChange={(e) =>
            onChange(
              e.target.checked
                ? { uptime: { distribution: 'exponential', mean: 8 * 3600 }, repair: { distribution: 'exponential', mean: 1800 } }
                : null,
            )
          }
        />
        <span>Breaks down</span>
      </label>

      {on && failure && (
        <div style={{ marginTop: 8 }}>
          <span className="field-label">Time between failures</span>
          <DistEditor
            dist={failure.uptime ?? { distribution: 'exponential', mean: 8 * 3600 }}
            onChange={(d) => onChange({ ...failure, uptime: d })}
          />
          <span className="field-label" style={{ marginTop: 8 }}>
            Repair time
          </span>
          <DistEditor
            dist={failure.repair ?? { distribution: 'exponential', mean: 1800 }}
            onChange={(d) => onChange({ ...failure, repair: d })}
          />
        </div>
      )}
    </div>
  )
}

/**
 * Edits a resource's working hours. With no shifts a resource is always open;
 * add one and it is closed outside it, which is how a night that stops the
 * cranes, or a gate that shuts at six, gets into a model.
 *
 * Times are a time of day. Days are the days of a seven-day cycle counted from
 * the start of the run, so "day 1" is the first day simulated; leaving them all
 * off means every day.
 */
function ShiftsEditor({ shifts, onChange }: { shifts: Shift[]; onChange: (shifts: Shift[]) => void }) {
  const update = (i: number, values: Partial<Shift>) =>
    onChange(shifts.map((s, j) => (j === i ? { ...s, ...values } : s)))

  return (
    <div className="inspector-section" style={{ marginTop: 14, paddingTop: 12 }}>
      <div className="row" style={{ marginBottom: 6 }}>
        <span className="field-label" style={{ flex: 1, marginBottom: 0 }}>
          Shifts
        </span>
        <button
          className="btn small ghost"
          onClick={() => onChange([...shifts, { start: 8 * 3600, end: 18 * 3600 }])}
        >
          + Add
        </button>
      </div>

      {shifts.length === 0 && (
        <p className="faint" style={{ fontSize: 11, margin: 0 }}>
          Always open. Add a shift to close it outside working hours.
        </p>
      )}

      {shifts.map((shift, i) => (
        <div key={i} className="shift-card">
          <div className="row" style={{ gap: 8 }}>
            <Field label="From">
              <input
                type="time"
                value={secondsToTime(shift.start)}
                onChange={(e) => update(i, { start: timeToSeconds(e.target.value) })}
              />
            </Field>
            <Field label="To">
              <input
                type="time"
                value={secondsToTime(shift.end)}
                onChange={(e) => update(i, { end: timeToSeconds(e.target.value) })}
              />
            </Field>
            <button
              className="icon-btn btn danger small"
              style={{ alignSelf: 'end', marginBottom: 12 }}
              title="Remove shift"
              onClick={() => onChange(shifts.filter((_, j) => j !== i))}
            >
              ×
            </button>
          </div>
          <DayToggles
            days={shift.days ?? []}
            onChange={(days) => update(i, { days: days.length ? days : undefined })}
          />
        </div>
      ))}
    </div>
  )
}

function DayToggles({ days, onChange }: { days: number[]; onChange: (days: number[]) => void }) {
  const toggle = (d: number) =>
    onChange(days.includes(d) ? days.filter((x) => x !== d) : [...days, d].sort((a, b) => a - b))

  return (
    <div className="day-toggles">
      {[0, 1, 2, 3, 4, 5, 6].map((d) => (
        <button
          key={d}
          className={`day-toggle ${days.length === 0 || days.includes(d) ? 'on' : ''}`}
          title={days.length === 0 ? 'Every day' : `Day ${d + 1} of the week`}
          onClick={() => toggle(d)}
        >
          {d + 1}
        </button>
      ))}
      <span className="faint" style={{ fontSize: 10, marginLeft: 6 }}>
        {days.length === 0 ? 'every day' : 'days of the run week'}
      </span>
    </div>
  )
}

function secondsToTime(seconds: number): string {
  const s = Math.max(0, Math.min(86399, Math.round(seconds)))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`
}

function timeToSeconds(value: string): number {
  const [h, m] = value.split(':').map(Number)
  return (h || 0) * 3600 + (m || 0) * 60
}

/**
 * Edits a distribution: the kind, and only the fields that kind reads. The
 * defaults it fills in when the kind changes keep the result valid, so switching
 * from a constant to a triangular does not save a distribution with no bounds.
 */
function DistEditor({ dist, onChange }: { dist: Dist; onChange: (dist: Dist) => void }) {
  const kind = dist.distribution ?? 'constant'
  const set = (values: Partial<Dist>) => onChange({ ...dist, ...values })
  const num = (v: number | undefined, fallback = 0) => (v === undefined ? fallback : v)

  const changeKind = (next: DistKind) => {
    // Seed the new kind with sensible numbers derived from whatever was there,
    // so the switch never lands on an invalid distribution.
    const centre = num(dist.value ?? dist.mean ?? dist.mode, 60)
    switch (next) {
      case 'constant':
        onChange({ distribution: 'constant', value: centre })
        break
      case 'exponential':
      case 'lognormal':
        onChange({ distribution: next, mean: Math.max(0.1, centre), sd: next === 'lognormal' ? centre * 0.3 : undefined })
        break
      case 'normal':
        onChange({ distribution: 'normal', mean: centre, sd: Math.max(0.1, centre * 0.2) })
        break
      case 'uniform':
        onChange({ distribution: 'uniform', min: centre * 0.6, max: centre * 1.4 })
        break
      case 'triangular':
        onChange({ distribution: 'triangular', min: centre * 0.6, mode: centre, max: centre * 1.6 })
        break
      default:
        onChange({ distribution: next })
    }
  }

  return (
    <div className="dist-editor">
      <select value={kind} onChange={(e) => changeKind(e.target.value as DistKind)}>
        <option value="constant">Fixed</option>
        <option value="exponential">Exponential (by mean)</option>
        <option value="normal">Normal</option>
        <option value="triangular">Triangular</option>
        <option value="uniform">Uniform</option>
        <option value="lognormal">Log-normal</option>
      </select>

      <div className="row" style={{ gap: 6, marginTop: 6 }}>
        {kind === 'constant' && (
          <Field label="Seconds">
            <input type="number" value={num(dist.value)} onChange={(e) => set({ value: Number(e.target.value) })} />
          </Field>
        )}
        {(kind === 'exponential' || kind === 'lognormal') && (
          <Field label="Mean (s)">
            <input type="number" value={num(dist.mean)} onChange={(e) => set({ mean: Number(e.target.value) })} />
          </Field>
        )}
        {(kind === 'normal' || kind === 'lognormal') && (
          <Field label="Std dev (s)">
            <input type="number" value={num(dist.sd)} onChange={(e) => set({ sd: Number(e.target.value) })} />
          </Field>
        )}
        {kind === 'normal' && (
          <Field label="Mean (s)">
            <input type="number" value={num(dist.mean)} onChange={(e) => set({ mean: Number(e.target.value) })} />
          </Field>
        )}
        {(kind === 'uniform' || kind === 'triangular') && (
          <Field label="Min (s)">
            <input type="number" value={num(dist.min)} onChange={(e) => set({ min: Number(e.target.value) })} />
          </Field>
        )}
        {kind === 'triangular' && (
          <Field label="Mode (s)">
            <input type="number" value={num(dist.mode)} onChange={(e) => set({ mode: Number(e.target.value) })} />
          </Field>
        )}
        {(kind === 'uniform' || kind === 'triangular') && (
          <Field label="Max (s)">
            <input type="number" value={num(dist.max)} onChange={(e) => set({ max: Number(e.target.value) })} />
          </Field>
        )}
      </div>
    </div>
  )
}

/**
 * Edits routes: the ordered steps an entity takes. A step references a node to
 * travel to or a resource to use, so its dropdowns are the model's own nodes and
 * resources. The server validates the whole route on save — a resource seized
 * and never released, a step to a node that does not exist — and the message
 * comes back to the banner above.
 */
function RoutesPanel({ routes, nodes, resources, onRoute }: InspectorProps) {
  const [openId, setOpenId] = useState<string | null>(routes[0]?.id ?? null)

  if (routes.length === 0) {
    return <p className="faint">This model has no routes to edit.</p>
  }

  const route = routes.find((r) => r.id === openId) ?? routes[0]

  return (
    <>
      <Field label="Route">
        <select value={route.id} onChange={(e) => setOpenId(e.target.value)}>
          {routes.map((r) => (
            <option key={r.id} value={r.id}>
              {r.label ?? r.id}
            </option>
          ))}
        </select>
      </Field>

      <StepList
        steps={route.steps ?? []}
        onChange={(steps) => onRoute(route.id, { steps })}
        nodes={nodes}
        resources={resources}
        depth={0}
      />
    </>
  )
}

// Bounds how deep the UI will build nested branches. The engine allows more,
// but past this a branch inside a branch inside a branch stops being something
// anyone can read in a side panel.
const MAX_BRANCH_DEPTH = 3

/** An ordered list of route steps, rendered recursively so a branch's own
 *  steps are edited the same way as the route's. */
function StepList({
  steps,
  onChange,
  nodes,
  resources,
  depth,
}: {
  steps: Step[]
  onChange: (steps: Step[]) => void
  nodes: Node[]
  resources: Resource[]
  depth: number
}) {
  const setStep = (i: number, values: Partial<Step>) =>
    onChange(steps.map((s, j) => (j === i ? { ...s, ...values } : s)))
  const move = (i: number, dir: -1 | 1) => {
    const j = i + dir
    if (j < 0 || j >= steps.length) return
    const next = steps.slice()
    ;[next[i], next[j]] = [next[j], next[i]]
    onChange(next)
  }

  return (
    <div className="step-list">
      {steps.map((step, i) => (
        <div key={i} className="step-card">
          <div className="row" style={{ gap: 6, alignItems: 'center' }}>
            <span className="step-index">{i + 1}</span>
            <select value={step.type} onChange={(e) => setStep(i, { type: e.target.value as StepType })} style={{ flex: 1 }}>
              <option value="travel">Travel to</option>
              <option value="use">Use resource</option>
              <option value="delay">Wait</option>
              <option value="seize">Seize resource</option>
              <option value="release">Release resource</option>
              <option value="branch">Branch</option>
              <option value="exit">Exit</option>
            </select>
            <button className="icon-btn btn ghost small" title="Move up" onClick={() => move(i, -1)}>
              ↑
            </button>
            <button className="icon-btn btn ghost small" title="Move down" onClick={() => move(i, 1)}>
              ↓
            </button>
            <button className="icon-btn btn danger small" title="Remove step" onClick={() => onChange(steps.filter((_, j) => j !== i))}>
              ×
            </button>
          </div>

          {step.type === 'travel' && (
            <select value={step.to ?? ''} onChange={(e) => setStep(i, { to: e.target.value })} style={{ marginTop: 6 }}>
              <option value="">Choose a node…</option>
              {nodes.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.label ?? n.id}
                </option>
              ))}
            </select>
          )}

          {(step.type === 'use' || step.type === 'seize' || step.type === 'release') && (
            <select value={step.resource ?? ''} onChange={(e) => setStep(i, { resource: e.target.value })} style={{ marginTop: 6 }}>
              <option value="">Choose a resource…</option>
              {resources.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.label ?? r.id}
                </option>
              ))}
            </select>
          )}

          {step.type === 'delay' && (
            <div style={{ marginTop: 6 }}>
              <DistEditor dist={step.duration ?? { distribution: 'constant', value: 60 }} onChange={(d) => setStep(i, { duration: d })} />
            </div>
          )}

          {step.type === 'branch' && (
            <BranchList
              branches={step.branches ?? []}
              onChange={(branches) => setStep(i, { branches })}
              nodes={nodes}
              resources={resources}
              depth={depth}
            />
          )}
        </div>
      ))}

      <button className="btn small ghost" style={{ marginTop: 8 }} onClick={() => onChange([...steps, { type: 'travel' }])}>
        + Add step
      </button>
    </div>
  )
}

/**
 * The alternatives of a branch step, each with a weight and its own steps.
 *
 * The weight is relative: a branch of weight 3 against a branch of weight 1 is
 * taken three times as often. The share each takes is shown so a reader does
 * not have to do the division in their head.
 */
function BranchList({
  branches,
  onChange,
  nodes,
  resources,
  depth,
}: {
  branches: Branch[]
  onChange: (branches: Branch[]) => void
  nodes: Node[]
  resources: Resource[]
  depth: number
}) {
  const total = branches.reduce((sum, b) => sum + (b.weight || 0), 0)
  const update = (i: number, values: Partial<Branch>) =>
    onChange(branches.map((b, j) => (j === i ? { ...b, ...values } : b)))

  return (
    <div className="branch-list">
      {branches.map((branch, i) => (
        <div key={i} className="branch-card">
          <div className="row" style={{ gap: 6, alignItems: 'flex-end' }}>
            <Field label="Share">
              <input
                type="number"
                min={0}
                step={0.5}
                value={branch.weight ?? 1}
                onChange={(e) => update(i, { weight: Math.max(0, Number(e.target.value) || 0) })}
              />
            </Field>
            <span className="branch-share">
              {total > 0 ? `${Math.round(((branch.weight || 0) / total) * 100)}%` : '—'}
            </span>
            <input
              className="branch-label"
              placeholder="label"
              value={branch.label ?? ''}
              onChange={(e) => update(i, { label: e.target.value })}
            />
            <button className="icon-btn btn danger small" title="Remove branch" onClick={() => onChange(branches.filter((_, j) => j !== i))}>
              ×
            </button>
          </div>

          {depth < MAX_BRANCH_DEPTH ? (
            <StepList
              steps={branch.steps ?? []}
              onChange={(steps) => update(i, { steps })}
              nodes={nodes}
              resources={resources}
              depth={depth + 1}
            />
          ) : (
            <p className="faint" style={{ fontSize: 11 }}>
              {(branch.steps?.length ?? 0)} steps, nested too deep to edit here.
            </p>
          )}
        </div>
      ))}

      <button className="btn small ghost" style={{ marginTop: 6 }} onClick={() => onChange([...branches, { weight: 1, steps: [] }])}>
        + Add branch
      </button>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="field" style={{ flex: 1 }}>
      <span className="field-label">{label}</span>
      {children}
    </label>
  )
}

const TOOL_LABEL: Record<Tool, string> = { select: 'Move', node: '+ Node', zone: '+ Zone', link: 'Link' }
const TOOL_HINT: Record<Tool, string> = {
  select: 'Drag a node or zone to move it. Drag the background to pan, scroll to zoom.',
  node: 'Click to place a node.',
  zone: 'Click to place a zone, then drag its corner to size it.',
  link: 'Click one node, then another, to connect them. Click a link to remove it.',
}

function uniqueId(prefix: string, existing: string[]): string {
  const taken = new Set(existing)
  for (let i = 1; ; i++) {
    const id = `${prefix}_${i}`
    if (!taken.has(id)) return id
  }
}

const round = (v: number) => Math.round(v * 100) / 100
const roundNode = (n: Node | undefined): Partial<Node> => (n ? { x: round(n.x), y: round(n.y) } : {})
const roundZone = (z: Zone | undefined): Partial<Zone> =>
  z ? { x: round(z.x), y: round(z.y), width: round(z.width), height: round(z.height) } : {}
