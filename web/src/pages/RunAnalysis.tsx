import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { KPISet, Manifest, RunKPI } from '@/api/types'
import { GanttChart, type GanttRow } from '@/charts/GanttChart'
import { LineChart } from '@/charts/LineChart'
import { Meter, StatTile, formatKPI } from '@/charts/StatTile'
import { formatDuration } from '@/state/timeline'

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
 * The analysis view: what the run measured, as opposed to what it looked like.
 *
 * The order is the order a reader needs it in. The headline numbers first,
 * because most visits end there. Then where the time went, then the resources,
 * then the detail. Nothing here needs the playback engine, so it loads and
 * reads while a large run is still streaming its chunks.
 */
export function RunAnalysis({
  projectId,
  runId,
  manifest,
}: {
  projectId: string
  runId: string
  manifest: Manifest
}) {
  const kpis = useQuery({
    queryKey: ['kpis', projectId, runId],
    queryFn: () => api.runs.aggregate<KPISet>(projectId, runId, 'kpi.json'),
    staleTime: Infinity,
  })

  const series = useQuery({
    queryKey: ['series', projectId, runId],
    queryFn: () => api.runs.aggregate<SeriesFile[]>(projectId, runId, 'series.json'),
    staleTime: Infinity,
  })

  const gantt = useQuery({
    queryKey: ['gantt', projectId, runId],
    queryFn: () => api.runs.aggregate<GanttRow[]>(projectId, runId, 'gantt.json'),
    enabled: manifest.available.gantt,
    staleTime: Infinity,
  })

  const byKey = useMemo(() => {
    const map = new Map<string, RunKPI>()
    for (const kpi of kpis.data?.kpis ?? []) map.set(kpi.key, kpi)
    return map
  }, [kpis.data])

  const seriesByKey = useMemo(() => {
    const map = new Map<string, SeriesFile>()
    for (const s of series.data ?? []) map.set(s.key, s)
    return map
  }, [series.data])

  const headlines = (kpis.data?.kpis ?? []).filter((k) => k.headline)
  const grouped = groupKPIs(kpis.data?.kpis ?? [])

  if (kpis.isLoading) {
    return (
      <div className="loading-page" style={{ height: 220 }}>
        <span className="spinner" />
      </div>
    )
  }

  const throughput = seriesByKey.get('departures')
  const arrivals = seriesByKey.get('arrivals')
  const wip = seriesByKey.get('wip')

  return (
    <div className="analysis">
      {(kpis.data?.notes ?? []).map((note, index) => (
        <div key={index} className="banner warn">
          {note}
        </div>
      ))}

      {manifest.warnings?.map((warning, index) => (
        <div key={`m${index}`} className="banner warn">
          {warning}
        </div>
      ))}

      {headlines.length > 0 && (
        <section className="card">
          <h3>Headline</h3>
          <div className="kpi-row">
            {headlines.map((kpi) => (
              <StatTile key={kpi.key} kpi={kpi} />
            ))}
          </div>
        </section>
      )}

      {/* Demand against completion answers the first question anyone asks of a
          run: did the system keep up? Two series, so a legend is present and
          both ends are directly labelled. */}
      {arrivals && throughput && (
        <section className="card">
          <div className="card-head">
            <h3 style={{ flex: 1 }}>Demand and completion</h3>
            <span className="faint" style={{ fontSize: 12 }}>
              per {formatDuration(arrivals.bucketSeconds)}
            </span>
          </div>

          <LineChart
            series={[
              { key: 'arrivals', label: 'Arrived', values: arrivals.values, slot: 0 },
              { key: 'departures', label: 'Completed', values: throughput.values, slot: 1 },
            ]}
            startTime={arrivals.startTime}
            bucketSeconds={arrivals.bucketSeconds}
            warmUpUntil={manifest.warmUp ? manifest.startTime + manifest.warmUp : undefined}
          />

          <p className="chart-note">
            A completion line that sits below the arrival line for a sustained stretch means work is
            accumulating, not that it is being lost. The gap shows up as a rising count in the
            system below. The last bucket usually spikes: everything still in the model when the
            horizon arrives leaves at that moment, which is an artefact of the run ending rather
            than a burst of work getting done.
          </p>
        </section>
      )}

      {wip && (
        <section className="card">
          <div className="card-head">
            <h3 style={{ flex: 1 }}>{byKey.get('wip.mean')?.label ?? 'In the system'}</h3>
            {byKey.get('wip.peak') && (
              <span className="faint" style={{ fontSize: 12 }}>
                peak {formatKPI(byKey.get('wip.peak')!)}
              </span>
            )}
          </div>

          <LineChart
            series={[{ key: 'wip', label: 'In the system', values: wip.values, slot: 0 }]}
            startTime={wip.startTime}
            bucketSeconds={wip.bucketSeconds}
            area
            warmUpUntil={manifest.warmUp ? manifest.startTime + manifest.warmUp : undefined}
          />
        </section>
      )}

      {gantt.data && gantt.data.length > 0 && (
        <section className="card">
          <div className="card-head">
            <h3 style={{ flex: 1 }}>Resources over time</h3>
          </div>

          <GanttChart rows={gantt.data} startTime={manifest.startTime} endTime={manifest.endTime} />

          <p className="chart-note">
            A row that stays bright is the constraint. Adding capacity anywhere else moves work to
            it faster without getting more through.
          </p>
        </section>
      )}

      {/* Per-resource detail as small multiples rather than five lines on one
          chart: each resource is its own story, and one hue per facet beats
          five colours a reader has to match against a legend. */}
      <ResourceFacets manifest={manifest} series={series.data ?? []} kpis={byKey} />

      {grouped.length > 0 && (
        <section className="card">
          <h3 style={{ marginBottom: 12 }}>All measurements</h3>

          {grouped.map(([group, list]) => (
            <div key={group} className="kpi-group">
              <div className="kpi-group-title">{group}</div>
              <table>
                <tbody>
                  {list.map((kpi) => (
                    <tr key={kpi.key}>
                      <td>
                        {kpi.label}
                        {kpi.description && <div className="kpi-desc">{kpi.description}</div>}
                      </td>
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

      <section className="card">
        <h3 style={{ marginBottom: 10 }}>How this run was made</h3>
        <table>
          <tbody>
            <tr>
              <td className="dim">Model</td>
              <td className="num">{manifest.modelName}</td>
            </tr>
            <tr>
              <td className="dim">Seed</td>
              <td className="num mono">{manifest.seed}</td>
            </tr>
            <tr>
              <td className="dim">Engine</td>
              <td className="num mono">{manifest.engineVersion}</td>
            </tr>
            <tr>
              <td className="dim">Simulated</td>
              <td className="num">{formatDuration(manifest.endTime - manifest.startTime)}</td>
            </tr>
            {manifest.warmUp ? (
              <tr>
                <td className="dim">Warm-up excluded</td>
                <td className="num">{formatDuration(manifest.warmUp)}</td>
              </tr>
            ) : null}
            <tr>
              <td className="dim">Events recorded</td>
              <td className="num mono">{manifest.counts.records.toLocaleString()}</td>
            </tr>
            <tr>
              <td className="dim">Built</td>
              <td className="num dim">{new Date(manifest.builtAt).toLocaleString()}</td>
            </tr>
          </tbody>
        </table>
        <p className="chart-note">
          The same seed and the same model always produce the same run, so any difference between
          two runs is a difference somebody chose.
        </p>
      </section>
    </div>
  )
}

/**
 * One facet per resource.
 *
 * Small multiples rather than a five-line chart. Each facet carries one series,
 * so it needs no legend and no colour matching, and comparison happens by
 * position, which is the channel people read most accurately.
 */
function ResourceFacets({
  manifest,
  series,
  kpis,
}: {
  manifest: Manifest
  series: SeriesFile[]
  kpis: Map<string, RunKPI>
}) {
  const facets = useMemo(() => {
    const byKey = new Map(series.map((s) => [s.key, s]))

    return (manifest.resources ?? [])
      .map((resource) => ({
        resource,
        queue: byKey.get(`queue.${resource.id}`),
        utilisation: kpis.get(`util.${resource.id}`),
        peakQueue: kpis.get(`queue.peak.${resource.id}`),
        wait: kpis.get(`wait.p95.${resource.id}`),
      }))
      .filter((f) => f.queue || f.utilisation)
  }, [manifest.resources, series, kpis])

  if (facets.length === 0) return null

  // Every facet shares one y scale, or their heights would not be comparable
  // and the whole point of the form would be lost.
  const queueMax = Math.max(
    1,
    ...facets.flatMap((f) => f.queue?.values ?? []).filter((v) => Number.isFinite(v)),
  )

  return (
    <section className="card">
      <div className="card-head">
        <h3 style={{ flex: 1 }}>Queue at each resource</h3>
        <span className="faint" style={{ fontSize: 12 }}>
          same scale across all
        </span>
      </div>

      <div className="facets">
        {facets.map((facet) => (
          <div key={facet.resource.id} className="facet">
            <div className="facet-head">
              <span className="facet-title">{facet.resource.label}</span>
              {facet.utilisation && (
                <span className="facet-stat">{facet.utilisation.value.toFixed(0)}% busy</span>
              )}
            </div>

            {facet.utilisation && (
              <Meter
                label="In use"
                fraction={facet.utilisation.value / 100}
                caption={
                  facet.wait && facet.wait.value > 0
                    ? `95th percentile wait ${formatDuration(facet.wait.value)}`
                    : undefined
                }
              />
            )}

            {facet.queue && (
              <LineChart
                series={[{ key: facet.resource.id, label: 'Waiting', values: facet.queue.values, slot: 0 }]}
                startTime={facet.queue.startTime}
                bucketSeconds={facet.queue.bucketSeconds}
                height={100}
                yMax={queueMax}
                area
              />
            )}
          </div>
        ))}
      </div>
    </section>
  )
}

/** Groups the KPI table, keeping resource-specific rows out of the top level. */
function groupKPIs(kpis: RunKPI[]): Array<[string, RunKPI[]]> {
  const groups = new Map<string, RunKPI[]>()

  for (const kpi of kpis) {
    const key = kpi.group || 'Other'
    const list = groups.get(key)
    if (list) list.push(kpi)
    else groups.set(key, [kpi])
  }

  return [...groups.entries()]
}
