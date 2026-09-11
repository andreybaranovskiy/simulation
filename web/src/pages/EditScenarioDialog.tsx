import { useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'

import { ApiError, api } from '@/api/client'
import type { Scenario, TemplateParam } from '@/api/types'
import { ParamControl, groupParams } from './NewScenarioDialog'

/**
 * Editing an existing scenario.
 *
 * A scenario is a model plus the values it changed, so this edits the values,
 * the seed, how many times it repeats, the plan it is viewed over, and its
 * name — not the model. Changing the model would make it a different scenario,
 * and a comparison rests on two scenarios sharing one model.
 *
 * The parameters it shows come from wherever the model defines them: a
 * template's parameter list, or a spec model's own declared parameters. A model
 * with no tunable parameters still edits everything else.
 */
export function EditScenarioDialog({
  projectId,
  scenario,
  onClose,
  onSaved,
}: {
  projectId: string
  scenario: Scenario
  onClose: () => void
  onSaved: () => void
}) {
  const model = useQuery({
    queryKey: ['model', projectId, scenario.modelId],
    queryFn: () => api.models.get(projectId, scenario.modelId),
  })

  const template = useQuery({
    queryKey: ['template', model.data?.templateKey],
    queryFn: () => api.templates.get(model.data!.templateKey!),
    enabled: model.data?.source === 'template' && !!model.data.templateKey,
  })

  const plans = useQuery({
    queryKey: ['plans', projectId],
    queryFn: () => api.plans.list(projectId),
  })

  const [name, setName] = useState(scenario.name)
  const [description, setDescription] = useState(scenario.description)
  const [seed, setSeed] = useState(scenario.seed !== undefined ? String(scenario.seed) : '')
  const [replications, setReplications] = useState(scenario.replications)
  const [sitePlanId, setSitePlanId] = useState(scenario.sitePlanId ?? '')
  const [values, setValues] = useState<Record<string, number>>({ ...scenario.params })
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})

  // The parameter definitions, from the template or the spec, so each control
  // knows its range, unit and default.
  const paramDefs = useMemo<TemplateParam[]>(() => {
    if (model.data?.source === 'template') return template.data?.params ?? []
    if (model.data?.source === 'spec') return specParams(model.data.spec)
    return []
  }, [model.data, template.data])

  const groups = useMemo(() => groupParams(paramDefs), [paramDefs])
  const defaults = useMemo(() => {
    const map: Record<string, number> = {}
    for (const p of paramDefs) map[p.id] = p.default
    return map
  }, [paramDefs])

  const changedCount = paramDefs.filter(
    (p) => (values[p.id] ?? p.default) !== p.default,
  ).length

  const save = useMutation({
    mutationFn: () => {
      // Store only what differs from the model's defaults, so a scenario reads
      // as what it changed. A parameter dragged back to its default drops out.
      const changed: Record<string, number> = {}
      for (const p of paramDefs) {
        const v = values[p.id]
        if (v !== undefined && v !== p.default) changed[p.id] = v
      }
      // A model with no known parameter definitions keeps whatever it already
      // had, rather than losing values this dialog could not show.
      const params = paramDefs.length > 0 ? changed : scenario.params

      return api.scenarios.update(projectId, scenario.id, {
        modelId: scenario.modelId,
        name: name.trim() || scenario.name,
        description: description.trim(),
        params,
        seed: seed.trim() ? Number(seed) : null,
        replications,
        sitePlanId: sitePlanId || undefined,
        sortOrder: scenario.sortOrder,
      })
    },
    onSuccess: onSaved,
    onError: (err) => {
      if (err instanceof ApiError) setFieldErrors(err.fields ?? {})
    },
  })

  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div className="card-head">
        <h2 style={{ flex: 1 }}>Edit scenario</h2>
        <button className="btn ghost small" onClick={onClose}>
          Cancel
        </button>
      </div>

      {save.error && (
        <div className="banner error">
          {save.error instanceof Error ? save.error.message : 'Could not save the scenario.'}
        </div>
      )}
      {fieldErrors.params && <div className="banner error">{fieldErrors.params}</div>}

      <div className="row wrap" style={{ alignItems: 'flex-start', gap: 16, marginBottom: 16 }}>
        <div style={{ flex: '1 1 220px' }}>
          <div className="field">
            <label htmlFor="edit-name">Scenario name</label>
            <input id="edit-name" value={name} onChange={(e) => setName(e.target.value)} />
            {fieldErrors.name && <div className="error">{fieldErrors.name}</div>}
          </div>
        </div>

        <div style={{ flex: '0 0 120px' }}>
          <div className="field">
            <label htmlFor="edit-seed">Seed</label>
            <input
              id="edit-seed"
              value={seed}
              onChange={(e) => setSeed(e.target.value.replace(/[^0-9]/g, ''))}
              placeholder="random"
            />
            <div className="hint">Fix it to reproduce a run.</div>
          </div>
        </div>

        <div style={{ flex: '0 0 110px' }}>
          <div className="field">
            <label htmlFor="edit-reps">Replications</label>
            <input
              id="edit-reps"
              type="number"
              min={1}
              max={50}
              value={replications}
              onChange={(e) => setReplications(Math.max(1, Math.min(50, Number(e.target.value) || 1)))}
            />
          </div>
        </div>

        <div style={{ flex: '1 1 200px' }}>
          <div className="field">
            <label htmlFor="edit-plan">Site plan</label>
            <select id="edit-plan" value={sitePlanId} onChange={(e) => setSitePlanId(e.target.value)}>
              <option value="">None</option>
              {(plans.data ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
            <div className="hint">The drawing this scenario is viewed over.</div>
          </div>
        </div>
      </div>

      <div className="field" style={{ marginBottom: 16 }}>
        <label htmlFor="edit-desc">Description</label>
        <input
          id="edit-desc"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="Optional. What this scenario is."
        />
      </div>

      {paramDefs.length > 0 ? (
        <>
          <div className="row" style={{ marginBottom: 10 }}>
            <h3 style={{ flex: 1 }}>Parameters</h3>
            <span className="faint" style={{ fontSize: 12 }}>
              {changedCount === 0 ? 'All at defaults' : `${changedCount} changed`}
            </span>
          </div>

          {groups.map(([group, params]) => (
            <div key={group} style={{ marginBottom: 14 }}>
              <div
                className="faint"
                style={{ fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.06em', marginBottom: 8 }}
              >
                {group}
              </div>
              <div className="grid" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(230px, 1fr))', gap: 12 }}>
                {params.map((param) => (
                  <ParamControl
                    key={param.id}
                    param={param}
                    value={values[param.id] ?? defaults[param.id] ?? param.default}
                    onChange={(v) => setValues((prev) => ({ ...prev, [param.id]: v }))}
                  />
                ))}
              </div>
            </div>
          ))}
        </>
      ) : (
        <p className="chart-note" style={{ marginTop: 0 }}>
          {model.data?.source === 'animation'
            ? 'This scenario plays back an imported animation, so it has no parameters to tune.'
            : model.data?.source === 'go_source'
              ? 'This is an uploaded Go model. Its parameters are read by the program at run time; edit its numbers here once it declares them.'
              : 'This model declares no tunable parameters.'}
        </p>
      )}

      <div className="row" style={{ marginTop: 8 }}>
        <button className="btn primary" onClick={() => save.mutate()} disabled={save.isPending}>
          {save.isPending && <span className="spinner" />}
          Save changes
        </button>
        <button className="btn ghost" onClick={onClose}>
          Cancel
        </button>
      </div>
    </div>
  )
}

interface SpecParamShape {
  id: string
  label?: string
  value: number
  min?: number
  max?: number
  unit?: string
  description?: string
}

// specParams turns a spec model's declared parameters into the same shape the
// template controls use, inventing a sensible range where the spec left one
// out so the slider still has bounds to work within.
function specParams(spec: unknown): TemplateParam[] {
  const raw = (spec as { params?: SpecParamShape[] } | null)?.params
  if (!Array.isArray(raw)) return []

  return raw.map((p) => {
    const value = Number(p.value) || 0
    const min = p.min ?? Math.min(0, value)
    const max = p.max ?? (value > 0 ? value * 2 : value + 10)
    return {
      id: p.id,
      label: p.label || p.id,
      description: p.description,
      default: value,
      min,
      max: max > min ? max : min + 1,
      unit: p.unit,
      group: 'Parameters',
    }
  })
}
