import type {
  ApiErrorBody,
  Asset,
  AssetKind,
  AuthConfig,
  Comparison,
  Manifest,
  Project,
  ProjectMember,
  Report,
  ReportInput,
  Role,
  Run,
  RunKPI,
  Scenario,
  Session,
  SimModel,
  SitePlan,
  Template,
} from './types'

/**
 * ApiError carries the server's message and its per-field problems, so a form
 * can put a message next to the input that caused it rather than dumping
 * everything at the top.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code?: string
  readonly fields?: Record<string, string>

  constructor(status: number, body: ApiErrorBody) {
    super(body.error || `Request failed with status ${status}`)
    this.name = 'ApiError'
    this.status = status
    this.code = body.code
    this.fields = body.fields
  }

  /** True when the caller should be sent back to the sign-in page. */
  get isUnauthorized() {
    return this.status === 401
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    // The session is a cookie, so every request has to carry it. Without this
    // the browser omits it on same-origin fetches that set custom headers.
    credentials: 'same-origin',
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init.body && !(init.body instanceof Blob) ? { 'Content-Type': 'application/json' } : {}),
      ...init.headers,
    },
  })

  if (response.status === 204) {
    return undefined as T
  }

  const text = await response.text()
  const parsed = text ? safeParse(text) : undefined

  if (!response.ok) {
    throw new ApiError(response.status, (parsed as ApiErrorBody) ?? { error: text || 'Request failed' })
  }

  return parsed as T
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return { error: text }
  }
}

const get = <T>(path: string) => request<T>(path)
const post = <T>(path: string, body?: unknown) =>
  request<T>(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) })
const patch = <T>(path: string, body: unknown) =>
  request<T>(path, { method: 'PATCH', body: JSON.stringify(body) })
const put = <T>(path: string, body: unknown) =>
  request<T>(path, { method: 'PUT', body: JSON.stringify(body) })
const del = <T>(path: string) => request<T>(path, { method: 'DELETE' })

