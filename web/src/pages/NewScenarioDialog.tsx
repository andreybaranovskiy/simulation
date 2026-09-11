import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'

import { ApiError, api } from '@/api/client'
import type { TemplateParam } from '@/api/types'

/**
 * Creating a scenario in one step: pick a template, tune its parameters, name
 * it.
 *
 * The model is created behind the scenes if the project does not already have
 * one for that template. Making the user create a model and then a scenario
 * against it would be two steps with no decision in the first one.
 */
export function NewScenarioDialog({
  projectId,
  onClose,
  onCreated,
}: {
  projectId: string
  onClose: () => void
  onCreated: () => void
}) {
  const templates = useQuery({ queryKey: ['templates'], queryFn: () => api.templates.list() })
  const models = useQuery({
    queryKey: ['models', projectId],
    queryFn: () => api.models.list(projectId),
  })

  const [templateKey, setTemplateKey] = useState<string>('')
  const [name, setName] = useState('')
  const [values, setValues] = useState<Record<string, number>>({})
  const [seed, setSeed] = useState<string>('')
  const [replications, setReplications] = useState(1)
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})

  const template = useMemo(
    () => templates.data?.find((t) => t.key === templateKey),
    [templates.data, templateKey],
  )

  // Default to the first template, and reset the parameters whenever the
  // template changes: values from one template mean nothing in another.
  useEffect(() => {
    if (!templates.data?.length) return
    if (!templateKey) setTemplateKey(templates.data[0].key)
  }, [templates.data, templateKey])

  useEffect(() => {
    if (!template) return
    const defaults: Record<string, number> = {}
    for (const p of template.params) defaults[p.id] = p.default
    setValues(defaults)
    if (!name) setName(template.name)
  }, [template])

  const create = useMutation({
    mutationFn: async () => {
      if (!template) throw new Error('Choose a template.')

      // Reuse the project's model for this template if it has one, so a
      // project does not accumulate a model per scenario.
      const existing = models.data?.find(
        (m) => m.source === 'template' && m.templateKey === template.key,
      )

      const model =
        existing ??
        (await api.models.createFromTemplate(projectId, template.key, template.name, template.description))

      // Only parameters that differ from the template's defaults are stored.
      // A scenario then reads as what it changed, which is what a comparison
      // is about.
      const changed: Record<string, number> = {}
      for (const p of template.params) {
        if (values[p.id] !== undefined && values[p.id] !== p.default) changed[p.id] = values[p.id]
      }

      return api.scenarios.create(projectId, {
        modelId: model.id,
        name: name.trim() || template.name,
        params: changed,
        seed: seed.trim() ? Number(seed) : null,
        replications,
      })
    },
    onSuccess: onCreated,
    onError: (err) => {
      if (err instanceof ApiError) setFieldErrors(err.fields ?? {})
    },
  })

  const groups = useMemo(() => groupParams(template?.params ?? []), [template])
  const changedCount = template
    ? template.params.filter((p) => values[p.id] !== undefined && values[p.id] !== p.default).length
    : 0

  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div className="card-head">
        <h2 style={{ flex: 1 }}>New scenario</h2>
        <button className="btn ghost small" onClick={onClose}>
          Cancel
        </button>
      </div>

      {create.error && (
        <div className="banner error">
          {create.error instanceof Error ? create.error.message : 'Could not create the scenario.'}
        </div>
      )}
      {fieldErrors.params && <div className="banner error">{fieldErrors.params}</div>}

      <div className="row wrap" style={{ alignItems: 'flex-start', gap: 16, marginBottom: 16 }}>
        <div style={{ flex: '1 1 260px' }}>
          <div className="field">
            <label htmlFor="template">Model template</label>
            <select id="template" value={templateKey} onChange={(e) => setTemplateKey(e.target.value)}>
              {templates.data?.map((t) => (
                <option key={t.key} value={t.key}>
                  {t.name}
                </option>
              ))}
            </select>
            {template && <div className="hint">{template.description}</div>}
          </div>
        </div>

        <div style={{ flex: '1 1 200px' }}>
          <div className="field">
            <label htmlFor="scenario-name">Scenario name</label>
            <input
              id="scenario-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Six gate lanes"
            />
            {fieldErrors.name && <div className="error">{fieldErrors.name}</div>}
          </div>
        </div>

        <div style={{ flex: '0 0 120px' }}>
          <div className="field">
            <label htmlFor="seed">Seed</label>
            <input
              id="seed"
              value={seed}
              onChange={(e) => setSeed(e.target.value.replace(/[^0-9]/g, ''))}
              placeholder="random"
            />
            <div className="hint">Fix it to make the run reproducible.</div>
          </div>
        </div>

        <div style={{ flex: '0 0 110px' }}>
          <div className="field">
            <label htmlFor="replications">Replications</label>
            <input
              id="replications"
              type="number"
              min={1}
              max={50}
              value={replications}
              onChange={(e) => setReplications(Math.max(1, Math.min(50, Number(e.target.value) || 1)))}
            />
            <div className="hint">Repeats with different seeds.</div>
          </div>
        </div>
      </div>

      {template && (
        <>
          <div className="row" style={{ marginBottom: 10 }}>
            <h3 style={{ flex: 1 }}>Parameters</h3>
            <span className="faint" style={{ fontSize: 12 }}>
              {changedCount === 0 ? 'All at defaults' : `${changedCount} changed`}
            </span>
          </div>

          {groups.map(([group, params]) => (
            <div key={group} style={{ marginBottom: 14 }}>
              <div className="faint" style={{ fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.06em', marginBottom: 8 }}>
                {group}
              </div>

              <div className="grid" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(230px, 1fr))', gap: 12 }}>
                {params.map((param) => (
                  <ParamControl
                    key={param.id}
                    param={param}
                    value={values[param.id] ?? param.default}
                    onChange={(v) => setValues((prev) => ({ ...prev, [param.id]: v }))}
                  />
                ))}
              </div>
            </div>
          ))}
        </>
      )}

      <div className="row" style={{ marginTop: 8 }}>
        <button
          className="btn primary"
          onClick={() => create.mutate()}
          disabled={create.isPending || !template}
        >
          {create.isPending && <span className="spinner" />}
          Create scenario
        </button>
        <button className="btn ghost" onClick={onClose}>
          Cancel
        </button>
      </div>
    </div>
  )
}

