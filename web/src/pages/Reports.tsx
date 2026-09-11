import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api, ApiError } from '@/api/client'
import { REPORT_SECTIONS, type Report, type ReportInput, type ReportKind } from '@/api/types'
import { formatDate } from './Projects'

/**
 * The report builder.
 *
 * A report here is a saved definition, not a file: it names the scenarios and
 * the blocks to include, and the PDF is rendered on demand from the current run
 * data. So the page is about assembling and keeping definitions, with export as
 * the action on each, and a one-off export for a definition worth printing once
 * but not keeping.
 */
export function Reports() {
  const { projectId } = useParams<{ projectId: string }>()
  const [editing, setEditing] = useState<Report | 'new' | null>(null)

  const reports = useQuery({
    queryKey: ['reports', projectId],
    queryFn: () => api.reports.list(projectId!),
    enabled: !!projectId,
  })

  const scenarios = useQuery({
    queryKey: ['scenarios', projectId],
    queryFn: () => api.scenarios.list(projectId!),
    enabled: !!projectId,
  })

  const project = useQuery({
    queryKey: ['project', projectId],
    queryFn: () => api.projects.get(projectId!),
    enabled: !!projectId,
  })

  if (!projectId) return null

  const canEdit = project.data?.role === 'owner' || project.data?.role === 'editor'
  const runnable = (scenarios.data ?? []).filter((s) => (s.runCount ?? 0) > 0)

  return (
    <div className="page">
      <div className="page-narrow">
        <div className="page-header">
          <div>
            <h1>Reports</h1>
            <div className="sub">Saved report definitions, exported to PDF on demand.</div>
          </div>
          <div className="spacer" />
          {canEdit && editing === null && (
            <button className="btn primary" onClick={() => setEditing('new')}>
              New report
            </button>
          )}
        </div>

        {runnable.length === 0 ? (
          <div className="empty">
            <h2>Nothing to report yet</h2>
            <p>
              A report is built from scenarios that have finished runs.{' '}
              <Link to={`/projects/${projectId}`}>Run a scenario</Link> first.
            </p>
          </div>
        ) : (
          <>
            {editing !== null && (
              <ReportEditor
                projectId={projectId}
                report={editing === 'new' ? null : editing}
                scenarios={runnable.map((s) => ({ id: s.id, name: s.name }))}
                onClose={() => setEditing(null)}
              />
            )}

            {reports.isLoading && (
              <div className="loading-page" style={{ height: 120 }}>
                <span className="spinner" />
              </div>
            )}

            {reports.data && reports.data.length === 0 && editing === null && (
              <div className="empty">
                <h2>No reports</h2>
                <p>Create one to bundle a scenario's analysis or a comparison into a PDF.</p>
              </div>
            )}

            <div className="report-list">
              {(reports.data ?? []).map((report) => (
                <ReportCard
                  key={report.id}
                  projectId={projectId}
                  report={report}
                  canEdit={canEdit}
                  onEdit={() => setEditing(report)}
                />
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function ReportCard({
  projectId,
  report,
  canEdit,
  onEdit,
}: {
  projectId: string
  report: Report
  canEdit: boolean
  onEdit: () => void
}) {
  const queryClient = useQueryClient()
  const [exporting, setExporting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const remove = useMutation({
    mutationFn: () => api.reports.remove(projectId, report.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['reports', projectId] }),
  })

  const exportPdf = async () => {
    setError(null)
    setExporting(true)
    try {
      // A saved report streams straight from its own URL, so the browser owns
      // the download and its progress rather than buffering the file in JS.
      await downloadFromUrl(api.reports.pdfUrl(projectId, report.id))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Export failed.')
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="card report-card">
      <div className="report-card-head">
        <span className={`pill ${report.kind}`}>
          {report.kind === 'comparison' ? 'Comparison' : 'Scenario'}
        </span>
        <span className="faint" style={{ fontSize: 12 }}>
          updated {formatDate(report.updatedAt)}
        </span>
      </div>

      <h3>{report.name}</h3>
      {report.subtitle && <p className="report-subtitle">{report.subtitle}</p>}

      <div className="report-scenarios">
        {(report.scenarioNames ?? []).map((name, i) => (
          <span key={i} className="chip">
            {name}
          </span>
        ))}
      </div>

      {error && <div className="banner error">{error}</div>}

      <div className="report-actions">
        <button className="btn small primary" onClick={() => void exportPdf()} disabled={exporting}>
          {exporting && <span className="spinner" />}
          {exporting ? 'Rendering' : 'Export PDF'}
        </button>
        {canEdit && (
          <>
            <button className="btn small ghost" onClick={onEdit}>
              Edit
            </button>
            <button
              className="btn small danger"
              onClick={() => {
                if (confirm(`Delete "${report.name}"?`)) remove.mutate()
              }}
              disabled={remove.isPending}
            >
              Delete
            </button>
          </>
        )}
      </div>
    </div>
  )
}

interface ScenarioOption {
  id: string
  name: string
}

function ReportEditor({
  projectId,
  report,
  scenarios,
  onClose,
}: {
  projectId: string
  report: Report | null
  scenarios: ScenarioOption[]
  onClose: () => void
}) {
  const queryClient = useQueryClient()

  const [name, setName] = useState(report?.name ?? '')
  const [subtitle, setSubtitle] = useState(report?.subtitle ?? '')
  const [kind, setKind] = useState<ReportKind>(report?.kind ?? 'scenario')
  const [selected, setSelected] = useState<string[]>(report?.scenarioIds ?? [])
  const [sections, setSections] = useState<string[]>(report?.sections ?? [])
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [error, setError] = useState<string | null>(null)
  const [exporting, setExporting] = useState(false)

  // The kind cannot change once a report exists: a scenario report and a
  // comparison are different documents. On a new report it is free to switch,
  // which resets the section list to the new kind's default.
  const lockedKind = report !== null

  const availableSections = useMemo(
    () => Object.entries(REPORT_SECTIONS).filter(([, meta]) => meta.kinds.includes(kind)),
    [kind],
  )

  const effectiveSections = sections.length > 0 ? sections : availableSections.map(([id]) => id)

  const toggleScenario = (id: string) => {
    setSelected((current) => {
      if (kind === 'scenario') return current.includes(id) ? [] : [id]
      return current.includes(id) ? current.filter((s) => s !== id) : [...current, id]
    })
  }

  const toggleSection = (id: string) =>
    setSections((current) => {
      const base = current.length > 0 ? current : availableSections.map(([s]) => s)
      return base.includes(id) ? base.filter((s) => s !== id) : [...base, id]
    })

  const changeKind = (next: ReportKind) => {
    setKind(next)
    setSelected([])
    setSections([])
  }

  const input = (): ReportInput => ({
    name: name.trim(),
    subtitle: subtitle.trim(),
    kind,
    scenarioIds: selected,
    // An empty selection means "every section this kind supports"; sending the
    // resolved list keeps the order the toggles show.
    sections: sections.length > 0 ? orderSections(sections, availableSections.map(([s]) => s)) : [],
  })

  const validate = (): boolean => {
    const errs: Record<string, string> = {}
    if (!name.trim()) errs.name = 'A report needs a name.'
    if (kind === 'scenario' && selected.length !== 1) errs.scenarioIds = 'Pick one scenario.'
    if (kind === 'comparison' && selected.length < 2) errs.scenarioIds = 'Pick at least two scenarios.'
    setFieldErrors(errs)
    return Object.keys(errs).length === 0
  }

  const save = useMutation({
    mutationFn: () =>
      report
        ? api.reports.update(projectId, report.id, input())
        : api.reports.create(projectId, input()),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['reports', projectId] })
      onClose()
    },
    onError: (err) => {
      if (err instanceof ApiError && err.fields) setFieldErrors(err.fields)
      else setError(err instanceof Error ? err.message : 'Could not save the report.')
    },
  })

  const exportNow = async () => {
    if (!validate()) return
    setError(null)
    setExporting(true)
    try {
      const blob = await api.reports.exportAdhoc(projectId, input())
      downloadBlob(blob, `${slug(name)}.pdf`)
    } catch (err) {
      if (err instanceof ApiError && err.fields) setFieldErrors(err.fields)
      else setError(err instanceof Error ? err.message : 'Export failed.')
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="card report-editor" style={{ marginBottom: 16 }}>
      <div className="card-head">
        <h3 style={{ flex: 1 }}>{report ? 'Edit report' : 'New report'}</h3>
        <button className="btn ghost small" onClick={onClose}>
          Cancel
        </button>
      </div>

      {error && <div className="banner error">{error}</div>}

      <div className="field">
        <label>Kind</label>
        <div className="segmented">
          <button
            className={`segment ${kind === 'scenario' ? 'on' : ''}`}
            onClick={() => !lockedKind && changeKind('scenario')}
            disabled={lockedKind}
          >
            One scenario
          </button>
          <button
            className={`segment ${kind === 'comparison' ? 'on' : ''}`}
            onClick={() => !lockedKind && changeKind('comparison')}
            disabled={lockedKind}
          >
            Compare scenarios
          </button>
        </div>
        {lockedKind && <div className="hint">The kind is fixed once a report is created.</div>}
      </div>

      <div className="field">
        <label htmlFor="report-name">Name</label>
        <input
          id="report-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={kind === 'comparison' ? 'Third inspection bay vs. two' : 'Terminal baseline'}
        />
        {fieldErrors.name && <div className="error">{fieldErrors.name}</div>}
      </div>

      <div className="field">
        <label htmlFor="report-subtitle">Subtitle</label>
        <input
          id="report-subtitle"
          value={subtitle}
          onChange={(e) => setSubtitle(e.target.value)}
          placeholder="Optional. Shown under the title and in the footer."
        />
      </div>

      <div className="field">
        <label>
          {kind === 'scenario' ? 'Scenario' : 'Scenarios'}
          <span className="faint" style={{ marginLeft: 8, fontSize: 11 }}>
            {kind === 'comparison' ? 'the first is the baseline' : 'with a finished run'}
          </span>
        </label>
        <div className="picker">
          {scenarios.map((scenario) => {
            const on = selected.includes(scenario.id)
            const order = selected.indexOf(scenario.id)
            return (
              <button
                key={scenario.id}
                className={`picker-item ${on ? 'on' : ''}`}
                onClick={() => toggleScenario(scenario.id)}
              >
                <span className="picker-name">{scenario.name}</span>
                {kind === 'comparison' && on && (
                  <span className="picker-meta">{order === 0 ? 'baseline' : `#${order + 1}`}</span>
                )}
              </button>
            )
          })}
        </div>
        {fieldErrors.scenarioIds && <div className="error">{fieldErrors.scenarioIds}</div>}
      </div>

      <div className="field">
        <label>Sections</label>
        <div className="section-toggles">
          {availableSections.map(([id, meta]) => (
            <label key={id} className="section-toggle">
              <input
                type="checkbox"
                checked={effectiveSections.includes(id)}
                onChange={() => toggleSection(id)}
              />
              <span>
                <span className="section-name">{meta.label}</span>
                <span className="section-hint">{meta.hint}</span>
              </span>
            </label>
          ))}
        </div>
      </div>

      <div className="row" style={{ marginTop: 8, gap: 8 }}>
        <button className="btn primary" onClick={() => validate() && save.mutate()} disabled={save.isPending}>
          {save.isPending && <span className="spinner" />}
          {report ? 'Save changes' : 'Save report'}
        </button>
        <button className="btn ghost" onClick={() => void exportNow()} disabled={exporting}>
          {exporting && <span className="spinner" />}
          {exporting ? 'Rendering' : 'Export without saving'}
        </button>
      </div>
    </div>
  )
}

// orderSections returns the chosen sections in the canonical print order, so a
// report prints its blocks in a stable sequence regardless of toggle order.
function orderSections(chosen: string[], order: string[]): string[] {
  return order.filter((id) => chosen.includes(id))
}

async function downloadFromUrl(url: string) {
  // The endpoint answers with an attachment, but a bad request answers with
  // JSON; fetch it so an error surfaces as a message rather than a downloaded
  // file full of an error body.
  const response = await fetch(url, { credentials: 'same-origin' })
  if (!response.ok) {
    const text = await response.text()
    try {
      const body = JSON.parse(text)
      throw new Error(body.error ?? 'Export failed.')
    } catch {
      throw new Error('Export failed.')
    }
  }
  const disposition = response.headers.get('Content-Disposition') ?? ''
  const match = disposition.match(/filename="?([^"]+)"?/)
  downloadBlob(await response.blob(), match?.[1] ?? 'report.pdf')
}

function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  // Revoke on the next tick so the click has a chance to start the download.
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

function slug(name: string): string {
  const s = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return s || 'report'
}