export const api = {
  auth: {
    config: () => get<AuthConfig>('/api/auth/config'),
    me: () => get<Session>('/api/auth/me'),
    login: (email: string, password: string) => post<Session>('/api/auth/login', { email, password }),
    register: (email: string, password: string, displayName: string) =>
      post<Session>('/api/auth/register', { email, password, displayName }),
    logout: () => post<void>('/api/auth/logout'),
    updateProfile: (displayName: string) => patch<never>('/api/auth/me', { displayName }),
    changePassword: (currentPassword: string, newPassword: string) =>
      post<void>('/api/auth/password', { currentPassword, newPassword }),
  },

  projects: {
    list: (includeArchived = false) =>
      get<Project[]>(`/api/projects${includeArchived ? '?archived=1' : ''}`),
    get: (id: string) => get<Project>(`/api/projects/${id}`),
    create: (name: string, description: string) => post<Project>('/api/projects', { name, description }),
    update: (id: string, name: string, description: string) =>
      patch<Project>(`/api/projects/${id}`, { name, description }),
    remove: (id: string) => del<void>(`/api/projects/${id}`),

    members: (id: string) => get<ProjectMember[]>(`/api/projects/${id}/members`),
    setMember: (id: string, email: string, role: Role) =>
      put<ProjectMember[]>(`/api/projects/${id}/members`, { email, role, userId: '' }),
    removeMember: (id: string, userId: string) => del<void>(`/api/projects/${id}/members/${userId}`),
  },

  assets: {
    list: (projectId: string, kind?: AssetKind) =>
      get<Asset[]>(`/api/projects/${projectId}/assets${kind ? `?kind=${kind}` : ''}`),

    /**
     * Uploads a file as a raw body rather than multipart form data. The server
     * streams it straight to disk that way, which matters when the file is a
     * hundred-megabyte model.
     */
    upload: (projectId: string, kind: AssetKind, file: File, onProgress?: (fraction: number) => void) =>
      uploadRaw(
        `/api/projects/${projectId}/assets?kind=${kind}&name=${encodeURIComponent(file.name)}`,
        file,
        onProgress,
      ),

    remove: (projectId: string, assetId: string) =>
      del<void>(`/api/projects/${projectId}/assets/${assetId}`),

    contentUrl: (projectId: string, assetId: string) =>
      `/api/projects/${projectId}/assets/${assetId}/content`,
  },

  plans: {
    list: (projectId: string) => get<SitePlan[]>(`/api/projects/${projectId}/plans`),
    get: (projectId: string, planId: string) => get<SitePlan>(`/api/projects/${projectId}/plans/${planId}`),
    create: (projectId: string, assetId: string, name: string) =>
      post<SitePlan>(`/api/projects/${projectId}/plans`, { assetId, name }),
    update: (projectId: string, planId: string, plan: Partial<SitePlan>) =>
      put<SitePlan>(`/api/projects/${projectId}/plans/${planId}`, {
        name: plan.name ?? '',
        metersPerPixel: plan.metersPerPixel ?? 1,
        originPxX: plan.originPxX ?? 0,
        originPxY: plan.originPxY ?? 0,
        rotationDeg: plan.rotationDeg ?? 0,
        flipY: plan.flipY ?? true,
        calibration: plan.calibration ?? null,
      }),
    remove: (projectId: string, planId: string) => del<void>(`/api/projects/${projectId}/plans/${planId}`),
  },

  templates: {
    list: () => get<Template[]>('/api/templates'),
    get: (key: string) => get<Template>(`/api/templates/${key}`),
  },

  models: {
    list: (projectId: string) => get<SimModel[]>(`/api/projects/${projectId}/models`),
    get: (projectId: string, modelId: string) => get<SimModel>(`/api/projects/${projectId}/models/${modelId}`),
    createFromTemplate: (projectId: string, templateKey: string, name: string, description = '') =>
      post<SimModel>(`/api/projects/${projectId}/models`, {
        name,
        description,
        source: 'template',
        templateKey,
        spec: null,
        assetId: '',
      }),
    remove: (projectId: string, modelId: string) => del<void>(`/api/projects/${projectId}/models/${modelId}`),
  },

  scenarios: {
    list: (projectId: string) => get<Scenario[]>(`/api/projects/${projectId}/scenarios`),
    get: (projectId: string, scenarioId: string) =>
      get<Scenario>(`/api/projects/${projectId}/scenarios/${scenarioId}`),

    create: (projectId: string, input: ScenarioInput) =>
      post<Scenario>(`/api/projects/${projectId}/scenarios`, scenarioBody(input)),

    update: (projectId: string, scenarioId: string, input: ScenarioInput) =>
      patch<Scenario>(`/api/projects/${projectId}/scenarios/${scenarioId}`, scenarioBody(input)),

    duplicate: (projectId: string, scenarioId: string, name: string) =>
      post<Scenario>(`/api/projects/${projectId}/scenarios/${scenarioId}/duplicate`, { name }),

    remove: (projectId: string, scenarioId: string) =>
      del<void>(`/api/projects/${projectId}/scenarios/${scenarioId}`),

    run: (projectId: string, scenarioId: string) =>
      post<Run[]>(`/api/projects/${projectId}/scenarios/${scenarioId}/run`, {}),
  },

  runs: {
    list: (projectId: string, scenarioId?: string) =>
      get<Run[]>(`/api/projects/${projectId}/runs${scenarioId ? `?scenarioId=${scenarioId}` : ''}`),
    get: (projectId: string, runId: string) => get<Run>(`/api/projects/${projectId}/runs/${runId}`),
    kpis: (projectId: string, runId: string) => get<RunKPI[]>(`/api/projects/${projectId}/runs/${runId}/kpis`),
    cancel: (projectId: string, runId: string) => post<void>(`/api/projects/${projectId}/runs/${runId}/cancel`),
    remove: (projectId: string, runId: string) => del<void>(`/api/projects/${projectId}/runs/${runId}`),

    artifactUrl: (projectId: string, runId: string, path: string) =>
      `/api/projects/${projectId}/runs/${runId}/artifacts/${path}`,

    manifest: (projectId: string, runId: string) =>
      get<Manifest>(`/api/projects/${projectId}/runs/${runId}/artifacts/manifest.json`),

    aggregate: <T>(projectId: string, runId: string, name: string) =>
      get<T>(`/api/projects/${projectId}/runs/${runId}/artifacts/agg/${name}`),
  },

  imports: {
    animation: (projectId: string, assetId: string, name: string) =>
      post<{ model: SimModel; scenario: Scenario; run: Run }>(
        `/api/projects/${projectId}/imports/animation`,
        { assetId, name },
      ),
  },

  events: {
    url: (projectId: string) => `/api/projects/${projectId}/events`,
  },

  reports: {
    list: (projectId: string) => get<Report[]>(`/api/projects/${projectId}/reports`),
    get: (projectId: string, reportId: string) =>
      get<Report>(`/api/projects/${projectId}/reports/${reportId}`),
    create: (projectId: string, input: ReportInput) =>
      post<Report>(`/api/projects/${projectId}/reports`, input),
    update: (projectId: string, reportId: string, input: ReportInput) =>
      patch<Report>(`/api/projects/${projectId}/reports/${reportId}`, input),
    remove: (projectId: string, reportId: string) =>
      del<void>(`/api/projects/${projectId}/reports/${reportId}`),

    /** The download URL for a saved report's PDF. A plain link, so the browser
     *  handles the download and its progress rather than buffering it in JS. */
    pdfUrl: (projectId: string, reportId: string) =>
      `/api/projects/${projectId}/reports/${reportId}/pdf`,

    /** Exports a report that was assembled but not saved, returning the PDF as
     *  a blob so a one-off export leaves no definition behind. */
    exportAdhoc: async (projectId: string, input: ReportInput): Promise<Blob> => {
      const response = await fetch(`/api/projects/${projectId}/reports/export`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', Accept: 'application/pdf' },
        body: JSON.stringify(input),
      })
      if (!response.ok) {
        const text = await response.text()
        throw new ApiError(response.status, safeParse(text) as ApiErrorBody)
      }
      return response.blob()
    },
  },

  /**
   * Compares scenarios, not runs. A scenario's replications are the same
   * experiment repeated, and the spread between them is what decides whether a
   * difference between scenarios means anything.
   */
  compare: (projectId: string, scenarioIds: string[]) =>
    get<Comparison>(
      `/api/projects/${projectId}/compare?scenarios=${encodeURIComponent(scenarioIds.join(','))}`,
    ),
}