export function ParamControl({
  param,
  value,
  onChange,
}: {
  param: TemplateParam
  value: number
  onChange: (value: number) => void
}) {
  const changed = value !== param.default
  const step = param.step ?? (param.integer ? 1 : (param.max - param.min) / 100)

  return (
    <div className="field" style={{ marginBottom: 0 }}>
      <label htmlFor={param.id} title={param.description}>
        {param.label}
        {changed && (
          <span style={{ color: 'var(--accent)', marginLeft: 6 }}>
            · was {formatValue(param.default, param)}
          </span>
        )}
      </label>

      <div className="row" style={{ gap: 8 }}>
        <input
          type="range"
          id={`${param.id}-range`}
          min={param.min}
          max={param.max}
          step={step}
          value={value}
          onChange={(e) => onChange(Number(e.target.value))}
          style={{ flex: 1 }}
        />
        <input
          id={param.id}
          type="number"
          min={param.min}
          max={param.max}
          step={step}
          value={value}
          onChange={(e) => onChange(clampToParam(Number(e.target.value), param))}
          style={{ width: 78, flex: 'none' }}
        />
        {param.unit && (
          <span className="faint" style={{ fontSize: 11, minWidth: 22 }}>
            {param.unit}
          </span>
        )}
      </div>
    </div>
  )
}

function clampToParam(value: number, param: TemplateParam): number {
  if (Number.isNaN(value)) return param.default
  const clamped = Math.min(param.max, Math.max(param.min, value))
  return param.integer ? Math.round(clamped) : clamped
}

function formatValue(value: number, param: TemplateParam): string {
  const text = param.integer ? String(Math.round(value)) : String(Number(value.toFixed(2)))
  return param.unit ? `${text} ${param.unit}` : text
}

export function groupParams(params: TemplateParam[]): Array<[string, TemplateParam[]]> {
  const groups = new Map<string, TemplateParam[]>()
  for (const param of params) {
    const key = param.group || 'Settings'
    const list = groups.get(key)
    if (list) list.push(param)
    else groups.set(key, [param])
  }
  return [...groups.entries()]
}
