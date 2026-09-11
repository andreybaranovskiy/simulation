import { useMemo } from 'react'
import { useParams, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'

import { api } from '@/api/client'
import { REPORT_SECTIONS, type Report, type ReportKind } from '@/api/types'
import { PrintDocument } from './PrintDocument'
import { ScenarioReport } from './ScenarioReport'
import { ComparisonReport } from './ComparisonReport'
import './print.css'

/**
 * The print route.
 *
 * A headless browser loads this to make a PDF, so it renders no app chrome and
 * no navigation, only the document. It resolves two forms: a saved report by
 * id, and an ad-hoc report whose whole definition arrives in the query string,
 * which is how the builder exports something it has not saved.
 *
 * The definition it resolves to is identical in both cases, so the documents
 * below never learn which form they came from.
 */
export function ReportPrint() {
  const { projectId, reportId } = useParams<{ projectId: string; reportId?: string }>()
  const [query] = useSearchParams()

  const saved = useQuery({
    queryKey: ['report', projectId, reportId],
    queryFn: () => api.reports.get(projectId!, reportId!),
    enabled: !!projectId && !!reportId,
    staleTime: Infinity,
  })

  // An ad-hoc report carries its definition in the URL; a saved one is fetched.
  // Both collapse to the same shape before anything is drawn.
  const definition = useMemo<ReportDefinition | null>(() => {
    if (reportId) {
      return saved.data ? fromReport(saved.data) : null
    }
    return fromQuery(query)
  }, [reportId, saved.data, query])

  if (!projectId) return null

  const title = definition?.name ?? 'Report'
  const generated = new Date().toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'long',
    day: 'numeric',
  })

  return (
    <PrintDocument title={title}>
      <header className="print-title">
        <div className="print-kicker">
          {definition?.kind === 'comparison' ? 'Scenario comparison' : 'Scenario report'}
        </div>
        <h1>{title}</h1>
        {definition?.subtitle && <p className="print-subtitle">{definition.subtitle}</p>}
        <p className="print-generated">Generated {generated}</p>
      </header>

      {!definition ? (
        <div className="print-empty">
          <p>{reportId && saved.isFetched ? 'That report could not be found.' : 'Loading…'}</p>
        </div>
      ) : definition.kind === 'comparison' ? (
        <ComparisonReport
          projectId={projectId}
          scenarioIds={definition.scenarioIds}
          sections={resolveSections(definition)}
        />
      ) : (
        <ScenarioReport
          projectId={projectId}
          scenarioId={definition.scenarioIds[0]}
          sections={resolveSections(definition)}
        />
      )}
    </PrintDocument>
  )
}

interface ReportDefinition {
  name: string
  subtitle: string
  kind: ReportKind
  scenarioIds: string[]
  sections: string[]
}

// resolveSections turns a definition's sections into the set the documents
// read. An empty list means "every block this kind supports", which is what
// the server means by it too, so a hand-built URL with no sections still prints
// a full report rather than a bare title.
function resolveSections(definition: ReportDefinition): Set<string> {
  if (definition.sections.length > 0) return new Set(definition.sections)
  const all = Object.entries(REPORT_SECTIONS)
    .filter(([, meta]) => meta.kinds.includes(definition.kind))
    .map(([id]) => id)
  return new Set(all)
}

function fromReport(report: Report): ReportDefinition {
  return {
    name: report.name,
    subtitle: report.subtitle,
    kind: report.kind,
    scenarioIds: report.scenarioIds,
    sections: report.sections,
  }
}

function fromQuery(query: URLSearchParams): ReportDefinition | null {
  const kind = query.get('kind') as ReportKind | null
  const scenarios = (query.get('scenarios') ?? '').split(',').filter(Boolean)
  if ((kind !== 'scenario' && kind !== 'comparison') || scenarios.length === 0) {
    return null
  }

  return {
    name: query.get('title') ?? 'Report',
    subtitle: query.get('subtitle') ?? '',
    kind,
    scenarioIds: scenarios,
    sections: (query.get('sections') ?? '').split(',').filter(Boolean),
  }
}