export interface ScenarioInput {
  modelId: string
  name: string
  description?: string
  params: Record<string, number>
  seed?: number | null
  replications?: number
  sitePlanId?: string
  sortOrder?: number
}

function scenarioBody(input: ScenarioInput) {
  return {
    modelId: input.modelId,
    name: input.name,
    description: input.description ?? '',
    params: input.params,
    seed: input.seed ?? null,
    replications: input.replications ?? 1,
    sitePlanId: input.sitePlanId ?? '',
    sortOrder: input.sortOrder ?? 0,
  }
}

/**
 * uploadRaw uses XMLHttpRequest rather than fetch, because fetch still cannot
 * report upload progress. For a file large enough to need a progress bar, that
 * is the whole reason to care.
 */
function uploadRaw(url: string, file: File, onProgress?: (fraction: number) => void): Promise<Asset> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', url)
    xhr.withCredentials = true
    xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream')

    if (onProgress) {
      xhr.upload.addEventListener('progress', (e) => {
        if (e.lengthComputable) onProgress(e.loaded / e.total)
      })
    }

    xhr.addEventListener('load', () => {
      const body = safeParse(xhr.responseText || '{}')
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(body as Asset)
      } else {
        reject(new ApiError(xhr.status, body as ApiErrorBody))
      }
    })

    xhr.addEventListener('error', () => reject(new Error('The upload failed. Check your connection.')))
    xhr.addEventListener('abort', () => reject(new Error('The upload was cancelled.')))

    xhr.send(file)
  })
}
