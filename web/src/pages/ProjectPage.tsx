import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { Run, RunStatus, Scenario } from '@/api/types'
import { mergeProgress, useRunEvents } from '@/state/runEvents'
import { formatDuration } from '@/state/timeline'
import { formatDate } from './Projects'
import { NewScenarioDialog } from './NewScenarioDialog'
import { PlansPanel } from './PlansPanel'

export function ProjectPage() {
  const { projectId } = useParams<{ projectId: string }>()
  const queryClient = useQueryClient()
  const { events, connected } = useRunEvents(projectId)

  const [creating, setCreating] = useState(false)

  const project = useQuery({
    queryKey: ['project', projectId],
    queryFn: () => api.projects.get(projectId!),
    enabled: !!projectId,
  })

  const scenarios = useQuery({
    queryKey: ['scenarios', projectId],
    queryFn: () => api.scenarios.list(projectId!),
    enabled: !!projectId,
  })

  const runs = useQuery({
    queryKey: ['runs', projectId],
    queryFn: () => api.runs.list(projectId!),
    enabled: !!projectId,
  })

  const startRun = useMutation({
    mutationFn: (scenarioId: string) => api.scenarios.run(projectId!, scenarioId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['runs', projectId] })
      void queryClient.invalidateQueries({ queryKey: ['scenarios', projectId] })
    },
  })

  const canEdit = project.data?.role === 'owner' || project.data?.role === 'editor'

  // Comparing needs results on both sides, so the entry point only appears
  // once two scenarios actually have a finished run behind them.
  const comparable = (scenarios.data ?? []).filter((s) => (s.runCount ?? 0) > 0)

  if (!projectId) return null

  return (
    <div className="page">
      <div className="page-narrow">
        <div className="page-header">
          <div>
            <h1>{project.data?.name ?? 'Project'}</h1>
            <div className="sub">{project.data?.description || 'No description.'}</div>
          </div>
          <div className="spacer" />

          {!connected && (
            <span className="pill" title="Live progress is reconnecting.">
              <span className="dot" /> Offline
            </span>
          )}

          {comparable.length >= 2 && (
            <Link
              to={`/projects/${projectId}/compare?scenarios=${comparable
                .slice(0, 2)
                .map((s) => s.id)
                .join(',')}`}
              className="btn ghost"
            >
              Compare
            </Link>
          )}

          {canEdit && (
            <button className="btn primary" onClick={() => setCreating(true)}>
              New scenario
            </button>
          )}
        </div>

        {startRun.error && (
          <div className="banner error">
            {startRun.error instanceof Error ? startRun.error.message : 'Could not start the run.'}
          </div>
        )}

        {creating && projectId && (
          <NewScenarioDialog
            projectId={projectId}
            onClose={() => setCreating(false)}
            onCreated={() => {
              setCreating(false)
              void queryClient.invalidateQueries({ queryKey: ['scenarios', projectId] })
            }}
          />
        )}

        <div className="card">
          <div className="card-head">
            <h2 style={{ flex: 1 }}>Scenarios</h2>
            <span className="faint" style={{ fontSize: 12 }}>
              {scenarios.data?.length ?? 0} defined
            </span>
          </div>

          {scenarios.isLoading && <span className="spinner" />}

          {scenarios.data?.length === 0 && (
            <div className="empty" style={{ padding: '28px 12px' }}>
              <h2>No scenarios yet</h2>
              <p className="dim">
                A scenario is a model plus the parameter values that make one case of it. Create two
                that differ in one value and the comparison tells you what that value costs.
              </p>
            </div>
          )}

          {scenarios.data && scenarios.data.length > 0 && (
            <table>
              <thead>
                <tr>
                  <th>Scenario</th>
                  <th>Model</th>
                  <th className="num">Runs</th>
                  <th>Latest run</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {scenarios.data.map((scenario) => (
                  <ScenarioRow
                    key={scenario.id}
                    projectId={projectId}
                    scenario={scenario}
                    live={scenario.latestRun ? events.get(scenario.latestRun.id) : undefined}
                    canEdit={canEdit}
                    starting={startRun.isPending && startRun.variables === scenario.id}
                    onRun={() => startRun.mutate(scenario.id)}
                  />
                ))}
              </tbody>
            </table>
          )}
        </div>

        <RunsPanel projectId={projectId} runs={runs.data ?? []} events={events} />

        <PlansPanel projectId={projectId} canEdit={canEdit} />
      </div>
    </div>
  )
}

