import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { SitePlan } from '@/api/types'

type Point = { x: number; y: number }

/**
 * Calibrating a plan: draw a line on something whose length you know, type the
 * length, and the drawing gains a scale.
 *
 * Drawing rather than typing a ratio, because nobody knows their drawing's
 * metres per pixel but everybody knows how wide a lane or a bay is. The numbers
 * stay editable underneath, so a value that is known exactly can be entered
 * directly, and the drawn measurement is stored so it can be re-examined later
 * rather than only its result.
 */
export function PlanCalibrate() {
  const { projectId, planId } = useParams<{ projectId: string; planId: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const canvasRef = useRef<HTMLCanvasElement>(null)

  // The drawing is held in state rather than a ref.
  //
  // A ref does not trigger a render, so the redraw effect would not know the
  // image had arrived, and the canvas stayed empty until something else
  // happened to redraw it. State makes loading the image and drawing it one
  // causal chain instead of two that have to be ordered by hand.
  const [image, setImage] = useState<HTMLImageElement | null>(null)
  const [imageError, setImageError] = useState<string | null>(null)

  const plan = useQuery({
    queryKey: ['plan', projectId, planId],
    queryFn: () => api.plans.get(projectId!, planId!),
    enabled: !!projectId && !!planId,
  })

  // The measurement, in image pixels.
  const [from, setFrom] = useState<Point | null>(null)
  const [to, setTo] = useState<Point | null>(null)
  const [dragging, setDragging] = useState(false)
  const [cursor, setCursor] = useState<Point | null>(null)

  const [realLength, setRealLength] = useState('')
  const [origin, setOrigin] = useState<Point>({ x: 0, y: 0 })
  const [rotation, setRotation] = useState(0)
  const [flipY, setFlipY] = useState(true)
  const [name, setName] = useState('')

  // View transform for the stage, in screen pixels per image pixel.
  const [zoom, setZoom] = useState(1)
  const [pan, setPan] = useState<Point>({ x: 0, y: 0 })
  const panning = useRef<{ x: number; y: number } | null>(null)

  const pixelDistance = from && to ? Math.hypot(to.x - from.x, to.y - from.y) : 0
  const metersPerPixel =
    pixelDistance > 0 && Number(realLength) > 0 ? Number(realLength) / pixelDistance : null

  // Load the existing calibration so recalibrating starts from what is there.
  useEffect(() => {
    const data = plan.data
    if (!data) return

    setName(data.name)
    setOrigin({ x: data.originPxX, y: data.originPxY })
    setRotation(data.rotationDeg)
    setFlipY(data.flipY)

    if (data.calibration?.points) {
      const [a, b] = data.calibration.points
      setFrom({ x: a[0], y: a[1] })
      setTo({ x: b[0], y: b[1] })
      setRealLength(String(data.calibration.realLength))
    }
  }, [plan.data])

  // Load the drawing itself.
  useEffect(() => {
    if (!plan.data || !projectId) return

    let cancelled = false
    const loaded = new Image()
    loaded.src = api.assets.contentUrl(projectId, plan.data.assetId)

    loaded
      .decode()
      .then(() => {
        if (cancelled) return
        setImage(loaded)
        setImageError(null)
      })
      .catch(() => {
        if (!cancelled) setImageError('The drawing could not be loaded.')
      })

    return () => {
      cancelled = true
    }
  }, [plan.data, projectId])

  const fitToStage = useCallback((image: HTMLImageElement) => {
    const canvas = canvasRef.current
    if (!canvas) return

    const scale = Math.min(canvas.clientWidth / image.width, canvas.clientHeight / image.height) * 0.92
    setZoom(scale)
    setPan({
      x: (canvas.clientWidth - image.width * scale) / 2,
      y: (canvas.clientHeight - image.height * scale) / 2,
    })
  }, [])

  // Frame the drawing once, when it first arrives.
  useEffect(() => {
    if (image) fitToStage(image)
  }, [image, fitToStage])

  const toImage = useCallback(
    (clientX: number, clientY: number): Point => {
      const canvas = canvasRef.current
      if (!canvas) return { x: 0, y: 0 }

      const rect = canvas.getBoundingClientRect()
      return {
        x: (clientX - rect.left - pan.x) / zoom,
        y: (clientY - rect.top - pan.y) / zoom,
      }
    },
    [pan, zoom],
  )

  const draw = useCallback(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    canvas.width = canvas.clientWidth * dpr
    canvas.height = canvas.clientHeight * dpr

    const ctx = canvas.getContext('2d')
    if (!ctx) return

    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.fillStyle = '#070b1c'
    ctx.fillRect(0, 0, canvas.clientWidth, canvas.clientHeight)

    if (!image) return

    ctx.save()
    ctx.translate(pan.x, pan.y)
    ctx.scale(zoom, zoom)
    ctx.imageSmoothingQuality = 'high'
    ctx.drawImage(image, 0, 0)
    ctx.restore()

    const end = to ?? (dragging ? cursor : null)

    if (from && end) {
      drawMeasurement(ctx, screenOf(from), screenOf(end), realLength, metersPerPixel)
    }

    drawOrigin(ctx, screenOf(origin), flipY, rotation)

    function screenOf(p: Point): Point {
      return { x: p.x * zoom + pan.x, y: p.y * zoom + pan.y }
    }
  }, [image, pan, zoom, from, to, cursor, dragging, realLength, metersPerPixel, origin, flipY, rotation])

  useEffect(() => {
    draw()
  }, [draw])

  useEffect(() => {
    const onResize = () => draw()
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [draw])

  const save = useMutation({
    mutationFn: () => {
      if (!projectId || !planId || !plan.data) throw new Error('No plan to save.')

      const update: Partial<SitePlan> = {
        name: name.trim() || plan.data.name,
        metersPerPixel: metersPerPixel ?? plan.data.metersPerPixel,
        originPxX: origin.x,
        originPxY: origin.y,
        rotationDeg: rotation,
        flipY,
      }

      // The measurement is stored alongside its result, so a later reader can
      // see what was measured rather than only the ratio it produced.
      if (from && to && Number(realLength) > 0) {
        update.calibration = {
          points: [
            [from.x, from.y],
            [to.x, to.y],
          ],
          realLength: Number(realLength),
          unit: 'm',
        }
      }

      return api.plans.update(projectId, planId, update)
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['plans', projectId] })
      navigate(`/projects/${projectId}`)
    },
  })

  const data = plan.data
  const widthMeters = data && metersPerPixel ? data.imageWidth * metersPerPixel : null
  const heightMeters = data && metersPerPixel ? data.imageHeight * metersPerPixel : null

  const step = !from || !to ? 0 : !Number(realLength) ? 1 : 2

  return (
    <div className="page">
      <div className="page-narrow">
        <div className="page-header">
          <div>
            <h1>Calibrate {data?.name}</h1>
            <div className="sub">
              Give the drawing a real-world scale, so distances on it become metres.
            </div>
          </div>
          <div className="spacer" />
          <button className="btn ghost" onClick={() => navigate(`/projects/${projectId}`)}>
            Cancel
          </button>
          <button
            className="btn primary"
            onClick={() => save.mutate()}
            disabled={save.isPending || metersPerPixel === null}
          >
            {save.isPending && <span className="spinner" />}
            Save calibration
          </button>
        </div>

        {imageError && <div className="banner error">{imageError}</div>}

        {save.error && (
          <div className="banner error">
            {save.error instanceof Error ? save.error.message : 'Could not save.'}
          </div>
        )}

        <div className="row" style={{ alignItems: 'flex-start', gap: 16 }}>
          <div style={{ flex: '1 1 520px', minWidth: 0 }}>
            <div
              className="calibrate-stage"
              onPointerDown={(e) => {
                if (e.button === 1 || e.shiftKey) {
                  panning.current = { x: e.clientX - pan.x, y: e.clientY - pan.y }
                  return
                }
                const point = toImage(e.clientX, e.clientY)
                setFrom(point)
                setTo(null)
                setCursor(point)
                setDragging(true)
                e.currentTarget.setPointerCapture(e.pointerId)
              }}
              onPointerMove={(e) => {
                if (panning.current) {
                  setPan({ x: e.clientX - panning.current.x, y: e.clientY - panning.current.y })
                  return
                }
                if (dragging) setCursor(toImage(e.clientX, e.clientY))
              }}
              onPointerUp={(e) => {
                panning.current = null
                if (dragging) {
                  setTo(toImage(e.clientX, e.clientY))
                  setDragging(false)
                }
              }}
              onWheel={(e) => {
                // Zoom about the cursor, so the point under it stays put.
                const factor = e.deltaY < 0 ? 1.12 : 1 / 1.12
                const rect = e.currentTarget.getBoundingClientRect()
                const px = e.clientX - rect.left
                const py = e.clientY - rect.top

                setPan({
                  x: px - (px - pan.x) * factor,
                  y: py - (py - pan.y) * factor,
                })
                setZoom((z) => Math.min(20, Math.max(0.02, z * factor)))
              }}
            >
              <canvas ref={canvasRef} />
            </div>

            <div className="row" style={{ marginTop: 10, gap: 8 }}>
              <button className="btn small ghost" onClick={() => image && fitToStage(image)}>
                Fit to view
              </button>
              <button
                className="btn small ghost"
                onClick={() => {
                  setFrom(null)
                  setTo(null)
                  setRealLength('')
                }}
              >
                Clear measurement
              </button>
              <span className="faint" style={{ fontSize: 12 }}>
                Drag to measure. Shift-drag or middle-drag to pan, scroll to zoom.
              </span>
            </div>
          </div>

          <div style={{ flex: '0 0 300px' }}>
            <div className="card">
              <h3 style={{ marginBottom: 12 }}>Steps</h3>

              <ol className="step-list">
                <li className={step > 0 ? 'done' : 'active'}>
                  Drag a line along something you know the length of: a lane, a bay, a building edge.
                </li>
                <li className={step > 1 ? 'done' : step === 1 ? 'active' : ''}>
                  Type that real length in metres.
                </li>
                <li className={step === 2 ? 'active' : ''}>Save.</li>
              </ol>

              <div className="field">
                <label htmlFor="real-length">Real length of the line</label>
                <div className="row">
                  <input
                    id="real-length"
                    type="number"
                    min={0}
                    step="any"
                    value={realLength}
                    onChange={(e) => setRealLength(e.target.value)}
                    placeholder="120"
                    disabled={!from || !to}
                  />
                  <span className="faint">m</span>
                </div>
                {from && to && (
                  <div className="hint">You drew {pixelDistance.toFixed(0)} pixels.</div>
                )}
              </div>

              {metersPerPixel !== null && (
                <div className="banner info" style={{ marginBottom: 14 }}>
                  <strong>{formatScale(metersPerPixel)}</strong>
                  <br />
                  The whole drawing is {Math.round(widthMeters ?? 0)} × {Math.round(heightMeters ?? 0)} m.
                </div>
              )}

              <h3 style={{ margin: '18px 0 10px' }}>Fine tuning</h3>

              <div className="field">
                <label htmlFor="plan-name">Plan name</label>
                <input id="plan-name" value={name} onChange={(e) => setName(e.target.value)} />
              </div>

              <div className="field">
                <label>World origin, in image pixels</label>
                <div className="row">
                  <input
                    type="number"
                    step="any"
                    value={origin.x}
                    onChange={(e) => setOrigin((p) => ({ ...p, x: Number(e.target.value) || 0 }))}
                  />
                  <input
                    type="number"
                    step="any"
                    value={origin.y}
                    onChange={(e) => setOrigin((p) => ({ ...p, y: Number(e.target.value) || 0 }))}
                  />
                </div>
                <div className="hint">
                  The pixel that sits at coordinate zero. Move it to line the drawing up with the model.
                </div>
              </div>

              <div className="field">
                <label htmlFor="rotation">Rotation</label>
                <div className="row">
                  <input
                    id="rotation"
                    type="range"
                    min={-180}
                    max={180}
                    step={0.5}
                    value={rotation}
                    onChange={(e) => setRotation(Number(e.target.value))}
                  />
                  <input
                    type="number"
                    step="any"
                    value={rotation}
                    onChange={(e) => setRotation(Number(e.target.value) || 0)}
                    style={{ width: 76 }}
                  />
                  <span className="faint">°</span>
                </div>
              </div>

              <div className="field" style={{ marginBottom: 0 }}>
                <label className="row" style={{ gap: 8, cursor: 'pointer' }}>
                  <input type="checkbox" checked={flipY} onChange={(e) => setFlipY(e.target.checked)} />
                  <span>Image Y grows downward</span>
                </label>
                <div className="hint">
                  Usually true for a raster drawing. Turn it off if the plan comes out mirrored.
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function drawMeasurement(
  ctx: CanvasRenderingContext2D,
  a: Point,
  b: Point,
  realLength: string,
  metersPerPixel: number | null,
) {
  ctx.save()

  ctx.strokeStyle = '#4c8dff'
  ctx.lineWidth = 2
  ctx.beginPath()
  ctx.moveTo(a.x, a.y)
  ctx.lineTo(b.x, b.y)
  ctx.stroke()

  // End caps perpendicular to the line, the way a dimension is drawn.
  const angle = Math.atan2(b.y - a.y, b.x - a.x)
  for (const p of [a, b]) {
    ctx.beginPath()
    ctx.moveTo(p.x + Math.cos(angle + Math.PI / 2) * 7, p.y + Math.sin(angle + Math.PI / 2) * 7)
    ctx.lineTo(p.x + Math.cos(angle - Math.PI / 2) * 7, p.y + Math.sin(angle - Math.PI / 2) * 7)
    ctx.stroke()
  }

  const label = metersPerPixel && Number(realLength) > 0 ? `${realLength} m` : 'Type the real length'

  const mx = (a.x + b.x) / 2
  const my = (a.y + b.y) / 2

  ctx.font = '12px system-ui, sans-serif'
  const width = ctx.measureText(label).width + 14

  ctx.fillStyle = 'rgba(11, 16, 32, 0.9)'
  ctx.fillRect(mx - width / 2, my - 26, width, 20)

  ctx.fillStyle = '#cfe0ff'
  ctx.textAlign = 'center'
  ctx.fillText(label, mx, my - 12)

  ctx.restore()
}

/** Draws the world origin and the axis directions, so a reader can see which
 *  way north is before anything is placed against it. */
function drawOrigin(ctx: CanvasRenderingContext2D, p: Point, flipY: boolean, rotation: number) {
  ctx.save()
  ctx.translate(p.x, p.y)
  ctx.rotate((-rotation * Math.PI) / 180)

  const length = 34
  const yDirection = flipY ? -1 : 1

  ctx.lineWidth = 2

  ctx.strokeStyle = '#e0566a'
  ctx.beginPath()
  ctx.moveTo(0, 0)
  ctx.lineTo(length, 0)
  ctx.stroke()

  ctx.strokeStyle = '#3fc98a'
  ctx.beginPath()
  ctx.moveTo(0, 0)
  ctx.lineTo(0, length * yDirection)
  ctx.stroke()

  ctx.fillStyle = '#cfe0ff'
  ctx.font = '10px system-ui, sans-serif'
  ctx.fillText('x', length + 4, 4)
  ctx.fillText('y', -3, length * yDirection + (flipY ? -6 : 12))

  ctx.beginPath()
  ctx.arc(0, 0, 4, 0, Math.PI * 2)
  ctx.fillStyle = '#f2c14e'
  ctx.fill()

  ctx.restore()
}

function formatScale(metersPerPixel: number): string {
  if (metersPerPixel < 0.01) return `${(metersPerPixel * 1000).toFixed(2)} mm per pixel`
  if (metersPerPixel < 1) return `${(metersPerPixel * 100).toFixed(1)} cm per pixel`
  return `${metersPerPixel.toFixed(3)} m per pixel`
}
