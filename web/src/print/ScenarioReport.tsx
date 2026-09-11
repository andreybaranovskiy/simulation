import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { KPISet, RunKPI, Scenario } from '@/api/types'
import { GanttChart, type GanttRow } from '@/charts/GanttChart'
import { LineChart } from '@/charts/LineChart'
import { StatTile, formatKPI } from '@/charts/StatTile'
import { formatDuration } from '@/state/timeline'
import { usePrintGate } from './PrintDocument'
import { ReportHeatmap } from './ReportHeatmap'

interface SeriesFile {
  key: string
  label: string
  unit?: string
  kind: string
  startTime: number
  bucketSeconds: number
  values: number[]
}

/**
 * A single scenario, in depth.
 *
 * It reads one representative run: the newest finished one, because a report is
 * about the scenario as most recently run. Replication spread belongs to a
 * comparison, where there is a baseline to judge it against; here the job is to
 * lay out what one run of this scenario looked like.
 */
export function ScenarioReport({
  projectId,
  scenarioId,
  sections,
}: {
  projectId: string
  scenarioId: string
  sections: Set<string>
}) {
  const scenario = useQuery({
    queryKey: ['scenario', projectId, scenarioId],
    queryFn: () => api.scenarios.get(projectId, scenarioId),
    staleTime: Infinity,
  })

  const runs = useQuery({
    queryKey: ['runs', projectId, scenarioId],
    queryFn: () => api.runs.list(projectId, scenarioId),
    staleTime: Infinity,
  })

  const runId = useMemo(() => {
    const done = (runs.data ?? []).filter((run) => run.status === 'done')
    // Newest finished run wins, so the report tracks the latest results.
    return done.sort((a, b) => (b.finishedAt ?? '').localeCompare(a.finishedAt ?? ''))[0]?.id ?? ''
  }, [runs.data])

  const manifest = useQuery({
    queryKey: ['manifest', projectId, runId],
    queryFn: () => api.runs.manifest(projectId, runId),
    enabled: !!runId,
    staleTime: Infinity,
  })

  const kpis = useQuery({
    queryKey: ['kpis', projectId, runId],
    queryFn: () => api.runs.aggregate<KPISet>(projectId, runId, 'kpi.json'),
    enabled: !!runId,
    staleTime: Infinity,
  })

  const series = useQuery({
    queryKey: ['series', projectId, runId],
    queryFn: () => api.runs.aggregate<SeriesFile[]>(projectId, runId, 'series.json'),
    enabled: !!runId && sections.has('throughput'),
    staleTime: Infinity,
  })

  const gantt = useQuery({
    queryKey: ['gantt', projectId, runId],
    queryFn: () => api.runs.aggregate<GanttRow[]>(projectId, runId, 'gantt.json'),
    enabled: !!runId && sections.has('utilisation') && !!manifest.data?.available.gantt,
    staleTime: Infinity,
  })

  // The report is ready when nothing it depends on is still loading. isLoading
  // is the right signal because a disabled section — a query switched off
  // because its data is not in the report — reads as not loading, so an
  // unselected block never holds the export open. A missing run is settled too:
  // it renders the empty note rather than hanging.
  const settled =
    !scenario.isLoading &&
    !runs.isLoading &&
    !manifest.isLoading &&
    !kpis.isLoading &&
    !series.isLoading &&
    !gantt.isLoading
  usePrintGate(settled)

  const params = useMemo(() => scenarioParams(scenario.data), [scenario.data])
  const seriesByKey = useMemo(() => {
    const map = new Map<string, SeriesFile>()
    for (const s of series.data ?? []) map.set(s.key, s)
    return map
  }, [series.data])

  if (!runId && runs.isFetched) {
    return (
      <div className="print-empty">
        <h2>{scenario.data?.name ?? 'Scenario'}</h2>
        <p>This scenario has no finished run, so there is nothing to report yet.</p>
      </div>
    )
  }

  const allKPIs = kpis.data?.kpis ?? []
  const headlines = allKPIs.filter((k) => k.headline)
  const grouped = groupKPIs(allKPIs)
  const m = manifest.data

  const arrivals = seriesByKey.get('arrivals')
  const departures = seriesByKey.get('departures')
  const wip = seriesByKey.get('wip')

  return (
    <>
      {sections.has('summary') && (
        <section className="card print-block">
          <h3>Headline</h3>
          {headlines.length > 0 ? (
            <div className="kpi-row">
              {headlines.map((kpi) => (
                <StatTile key={kpi.key} kpi={kpi} />
              ))}
            </div>
          ) : (
            <p className="chart-note">This run reported no headline measures.</p>
          )}

          {params.length > 0 && (
            <table className="print-params">
              <thead>
                <tr>
                  <th>Parameter</th>
                  <th className="num">Value</th>
                </tr>
              </thead>
              <tbody>
                {params.map(([key, value]) => (
                  <tr key={key}>
                    <td className="mono">{key}</td>
                    <td className="num mono">{value}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      )}

      {sections.has('throughput') && arrivals && departures && (
        <section className="card print-block">
          <div className="card-head">
            <h3 style={{ flex: 1 }}>Demand and completion</h3>
            <span className="faint" style={{ fontSize: 12 }}>
              per {formatDuration(arrivals.bucketSeconds)}
            </span>
          </div>
          <LineChart
            series={[
              { key: 'arrivals', label: 'Arrived', values: arrivals.values, slot: 0 },
              { key: 'departures', label: 'Completed', values: departures.values, slot: 1 },
            ]}
            startTime={arrivals.startTime}
            bucketSeconds={arrivals.bucketSeconds}
            warmUpUntil={m?.warmUp ? m.startTime + m.warmUp : undefined}
          />
          {wip && (
            <LineChart
              series={[{ key: 'wip', label: 'In the system', values: wip.values, slot: 0 }]}
              startTime={wip.startTime}
              bucketSeconds={wip.bucketSeconds}
              area
              warmUpUntil={m?.warmUp ? m.startTime + m.warmUp : undefined}
            />
          )}
        </section>
      )}

      {sections.has('utilisation') && gantt.data && gantt.data.length > 0 && m && (
        <section className="card print-block">
          <h3 style={{ marginBottom: 10 }}>Resources over time</h3>
          <GanttChart rows={gantt.data} startTime={m.startTime} endTime={m.endTime} />
          <p className="chart-note">
            A row that stays bright is the constraint. Capacity added anywhere else moves work to it
            faster without getting more through.
          </p>
        </section>
      )}

      {sections.has('heatmap') && m?.available.heatmaps && (
        <ReportHeatmap projectId={projectId} runId={runId} />
      )}

      {sections.has('kpis') && grouped.length > 0 && (
        <section className="card print-block">
          <h3 style={{ marginBottom: 12 }}>All measurements</h3>
          {grouped.map(([group, list]) => (
            <div key={group} className="kpi-group">
              <div className="kpi-group-title">{group}</div>
              <table>
                <tbody>
                  {list.map((kpi) => (
                    <tr key={kpi.key}>
                      <td>{kpi.label}</td>
                      <td className="num mono" style={{ width: 130 }}>
                        {formatKPI(kpi)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))}
        </section>
      )}

      {m && (
        <section className="card print-block print-provenance">
          <h3 style={{ marginBottom: 10 }}>How this run was made</h3>
          <table>
            <tbody>
              <tr>
                <td className="dim">Model</td>
                <td className="num">{m.modelName}</td>
              </tr>
              <tr>
                <td className="dim">Seed</td>
                <td className="num mono">{m.seed}</td>
              </tr>
              <tr>
                <td className="dim">Engine</td>
                <td className="num mono">{m.engineVersion}</td>
              </tr>
              <tr>
                <td className="dim">Simulated</td>
                <td className="num">{formatDuration(m.endTime - m.startTime)}</td>
              </tr>
              {m.warmUp ? (
                <tr>
                  <td className="dim">Warm-up excluded</td>
                  <td className="num">{formatDuration(m.warmUp)}</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </section>
      )}
    </>
  )
}

function scenarioParams(scenario: Scenario | undefined): [string, string][] {
  if (!scenario) return []
  return Object.entries(scenario.params)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => [key, Number.isInteger(value) ? String(value) : value.toFixed(2)])
}

// groupKPIs keeps the measurement table grouped the way the analysis page
// groups it, so the report and the screen read the same.
function groupKPIs(kpis: RunKPI[]): [string, RunKPI[]][] {
  const groups = new Map<string, RunKPI[]>()
  for (const kpi of kpis) {
    if (kpi.headline) continue
    const list = groups.get(kpi.group || 'Other')
    if (list) list.push(kpi)
    else groups.set(kpi.group || 'Other', [kpi])
  }
  return [...groups.entries()]
}
