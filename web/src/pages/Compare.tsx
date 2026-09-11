import { Fragment, useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'

import { api } from '@/api/client'
import type { Comparison, ComparedKPI } from '@/api/types'
import { DistributionChart } from '@/charts/DistributionChart'
import { formatDuration } from '@/state/timeline'
import { HeatmapDiff } from './HeatmapDiff'

/**
 * Comparing scenarios.
 *
 * The whole page is built around one idea: a simulation is stochastic, so a
 * difference between two scenarios means nothing until it is large compared
 * with how much each scenario varies against itself. Every number here is
 * therefore reported with what is known about it, and a change that sits
 * inside the run-to-run spread is labelled as such rather than presented as a
 * result.
 */
export function Compare() {
  const { projectId } = useParams<{ projectId: string }>()
  const [params, setParams] = useSearchParams()

  const selected = useMemo(
    () => (params.get('scenarios') ?? '').split(',').filter(Boolean),
    [params],
  )

  const scenarios = useQuery({
    queryKey: ['scenarios', projectId],
    queryFn: () => api.scenarios.list(projectId!),
    enabled: !!projectId,
  })

  const comparison = useQuery({
    queryKey: ['compare', projectId, selected.join(',')],
    queryFn: () => api.compare(projectId!, selected),
    enabled: !!projectId && selected.length >= 2,
  })

  const toggle = (id: string) => {
    const next = selected.includes(id) ? selected.filter((s) => s !== id) : [...selected, id]
    setParams(next.length ? { scenarios: next.join(',') } : {})
  }

  if (!projectId) return null

  return (
    <div className="page">
      <div className="page-narrow">
        <div className="page-header">
          <div>
            <h1>Compare scenarios</h1>
            <div className="sub">
              Pick two or more. Differences are judged against how much each scenario varies
              against itself.
            </div>
          </div>
          <div className="spacer" />
          <Link to={`/projects/${projectId}`} className="btn ghost">
            Back to the project
          </Link>
        </div>

        <div className="card">
          <h3 style={{ marginBottom: 10 }}>Scenarios</h3>

          <div className="picker">
            {(scenarios.data ?? []).map((scenario) => {
              const runs = scenario.runCount ?? 0
              const chosen = selected.includes(scenario.id)

              return (
                <button
                  key={scenario.id}
                  className={`picker-item ${chosen ? 'on' : ''}`}
                  onClick={() => toggle(scenario.id)}
                  disabled={runs === 0 && !chosen}
                  title={runs === 0 ? 'This scenario has no runs yet.' : undefined}
                >
                  <span className="picker-name">{scenario.name}</span>
                  <span className="picker-meta">
                    {runs === 0 ? 'never run' : `${runs} run${runs > 1 ? 's' : ''}`}
                  </span>
                </button>
              )
            })}
          </div>

          {selected.length < 2 && (
            <p className="chart-note">
              Choose at least two. The most useful comparison changes exactly one parameter, so
              that any difference in the results has only one thing it could be attributed to.
            </p>
          )}
        </div>

        {comparison.isLoading && (
          <div className="loading-page" style={{ height: 160 }}>
            <span className="spinner" />
          </div>
        )}

        {comparison.error && (
          <div className="banner error">
            {comparison.error instanceof Error ? comparison.error.message : 'Could not compare those.'}
          </div>
        )}

        {comparison.data && <ComparisonBody projectId={projectId} data={comparison.data} />}
      </div>
    </div>
  )
}

function ComparisonBody({ projectId, data }: { projectId: string; data: Comparison }) {
  const [expanded, setExpanded] = useState<string | null>(null)

  const baseline = data.scenarios.find((s) => s.id === data.baselineId)
  const others = data.scenarios.filter((s) => s.id !== data.baselineId)

  const headline = data.kpis.filter((k) => k.headline)
  const grouped = useMemo(() => {
    const groups = new Map<string, ComparedKPI[]>()
    for (const kpi of data.kpis) {
      const list = groups.get(kpi.group || 'Other')
      if (list) list.push(kpi)
      else groups.set(kpi.group || 'Other', [kpi])
    }
    return [...groups.entries()]
  }, [data.kpis])

  const differing = data.params.filter((p) => p.differs)
  const minReplications = Math.min(...data.scenarios.map((s) => s.replications))

  return (
    <>
      {data.warnings?.map((warning, i) => (
        <div key={i} className="banner warn">
          {warning}
        </div>
      ))}

      <div className={`banner ${minReplications < 2 ? 'warn' : 'info'}`}>{data.note}</div>

      <div className="card">
        <div className="card-head">
          <h3 style={{ flex: 1 }}>What differs</h3>
          <span className="faint" style={{ fontSize: 12 }}>
            {data.scenarios.map((s) => `${s.name}: ${s.replications} run${s.replications > 1 ? 's' : ''}`).join(' · ')}
          </span>
        </div>

        {differing.length === 0 ? (
          <div className="banner warn" style={{ marginBottom: 0 }}>
            These scenarios have identical parameters. Any difference in the results below is
            run-to-run variation by construction, not an effect of anything you changed.
          </div>
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

        {data.params.length > differing.length && (
          <p className="chart-note">
            {data.params.length - differing.length} other parameter
            {data.params.length - differing.length > 1 ? 's are' : ' is'} the same across every
            scenario.
          </p>
        )}
      </div>

      {headline.length > 0 && baseline && (
        <div className="card">
          <h3 style={{ marginBottom: 12 }}>Headline</h3>
          <DeltaTable
            kpis={headline}
            data={data}
            expanded={expanded}
            onExpand={setExpanded}
          />
        </div>
      )}

      <div className="card">
        <h3 style={{ marginBottom: 12 }}>All measurements</h3>

        {grouped.map(([group, list]) => (
          <div key={group} className="kpi-group">
            <div className="kpi-group-title">{group}</div>
            <DeltaTable kpis={list} data={data} expanded={expanded} onExpand={setExpanded} />
          </div>
        ))}
      </div>

      {others.length === 1 && baseline && (
        <HeatmapDiff
          projectId={projectId}
          baseline={{ id: baseline.sampleRunId, name: baseline.name }}
          other={{ id: others[0].sampleRunId, name: others[0].name }}
        />
      )}
    </>
  )
}

/**
 * The delta table.
 *
 * A row shows every scenario's mean and, against the baseline, the change with
 * a verdict. The verdict is the point: "within noise" is a finding, and saying
 * it plainly is what stops a reader acting on a number that is not there.
 */
function DeltaTable({
  kpis,
  data,
  expanded,
  onExpand,
}: {
  kpis: ComparedKPI[]
  data: Comparison
  expanded: string | null
  onExpand: (key: string | null) => void
}) {
  const others = data.scenarios.filter((s) => s.id !== data.baselineId)

  return (
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
        {kpis.map((kpi) => {
          const format = (v: number) => formatValue(v, kpi)
          const open = expanded === kpi.key

          return (
            // The key belongs on the fragment: a measure is one logical row
            // that happens to render as two when it is open.
            <Fragment key={kpi.key}>
              <tr className="delta-row" onClick={() => onExpand(open ? null : kpi.key)}>
                <td>
                  <span className="delta-name">{kpi.label}</span>
                  <span className="delta-expand">{open ? '▾' : '▸'}</span>
                </td>

                {data.scenarios.map((s) => {
                  const sample = kpi.samples[s.id]
                  return (
                    <td key={s.id} className="num mono">
                      {sample ? format(sample.mean) : '—'}
                      {sample?.hasInterval && (
                        <div className="faint delta-ci">
                          ± {format(sample.ciHigh - sample.mean).replace(/^[^0-9.-]*/, '')}
                        </div>
                      )}
                    </td>
                  )
                })}

                {others.map((s) => {
                  const diff = kpi.differences[s.id]
                  if (!diff) return <td key={s.id} className="num">—</td>

                  return (
                    <td key={s.id} className="num">
                      <Verdict diff={diff} />
                    </td>
                  )
                })}
              </tr>

              {open && (
                <tr>
                  <td colSpan={1 + data.scenarios.length + others.length} className="delta-detail">
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
                      format={format}
                    />

                    {others.map((s) => {
                      const diff = kpi.differences[s.id]
                      if (!diff?.hasInterval) return null

                      return (
                        <p key={s.id} className="chart-note">
                          {s.name} differs from {data.scenarios.find((x) => x.id === data.baselineId)?.name}{' '}
                          by {format(diff.delta)}, and the true difference is between{' '}
                          {format(diff.ciLow)} and {format(diff.ciHigh)} at 95% confidence.{' '}
                          {diff.distinguishable
                            ? 'That range excludes zero, so the difference is larger than the run-to-run variation.'
                            : 'That range includes zero, so these scenarios are not distinguishable on this measure at this number of replications.'}
                        </p>
                      )
                    })}
                  </td>
                </tr>
              )}
            </Fragment>
          )
        })}
      </tbody>
    </table>
  )
}

/**
 * The verdict on one change.
 *
 * Three states, not two. A difference can be an improvement, a regression, or
 * too small to call; and a measure with no declared direction, such as a count
 * of arrivals, gets a magnitude and no judgement at all.
 */
function Verdict({ diff }: { diff: Comparison['kpis'][number]['differences'][string] }) {
  if (!diff.hasInterval) {
    return (
      <span className="verdict unknown" title="Too few replications to tell signal from noise.">
        {formatPercent(diff.percent)}
        <span className="verdict-word">not testable</span>
      </span>
    )
  }

  if (!diff.distinguishable) {
    return (
      <span className="verdict noise" title="The confidence interval for the difference includes zero.">
        {formatPercent(diff.percent)}
        <span className="verdict-word">within noise</span>
      </span>
    )
  }

  if (diff.better === undefined) {
    return (
      <span className="verdict neutral" title="Real, but this measure has no better or worse direction.">
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
  const sign = value > 0 ? '+' : ''
  return `${sign}${value.toFixed(1)}%`
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
