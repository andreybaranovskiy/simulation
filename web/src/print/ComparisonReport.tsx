import { useMemo } from 'react'
import { useQueries, useQuery } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { Difference } from '@/api/types'
import { DistributionChart } from '@/charts/DistributionChart'
import { LineChart } from '@/charts/LineChart'
import { formatDuration } from '@/state/timeline'
import { usePrintGate } from './PrintDocument'
import { HeatmapDiff } from '@/pages/HeatmapDiff'

interface SeriesFile {
  key: string
  startTime: number
  bucketSeconds: number
  values: number[]
}

/**
 * Several scenarios side by side, laid out for paper.
 *
 * It is the comparison page's analysis, in print order: what was changed, then
 * the spread each verdict rests on, then the full delta table, then throughput,
 * then where the two diverge on the ground. Every difference carries the same
 * three-state verdict the screen uses, because "within noise" is a finding and
 * a printed report is exactly where it must not quietly become a result.
 */
export function ComparisonReport({
  projectId,
  scenarioIds,
  sections,
}: {
  projectId: string
  scenarioIds: string[]
  sections: Set<string>
}) {
  const comparison = useQuery({
    queryKey: ['compare', projectId, scenarioIds.join(',')],
    queryFn: () => api.compare(projectId, scenarioIds),
    enabled: scenarioIds.length >= 2,
    staleTime: Infinity,
  })

  usePrintGate(!comparison.isLoading)

  const data = comparison.data
  if (!data) {
    return comparison.isFetched ? (
      <div className="print-empty">
        <h2>Comparison</h2>
        <p>These scenarios could not be compared. They need at least two with finished runs.</p>
      </div>
    ) : null
  }

  const headline = data.kpis.filter((k) => k.headline)
  const differing = data.params.filter((p) => p.differs)
  const others = data.scenarios.filter((s) => s.id !== data.baselineId)

  return (
    <>
      {data.warnings?.map((warning, i) => (
        <div key={i} className="banner warn print-block">
          {warning}
        </div>
      ))}

      {sections.has('summary') && (
        <section className="card print-block">
          <h3 style={{ marginBottom: 10 }}>What differs</h3>
          <p className="banner info" style={{ marginTop: 0 }}>
            {data.note}
          </p>

          {differing.length === 0 ? (
            <p className="chart-note">
              These scenarios share every parameter, so any difference below is run-to-run
              variation by construction.
            </p>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>Parameter</th>
                  {data.scenarios.map((s) => (
                    <th key={s.id} className="num">
                      {s.name}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {differing.map((param) => (
                  <tr key={param.id}>
                    <td className="mono">{param.id}</td>
                    {data.scenarios.map((s) => (
                      <td key={s.id} className="num mono">
                        {formatPlain(param.values[s.id])}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      )}

      {sections.has('distribution') && headline.length > 0 && (
        <section className="card print-block">
          <h3 style={{ marginBottom: 12 }}>Replication spread</h3>
          {headline.map((kpi) => (
            <div key={kpi.key} className="print-dist">
              <div className="kpi-group-title">{kpi.label}</div>
              <DistributionChart
                rows={data.scenarios.map((s) => {
                  const sample = kpi.samples[s.id]
                  return {
                    id: s.id,
                    label: s.name,
                    values: sample?.values ?? [],
                    mean: sample?.mean ?? 0,
                    ciLow: sample?.ciLow ?? 0,
                    ciHigh: sample?.ciHigh ?? 0,
                    hasInterval: sample?.hasInterval ?? false,
                  }
                })}
                baselineId={data.baselineId}
                unit={kpi.unit}
                format={(v) => formatValue(v, kpi)}
              />
            </div>
          ))}
        </section>
      )}

      {sections.has('kpis') && (
        <section className="card print-block">
          <h3 style={{ marginBottom: 12 }}>All measurements</h3>
          <table className="delta-table">
            <thead>
              <tr>
                <th>Measure</th>
                {data.scenarios.map((s) => (
                  <th key={s.id} className="num">
                    {s.name}
                    {s.id === data.baselineId && <div className="faint baseline-tag">baseline</div>}
                  </th>
                ))}
                {others.map((s) => (
                  <th key={`d-${s.id}`} className="num">
                    Change
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.kpis.map((kpi) => (
                <tr key={kpi.key}>
                  <td>{kpi.label}</td>
                  {data.scenarios.map((s) => (
                    <td key={s.id} className="num mono">
                      {kpi.samples[s.id] ? formatValue(kpi.samples[s.id].mean, kpi) : '—'}
                    </td>
                  ))}
                  {others.map((s) => (
                    <td key={s.id} className="num">
                      <Verdict diff={kpi.differences[s.id]} />
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      {sections.has('throughput') && (
        <ThroughputOverlay
          projectId={projectId}
          runs={data.scenarios.map((s) => ({ id: s.sampleRunId, name: s.name }))}
        />
      )}

      {sections.has('difference') && others.length === 1 && (
        <div className="print-block">
          <HeatmapDiff
            projectId={projectId}
            baseline={{
              id: data.scenarios.find((s) => s.id === data.baselineId)!.sampleRunId,
              name: data.scenarios.find((s) => s.id === data.baselineId)!.name,
            }}
            other={{ id: others[0].sampleRunId, name: others[0].name }}
          />
        </div>
      )}
    </>
  )
}

/**
 * Completions over time, one line per scenario.
 *
 * It reads each scenario's representative run, the same run the comparison drew
 * its sample from, so the overlay and the table agree. The line count is capped
 * at the palette's three separable slots; a wider comparison keeps its table
 * and drops the overlay rather than drawing lines a reader cannot tell apart.
 */
function ThroughputOverlay({
  projectId,
  runs,
}: {
  projectId: string
  runs: { id: string; name: string }[]
}) {
  const capped = runs.slice(0, 3)

  const results = useQueries({
    queries: capped.map((run) => ({
      queryKey: ['series', projectId, run.id],
      queryFn: () => api.runs.aggregate<SeriesFile[]>(projectId, run.id, 'series.json'),
      enabled: !!run.id,
      staleTime: Infinity,
    })),
  })

  const settled = results.every((r) => !r.isLoading)
  usePrintGate(settled)

  const lines = useMemo(() => {
    return capped
      .map((run, index) => {
        const file = results[index].data?.find((s) => s.key === 'departures')
        if (!file) return null
        return { run, file, slot: index }
      })
      .filter((x): x is { run: { id: string; name: string }; file: SeriesFile; slot: number } => !!x)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [results.map((r) => r.dataUpdatedAt).join(','), capped.length])

  if (lines.length < 2) return null

  const base = lines[0].file

  return (
    <section className="card print-block">
      <div className="card-head">
        <h3 style={{ flex: 1 }}>Completion over time</h3>
        <span className="faint" style={{ fontSize: 12 }}>
          per {formatDuration(base.bucketSeconds)}
        </span>
      </div>
      <LineChart
        series={lines.map((line) => ({
          key: line.run.id,
          label: line.run.name,
          values: line.file.values,
          slot: line.slot,
        }))}
        startTime={base.startTime}
        bucketSeconds={base.bucketSeconds}
      />
      {runs.length > 3 && (
        <p className="chart-note">
          Showing the first three scenarios. Beyond three, overlaid lines stop being reliably
          distinguishable; the table above carries every scenario.
        </p>
      )}
    </section>
  )
}

/** The three-state verdict, compact for a table cell. */
function Verdict({ diff }: { diff: Difference | undefined }) {
  if (!diff) return <>—</>

  if (!diff.hasInterval) {
    return (
      <span className="verdict unknown">
        {formatPercent(diff.percent)}
        <span className="verdict-word">not testable</span>
      </span>
    )
  }
  if (!diff.distinguishable) {
    return (
      <span className="verdict noise">
        {formatPercent(diff.percent)}
        <span className="verdict-word">within noise</span>
      </span>
    )
  }
  if (diff.better === undefined) {
    return (
      <span className="verdict neutral">
        {formatPercent(diff.percent)}
        <span className="verdict-word">changed</span>
      </span>
    )
  }
  return (
    <span className={`verdict ${diff.better ? 'better' : 'worse'}`}>
      {formatPercent(diff.percent)}
      <span className="verdict-word">{diff.better ? 'better' : 'worse'}</span>
    </span>
  )
}

function formatPercent(value: number): string {
  if (!Number.isFinite(value)) return '—'
  return `${value > 0 ? '+' : ''}${value.toFixed(1)}%`
}

function formatValue(value: number, kpi: { unit: string; decimals: number }): string {
  if (kpi.unit === 's') return formatDuration(value)
  if (kpi.unit === '%') return `${value.toFixed(kpi.decimals)}%`
  return value.toLocaleString(undefined, {
    minimumFractionDigits: kpi.decimals,
    maximumFractionDigits: kpi.decimals,
  })
}

function formatPlain(value: number | undefined): string {
  if (value === undefined) return '—'
  return Number.isInteger(value) ? String(value) : value.toFixed(2)
}