function ScenarioRow({
  projectId,
  scenario,
  live,
  canEdit,
  starting,
  onRun,
}: {
  projectId: string
  scenario: Scenario
  live: ReturnType<typeof useRunEvents>['events'] extends Map<string, infer E> ? E | undefined : never
  canEdit: boolean
  starting: boolean
  onRun: () => void
}) {
  const latest = scenario.latestRun
  const merged = latest
    ? mergeProgress(
        {
          status: latest.status,
          progress: latest.progress,
          entityCount: latest.entityCount,
          simTime: latest.simTime,
        },
        live,
      )
    : null

  const active = merged?.status === 'running' || merged?.status === 'building'

  return (
    <tr>
      <td>
        <div style={{ fontWeight: 500 }}>{scenario.name}</div>
        {scenario.description && (
          <div className="faint" style={{ fontSize: 12 }}>
            {scenario.description}
          </div>
        )}
      </td>

      <td className="dim">{scenario.modelName}</td>
      <td className="num dim">{scenario.runCount ?? 0}</td>

      <td style={{ minWidth: 190 }}>
        {!latest && <span className="faint">Never run</span>}

        {latest && merged && (
          <div>
            <div className="row" style={{ gap: 8 }}>
              <StatusPill status={merged.status as RunStatus} />
              {latest.status === 'done' && (
                <Link to={`/projects/${projectId}/runs/${latest.id}`} className="small">
                  Open
                </Link>
              )}
            </div>

            {active && (
              <div className="progress" style={{ marginTop: 6 }}>
                <span style={{ width: `${Math.round(merged.progress * 100)}%` }} />
              </div>
            )}

            {active && (
              <div className="faint" style={{ fontSize: 11, marginTop: 3 }}>
                {merged.entityCount.toLocaleString()} entities
              </div>
            )}

            {!active && latest.status === 'done' && (
              <div className="faint" style={{ fontSize: 11, marginTop: 3 }}>
                {latest.entityCount.toLocaleString()} entities in {formatDuration(latest.durationMs / 1000)}
              </div>
            )}

            {latest.status === 'failed' && latest.error && (
              <div style={{ fontSize: 11, marginTop: 3, color: 'var(--bad)' }}>{latest.error}</div>
            )}
          </div>
        )}
      </td>

      <td className="num">
        {canEdit && (
          <button className="btn small" onClick={onRun} disabled={starting || active}>
            {starting && <span className="spinner" />}
            Run
          </button>
        )}
      </td>
    </tr>
  )
}

function RunsPanel({
  projectId,
  runs,
  events,
}: {
  projectId: string
  runs: Run[]
  events: Map<string, import('@/api/types').RunEvent>
}) {
  if (runs.length === 0) return null

  return (
    <div className="card">
      <div className="card-head">
        <h2 style={{ flex: 1 }}>Recent runs</h2>
      </div>

      <table>
        <thead>
          <tr>
            <th>Scenario</th>
            <th>Status</th>
            <th className="num">Entities</th>
            <th className="num">Sim time</th>
            <th className="num">Took</th>
            <th>When</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {runs.slice(0, 12).map((run) => {
            const merged = mergeProgress(
              {
                status: run.status,
                progress: run.progress,
                entityCount: run.entityCount,
                simTime: run.simTime,
              },
              events.get(run.id),
            )

            return (
              <tr key={run.id}>
                <td>
                  {run.scenarioName}
                  {run.replication > 0 && <span className="faint"> · replication {run.replication + 1}</span>}
                </td>
                <td>
                  <StatusPill status={merged.status as RunStatus} />
                </td>
                <td className="num dim">{merged.entityCount.toLocaleString()}</td>
                <td className="num dim">{formatDuration(merged.simTime)}</td>
                <td className="num dim">{run.durationMs ? `${(run.durationMs / 1000).toFixed(1)} s` : '—'}</td>
                <td className="faint">{formatDate(run.queuedAt)}</td>
                <td className="num">
                  {run.status === 'done' && (
                    <Link to={`/projects/${projectId}/runs/${run.id}`} className="btn small">
                      Open
                    </Link>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

export function StatusPill({ status }: { status: RunStatus }) {
  const labels: Record<RunStatus, string> = {
    queued: 'Queued',
    running: 'Running',
    building: 'Building',
    done: 'Done',
    failed: 'Failed',
    canceled: 'Cancelled',
  }

  return (
    <span className={`pill ${status}`}>
      <span className="dot" />
      {labels[status] ?? status}
    </span>
  )
}
