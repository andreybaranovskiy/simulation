// Types mirroring the Go API. They are written by hand rather than generated,
// so every field here is one somebody decided the UI needs.

export type Role = 'owner' | 'editor' | 'viewer'

export interface User {
  id: string
  email: string
  displayName: string
  isAdmin: boolean
  canUploadGo: boolean
  isActive: boolean
  createdAt: string
  updatedAt: string
}

export interface Session {
  user: User
  expiresAt: string
}

export interface AuthConfig {
  provider: string
  allowRegistration: boolean
  minPasswordLength: number
}

export interface Project {
  id: string
  name: string
  description: string
  ownerId: string
  archivedAt?: string
  createdAt: string
  updatedAt: string
  role?: Role
}

export interface ProjectMember {
  projectId: string
  userId: string
  email: string
  displayName: string
  role: Role
  createdAt: string
}

export type AssetKind =
  | 'plan_image'
  | 'model_3d'
  | 'model_spec'
  | 'animation_json'
  | 'go_model'
  | 'other'

export interface Asset {
  id: string
  projectId: string
  kind: AssetKind
  originalName: string
  contentType: string
  sizeBytes: number
  sha256: string
  meta?: { width?: number; height?: number; format?: string }
  uploadedBy: string
  createdAt: string
}

/** A plan image plus the transform that turns its pixels into metres. */
export interface SitePlan {
  id: string
  projectId: string
  assetId: string
  name: string
  imageWidth: number
  imageHeight: number
  metersPerPixel: number
  originPxX: number
  originPxY: number
  rotationDeg: number
  flipY: boolean
  calibration?: Calibration
  createdAt: string
  updatedAt: string
}

/**
 * How the scale was measured: the two points picked on the image and the
 * real-world distance typed in. Storing the measurement rather than only its
 * result is what lets the tool redraw and re-edit it.
 */
export interface Calibration {
  points: [[number, number], [number, number]]
  realLength: number
  unit: string
}

export type ModelSource = 'template' | 'spec' | 'go_source' | 'animation'

export interface SimModel {
  id: string
  projectId: string
  name: string
  description: string
  source: ModelSource
  templateKey?: string
  domain: string
  spec?: unknown
  assetId?: string
  version: number
  createdBy: string
  createdAt: string
  updatedAt: string
  scenarioCount?: number
}

export interface TemplateParam {
  id: string
  label: string
  description?: string
  default: number
  min: number
  max: number
  step?: number
  unit?: string
  integer?: boolean
  group?: string
}

export interface Template {
  key: string
  name: string
  description: string
  domain: string
  params: TemplateParam[]
}

export interface Scenario {
  id: string
  projectId: string
  modelId: string
  name: string
  description: string
  params: Record<string, number>
  seed?: number
  replications: number
  sitePlanId?: string
  sortOrder: number
  archivedAt?: string
  createdBy: string
  createdAt: string
  updatedAt: string
  modelName?: string
  runCount?: number
  latestRun?: Run
}

export type RunStatus = 'queued' | 'running' | 'building' | 'done' | 'failed' | 'canceled'

export interface Run {
  id: string
  projectId: string
  scenarioId: string
  replication: number
  seed: number
  status: RunStatus
  queuedAt: string
  startedAt?: string
  finishedAt?: string
  progress: number
  simTime: number
  entityCount: number
  recordCount: number
  durationMs: number
  artifactBytes: number
  engineVersion?: string
  error?: string
  warnings?: string[]
  createdBy: string
  scenarioName?: string
}

export interface RunKPI {
  runId: string
  key: string
  value: number
  label: string
  unit: string
  group: string
  better?: 'lower' | 'higher'
  decimals: number
  headline: boolean
  resourceId?: string
  /** Only the run's own KPI file carries this. The database rows leave it out
   *  because it is a property of the measure, not of the run, and duplicating
   *  it per run would let the two drift apart. */
  description?: string
}

/** A run's full KPI set, as stored in its aggregate file. */
export interface KPISet {
  kpis: RunKPI[]
  /** Caveats that change how the numbers should be read, such as a run that
   *  hit its entity limit and is therefore a lower bound. */
  notes?: string[]
}

/** Live progress from the server-sent event stream. */
export interface RunEvent {
  type: 'queued' | 'started' | 'progress' | 'finished'
  runId: string
  scenarioId?: string
  projectId?: string
  status?: RunStatus
  progress?: number
  simTime?: number
  entities?: number
  records?: number
  error?: string
}

// ---------------------------------------------------------------------------
// Run artifacts
// ---------------------------------------------------------------------------

/** The index a viewer reads first. */
export interface Manifest {
  version: number
  runId: string
  modelName: string
  domain?: string
  seed: number
  replication: number
  engineVersion: string
  builtAt: string

  startTime: number
  endTime: number
  warmUp?: number

  bounds: Bounds
  levels: Level[]

  classes: ClassInfo[]
  nodes: NodeInfo[]
  resources: ResourceInfo[]
  zones?: ZoneInfo[]

  counts: {
    entities: number
    records: number
    spans: number
    chunks: number
    totalBytes: number
  }

  heatmaps?: HeatmapInfo[]
  available: {
    series: boolean
    gantt: boolean
    paths: boolean
    heatmaps: boolean
    kpis: boolean
  }
  warnings?: string[]
}

export interface Bounds {
  minX: number
  minY: number
  minZ: number
  maxX: number
  maxY: number
  maxZ: number
}

/** One playback fidelity. Level 0 has every entity; higher levels thin them. */
export interface Level {
  index: number
  stride: number
  chunkSeconds: number
  chunkCount: number
  bytes: number
  peakLive: number
}

export interface ClassInfo {
  id: string
  label: string
  color: string
  shape: string
  length: number
  width: number
  height: number
  model?: string
  count: number
}

export interface NodeInfo {
  id: string
  label: string
  x: number
  y: number
  z: number
}

export interface ResourceInfo {
  id: string
  label: string
  capacity: number
  x: number
  y: number
}

export interface ZoneInfo {
  id: string
  label: string
  x: number
  y: number
  width: number
  height: number
  color?: string
}

export interface HeatmapInfo {
  metric: string
  label: string
  unit: string
  description: string
  cols: number
  rows: number
  cellMeters: number
  buckets: number
  bucketSeconds: number
  max: number
  total: number
}

/** What an entity is doing, matching the engine's state values. */
export enum EntityState {
  Idle = 0,
  Travelling = 1,
  Queued = 2,
  Serving = 3,
  Blocked = 4,
}

export const stateNames: Record<EntityState, string> = {
  [EntityState.Idle]: 'Idle',
  [EntityState.Travelling]: 'Travelling',
  [EntityState.Queued]: 'Queued',
  [EntityState.Serving]: 'Serving',
  [EntityState.Blocked]: 'Blocked',
}

/**
 * Colours for the state overlay. Queued and blocked are the warm end on
 * purpose: the point of colouring by state is to make waiting visible.
 */
export const stateColors: Record<EntityState, string> = {
  [EntityState.Idle]: '#6b7590',
  [EntityState.Travelling]: '#4c8dff',
  [EntityState.Queued]: '#f2994a',
  [EntityState.Serving]: '#3fc98a',
  [EntityState.Blocked]: '#e0566a',
}

export interface ApiErrorBody {
  error: string
  code?: string
  fields?: Record<string, string>
}
