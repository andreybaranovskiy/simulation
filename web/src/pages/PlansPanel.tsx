import { useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { SitePlan } from '@/api/types'

/**
 * Uploading a site drawing and turning it into a georeferenced plan.
 *
 * The upload and the plan are separate steps in the API, but one action here:
 * a drawing with no scale is not useful on its own, so uploading one always
 * creates a plan and offers to calibrate it.
 */
export function PlansPanel({ projectId, canEdit }: { projectId: string; canEdit: boolean }) {
  const queryClient = useQueryClient()
  const fileInput = useRef<HTMLInputElement>(null)

  const [uploading, setUploading] = useState(false)
  const [uploadProgress, setUploadProgress] = useState(0)
  const [error, setError] = useState<string | null>(null)

  const plans = useQuery({
    queryKey: ['plans', projectId],
    queryFn: () => api.plans.list(projectId),
  })

  const upload = useMutation({
    mutationFn: async (file: File) => {
      setUploading(true)
      setUploadProgress(0)
      setError(null)

      const asset = await api.assets.upload(projectId, 'plan_image', file, setUploadProgress)
      return api.plans.create(projectId, asset.id, file.name.replace(/\.[^.]+$/, ''))
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['plans', projectId] })
    },
    onError: (err) => {
      setError(err instanceof Error ? err.message : 'The upload failed.')
    },
    onSettled: () => {
      setUploading(false)
      if (fileInput.current) fileInput.current.value = ''
    },
  })

  const remove = useMutation({
    mutationFn: (planId: string) => api.plans.remove(projectId, planId),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['plans', projectId] }),
  })

  return (
    <div className="card">
      <div className="card-head">
        <h2 style={{ flex: 1 }}>Site plans</h2>

        {canEdit && (
          <>
            <input
              ref={fileInput}
              type="file"
              accept="image/png,image/jpeg,image/gif"
              style={{ display: 'none' }}
              onChange={(e) => {
                const file = e.target.files?.[0]
                if (file) upload.mutate(file)
              }}
            />
            <button className="btn small" onClick={() => fileInput.current?.click()} disabled={uploading}>
              {uploading && <span className="spinner" />}
              Upload a drawing
            </button>
          </>
        )}
      </div>

      {error && <div className="banner error">{error}</div>}

      {uploading && (
        <div className="progress" style={{ marginBottom: 12 }}>
          <span style={{ width: `${Math.round(uploadProgress * 100)}%` }} />
        </div>
      )}

      {plans.data?.length === 0 && (
        <p className="dim" style={{ margin: 0, fontSize: 13 }}>
          Upload a floor plan or site drawing, then draw a line on a distance you know to set its
          scale. That is what turns the 2D view into real metres, and what makes heatmap cells and
          report scale bars mean something.
        </p>
      )}

      {plans.data && plans.data.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Plan</th>
              <th className="num">Size</th>
              <th className="num">Scale</th>
              <th>Calibration</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {plans.data.map((plan) => (
              <PlanRow
                key={plan.id}
                projectId={projectId}
                plan={plan}
                canEdit={canEdit}
                onRemove={() => remove.mutate(plan.id)}
              />
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function PlanRow({
  projectId,
  plan,
  canEdit,
  onRemove,
}: {
  projectId: string
  plan: SitePlan
  canEdit: boolean
  onRemove: () => void
}) {
  // A plan at exactly one metre per pixel has almost certainly never been
  // calibrated: that is the value it is created with.
  const calibrated = plan.calibration != null || plan.metersPerPixel !== 1

  const widthMeters = plan.imageWidth * plan.metersPerPixel
  const heightMeters = plan.imageHeight * plan.metersPerPixel

  return (
    <tr>
      <td style={{ fontWeight: 500 }}>{plan.name}</td>

      <td className="num dim">
        {plan.imageWidth} × {plan.imageHeight} px
      </td>

      <td className="num dim">
        {plan.metersPerPixel < 0.01
          ? `${(plan.metersPerPixel * 1000).toFixed(2)} mm/px`
          : `${plan.metersPerPixel.toFixed(3)} m/px`}
      </td>

      <td>
        {calibrated ? (
          <span className="dim" style={{ fontSize: 12 }}>
            {Math.round(widthMeters)} × {Math.round(heightMeters)} m
          </span>
        ) : (
          <span style={{ fontSize: 12, color: 'var(--warn)' }}>Not calibrated</span>
        )}
      </td>

      <td className="num">
        <div className="row" style={{ justifyContent: 'flex-end', gap: 6 }}>
          <Link to={`/projects/${projectId}/plans/${plan.id}`} className="btn small">
            {calibrated ? 'Recalibrate' : 'Calibrate'}
          </Link>
          {canEdit && (
            <button className="btn danger small" onClick={onRemove}>
              Delete
            </button>
          )}
        </div>
      </td>
    </tr>
  )
}
