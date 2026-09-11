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
interface Resource {
  id: string
  label?: string
  node?: string
}

// The spec is kept whole and only its geometry is touched, so saving round
// trips everything the editor does not understand back unchanged.
interface SpecModel {
  nodes?: Node[]
  links?: Link[]
  zones?: Zone[]
  resources?: Resource[]
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

  const nodes = spec.nodes ?? []
  const links = spec.links ?? []
  const zones = spec.zones ?? []
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
          selection={selection}
          nodes={nodes}
          zones={zones}
          onNode={setNode}
          onZone={setZone}
          onDelete={remove}
        />
      </div>
    </div>
  )
}

function Inspector({
  selection,
  nodes,
  zones,
  onNode,
  onZone,
  onDelete,
}: {
  selection: Selection
  nodes: Node[]
  zones: Zone[]
  onNode: (id: string, values: Partial<Node>) => void
  onZone: (id: string, values: Partial<Zone>) => void
  onDelete: (target: NonNullable<Selection>) => void
}) {
  if (!selection) {
    return (
      <aside className="editor-inspector">
        <p className="faint">
          Select a node or a zone to edit its numbers. Every position here is in metres, on the same
          grid the plan is calibrated to.
        </p>
      </aside>
    )
  }

  if (selection.kind === 'node') {
    const node = nodes.find((n) => n.id === selection.id)
    if (!node) return <aside className="editor-inspector" />
    return (
      <aside className="editor-inspector">
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
      </aside>
    )
  }

  const zone = zones.find((z) => z.id === selection.id)
  if (!zone) return <aside className="editor-inspector" />
  return (
    <aside className="editor-inspector">
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
    </aside>
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
