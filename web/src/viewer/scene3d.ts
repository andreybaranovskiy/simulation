import * as THREE from 'three'
import { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js'
import { GLTFLoader } from 'three/examples/jsm/loaders/GLTFLoader.js'
import { DRACOLoader } from 'three/examples/jsm/loaders/DRACOLoader.js'

import { formGeometry, resolveForm } from './forms'

import type { ClassInfo, Manifest, SitePlan } from '@/api/types'
import { EntityState, stateColors } from '@/api/types'
import type { EntityAt } from './playback'

/**
 * The viewer's own palette, one per theme. The entity, node and zone colours
 * are content and carry across both; only the ground the scene sits on changes.
 */
const VIEWER_THEME = {
  dark: { bg: 0x0b1020, gridMajor: 0x2a3566, gridMinor: 0x1b2344, hemiSky: 0xb8c6ff, hemiGround: 0x18203a },
  light: { bg: 0xeef1f8, gridMajor: 0xc3cde6, gridMinor: 0xdae1f2, hemiSky: 0xffffff, hemiGround: 0xccd6f0 },
} as const

type ThemeName = keyof typeof VIEWER_THEME

/**
 * The 3D scene.
 *
 * Carried over from the original viewer: the orbit controls, the dark palette,
 * the environment fitting, and the follow camera. Two things are new and they
 * are what let it handle a real run.
 *
 * Entities are drawn with one instanced mesh per class instead of one Object3D
 * each. A thousand trucks was a thousand draw calls before; now it is one per
 * class, and the cost of a crowded run is a matrix write rather than a scene
 * graph traversal.
 *
 * Positions come from the playback engine rather than from keyframes held in
 * memory, so the scene never holds more of a run than the moment on screen.
 */

/** World coordinates are metres with Y north; three.js wants Y up. The scene
 *  therefore maps world (x, y) onto (x, z) and uses world z as height. */
function toScene(x: number, y: number, z: number): [number, number, number] {
  return [x, z, -y]
}

export interface SceneOptions {
  /** Colour entities by what they are doing rather than by class. This is the
   *  single most useful toggle in the viewer: it turns a busy scene into a
   *  picture of where the waiting is. */
  colorByState: boolean
  showNodes: boolean
  showZones: boolean
  showGrid: boolean
}

const DEFAULT_OPTIONS: SceneOptions = {
  colorByState: false,
  showNodes: true,
  showZones: true,
  showGrid: true,
}

export class Scene3D {
  readonly renderer: THREE.WebGLRenderer
  readonly scene: THREE.Scene
  readonly camera: THREE.PerspectiveCamera
  readonly controls: OrbitControls

  private container: HTMLElement
  private classes: ClassInfo[] = []
  private instanced: THREE.InstancedMesh[] = []
  private counts: number[] = []

  private environment: THREE.Object3D | null = null
  private planMesh: THREE.Mesh | null = null
  private annotations = new THREE.Group()
  private grid: THREE.GridHelper | null = null
  private themeName: ThemeName = 'dark'
  private hemi: THREE.HemisphereLight | null = null
  // Kept so a theme change can rebuild the grid without a full scene rebuild.
  private gridCenter: [number, number, number] = [0, 0, 0]
  private gridSize = 0
  private gridDivisions = 0

  private options: SceneOptions = { ...DEFAULT_OPTIONS }
  private hiddenClasses = new Set<number>()

  /** Reused so a frame with thousands of entities allocates nothing. */
  private readonly matrix = new THREE.Matrix4()
  private readonly quaternion = new THREE.Quaternion()
  private readonly position = new THREE.Vector3()
  private readonly scale = new THREE.Vector3(1, 1, 1)
  private readonly color = new THREE.Color()
  private readonly up = new THREE.Vector3(0, 1, 0)

  /** Where each entity was drawn last frame, so the follow camera can find one
   *  without re-resolving the whole moment. */
  private lastPositions = new Map<number, THREE.Vector3>()

  /** Which entity occupies each instance slot, per class.
   *
   *  Raycasting an instanced mesh reports an instance index, not an entity, and
   *  slots are reused every frame as entities come and go. Without this table a
   *  click would select whichever entity happened to be drawn in that slot. */
  private slotEntities: number[][] = []

  /** The heading each entity is currently drawn at, eased toward its true
   *  heading so a turn reads as the object rotating rather than the box
   *  flipping between two axis-aligned directions in one frame. */
  private renderHeading = new Map<number, number>()

  private followId: number | null = null
  private disposed = false

  constructor(container: HTMLElement) {
    this.container = container

    this.renderer = new THREE.WebGLRenderer({ antialias: true, powerPreference: 'high-performance' })
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2))
    this.renderer.setClearColor(0x0b1020)
    container.appendChild(this.renderer.domElement)

    this.scene = new THREE.Scene()
    this.scene.background = new THREE.Color(0x0b1020)
    // Fog hides the far edge of a large site instead of ending it abruptly.
    this.scene.fog = new THREE.Fog(0x0b1020, 400, 4000)

    this.camera = new THREE.PerspectiveCamera(55, 1, 0.5, 20000)
    this.camera.position.set(120, 90, 120)

    this.controls = new OrbitControls(this.camera, this.renderer.domElement)
    this.controls.enableDamping = true
    this.controls.dampingFactor = 0.08
    this.controls.maxPolarAngle = Math.PI * 0.495
    this.controls.screenSpacePanning = false

    this.hemi = new THREE.HemisphereLight(0xb8c6ff, 0x18203a, 1.1)
    this.scene.add(this.hemi)

    const sun = new THREE.DirectionalLight(0xffffff, 1.4)
    sun.position.set(200, 400, 150)
    this.scene.add(sun)

    this.scene.add(this.annotations)
    this.resize()
  }

  setOptions(options: Partial<SceneOptions>) {
    this.options = { ...this.options, ...options }
    if (this.grid) this.grid.visible = this.options.showGrid
    this.annotations.visible = this.options.showNodes || this.options.showZones
  }

  setHiddenClasses(hidden: Set<number>) {
    this.hiddenClasses = hidden
  }

  setFollow(id: number | null) {
    this.followId = id
  }

  /**
   * Builds the static parts of the scene: the ground, the node markers, the
   * zones and one instanced mesh per entity class.
   */
  build(manifest: Manifest) {
    this.classes = manifest.classes
    this.disposeInstances()
    this.annotations.clear()

    const bounds = manifest.bounds
    const width = Math.max(bounds.maxX - bounds.minX, 1)
    const depth = Math.max(bounds.maxY - bounds.minY, 1)
    const extent = Math.max(width, depth)

    this.buildGround(bounds, extent)
    this.buildZones(manifest)
    this.buildNodes(manifest, extent)
    this.buildInstances(manifest)

    this.frameSite(bounds)
  }

  private buildGround(bounds: Manifest['bounds'], extent: number) {
    if (this.grid) {
      this.scene.remove(this.grid)
      this.grid.dispose()
    }

    // Grid lines every ten metres up to a point, then coarser, so the grid
    // stays a scale reference rather than becoming a solid sheet.
    const spacing = extent > 2000 ? 100 : extent > 500 ? 50 : 10
    const divisions = Math.max(4, Math.round((extent * 1.4) / spacing))

    this.gridCenter = [(bounds.minX + bounds.maxX) / 2, -0.02, -(bounds.minY + bounds.maxY) / 2]
    this.gridSize = extent * 1.4
    this.gridDivisions = divisions
    this.rebuildGrid()
  }

  private rebuildGrid() {
    if (this.grid) {
      this.scene.remove(this.grid)
      this.grid.dispose()
      this.grid = null
    }
    if (this.gridSize <= 0) return

    const theme = VIEWER_THEME[this.themeName]
    this.grid = new THREE.GridHelper(this.gridSize, this.gridDivisions, theme.gridMajor, theme.gridMinor)
    this.grid.position.set(this.gridCenter[0], this.gridCenter[1], this.gridCenter[2])
    this.grid.visible = this.options.showGrid
    this.scene.add(this.grid)
  }

  /**
   * Recolours the ground for the chosen theme. The entities, nodes and zones
   * keep their own colours: those are content, legible on either surface. Only
   * the background, the fog and the grid, which are the ground the scene sits
   * on, follow the theme.
   */
  setTheme(name: ThemeName) {
    if (name === this.themeName && this.grid) return
    this.themeName = name

    const theme = VIEWER_THEME[name]
    this.renderer.setClearColor(theme.bg)
    if (this.scene.background instanceof THREE.Color) this.scene.background.setHex(theme.bg)
    else this.scene.background = new THREE.Color(theme.bg)
    if (this.scene.fog instanceof THREE.Fog) this.scene.fog.color.setHex(theme.bg)
    if (this.hemi) {
      this.hemi.color.setHex(theme.hemiSky)
      this.hemi.groundColor.setHex(theme.hemiGround)
    }
    this.rebuildGrid()
  }

  private buildZones(manifest: Manifest) {
    for (const zone of manifest.zones ?? []) {
      const geometry = new THREE.PlaneGeometry(zone.width, zone.height)
      const material = new THREE.MeshBasicMaterial({
        color: new THREE.Color(zone.color || '#3fc98a'),
        transparent: true,
        opacity: 0.08,
        side: THREE.DoubleSide,
        depthWrite: false,
      })

      const mesh = new THREE.Mesh(geometry, material)
      mesh.rotation.x = -Math.PI / 2
      const [x, y, z] = toScene(zone.x + zone.width / 2, zone.y + zone.height / 2, 0)
      mesh.position.set(x, y + 0.03, z)

      this.annotations.add(mesh)
    }
  }

  private buildNodes(manifest: Manifest, extent: number) {
    // Markers scale with the site, so a node is visible on a 900 metre
    // terminal and not overwhelming in a 40 metre warehouse.
    const radius = Math.max(0.6, extent / 400)

    const geometry = new THREE.CylinderGeometry(radius, radius, 0.3, 12)
    const nodeMaterial = new THREE.MeshBasicMaterial({ color: 0x4c6bbf, transparent: true, opacity: 0.7 })
    const resourceMaterial = new THREE.MeshBasicMaterial({ color: 0xf2c14e, transparent: true, opacity: 0.85 })

    const resourceNodes = new Set((manifest.resources ?? []).map((r) => `${r.x},${r.y}`))

    for (const node of manifest.nodes ?? []) {
      const isResource = resourceNodes.has(`${node.x},${node.y}`)
      const mesh = new THREE.Mesh(geometry, isResource ? resourceMaterial : nodeMaterial)

      const [x, y, z] = toScene(node.x, node.y, node.z)
      mesh.position.set(x, y + 0.15, z)
      this.annotations.add(mesh)
    }
  }

  /**
   * One instanced mesh per class. The capacity is the class's own entity count
   * rather than the run total, since an instance only exists while its entity
   * is on screen.
   */
  private buildInstances(manifest: Manifest) {
    for (const [index, info] of (manifest.classes ?? []).entries()) {
      const capacity = Math.max(16, Math.min(info.count || 64, 100_000))

      const geometry = geometryFor(info)
      const material = new THREE.MeshStandardMaterial({
        color: new THREE.Color(info.color || '#4c8dff'),
        roughness: 0.55,
        metalness: 0.05,
      })

      const mesh = new THREE.InstancedMesh(geometry, material, capacity)
      mesh.instanceMatrix.setUsage(THREE.DynamicDrawUsage)
      mesh.frustumCulled = false
      mesh.count = 0

      this.slotEntities[index] = new Array(capacity).fill(-1)

      // A per-instance colour buffer is what lets the state overlay recolour
      // individual entities without a material per entity.
      mesh.instanceColor = new THREE.InstancedBufferAttribute(new Float32Array(capacity * 3), 3)
      mesh.instanceColor.setUsage(THREE.DynamicDrawUsage)

      this.scene.add(mesh)
      this.instanced[index] = mesh
      this.counts[index] = 0
    }
  }

  /** Positions every entity for the current moment. */
  update(entities: EntityAt[], selectedId: number | null, hoveredId: number | null) {
    if (this.disposed) return

    for (let i = 0; i < this.instanced.length; i++) this.counts[i] = 0
    this.lastPositions.clear()

    // A four metre truck viewed across a ten kilometre site is a fraction of a
    // pixel, so a true-to-scale scene shows an empty field. Entities are given
    // a floor on their apparent size, derived from how far the camera is, so
    // they stay visible when zoomed out and return to true scale as soon as
    // the camera is close enough for true scale to mean anything.
    const cameraDistance = this.camera.position.distanceTo(this.controls.target)
    const minWorldSize = cameraDistance * 0.005

    for (const entity of entities) {
      if (this.hiddenClasses.has(entity.cls)) continue

      const mesh = this.instanced[entity.cls]
      if (!mesh) continue

      const slot = this.counts[entity.cls]
      if (slot >= mesh.instanceMatrix.count) continue

      const info = this.classes[entity.cls]
      const [x, y, z] = toScene(entity.x, entity.y, entity.z)

      const height = info?.height || 2
      const longest = Math.max(info?.length || 2, info?.width || 2, height)

      let boost = minWorldSize > longest ? minWorldSize / longest : 1

      const emphasised = entity.id === selectedId || entity.id === hoveredId
      if (emphasised) {
        // Scaling rather than outlining: an outline pass would cost a second
        // render of the whole scene to highlight one box.
        boost *= 1.6
      }

      this.position.set(x, y + (height * boost) / 2, z)
      this.quaternion.setFromAxisAngle(this.up, this.smoothHeading(entity))
      this.scale.set(boost, boost, boost)

      this.matrix.compose(this.position, this.quaternion, this.scale)
      mesh.setMatrixAt(slot, this.matrix)

      this.applyColor(mesh, slot, entity, info, emphasised)

      this.lastPositions.set(entity.id, new THREE.Vector3(x, y + height / 2, z))

      const slots = this.slotEntities[entity.cls]
      if (slots) slots[slot] = entity.id

      this.counts[entity.cls] = slot + 1
    }

    for (let i = 0; i < this.instanced.length; i++) {
      const mesh = this.instanced[i]
      if (!mesh) continue

      mesh.count = this.counts[i]
      mesh.instanceMatrix.needsUpdate = true
      if (mesh.instanceColor) mesh.instanceColor.needsUpdate = true
    }

    this.updateFollowCamera()
  }

  /**
   * Eases an entity's drawn heading toward its true one, and holds it steady
   * while the entity is stopped.
   *
   * A stopped entity reports no heading, so without the hold it would snap to
   * facing forward the moment it halts. While moving, the drawn heading turns a
   * fraction of the way to the target each frame, along the shorter arc, so a
   * corner is taken rather than jumped.
   */
  private smoothHeading(entity: EntityAt): number {
    const previous = this.renderHeading.get(entity.id)

    if (!entity.moving && previous !== undefined) {
      return previous
    }
    if (previous === undefined) {
      this.renderHeading.set(entity.id, entity.heading)
      return entity.heading
    }

    // The shortest signed angle from previous to target, so a turn across the
    // ±π seam goes the short way instead of spinning almost all the way round.
    let delta = entity.heading - previous
    while (delta > Math.PI) delta -= 2 * Math.PI
    while (delta < -Math.PI) delta += 2 * Math.PI

    const next = previous + delta * 0.2
    this.renderHeading.set(entity.id, next)
    return next
  }

  private applyColor(
    mesh: THREE.InstancedMesh,
    slot: number,
    entity: EntityAt,
    info: ClassInfo | undefined,
    emphasised: boolean,
  ) {
    if (!mesh.instanceColor) return

    if (emphasised) {
      this.color.set('#ffffff')
    } else if (this.options.colorByState) {
      this.color.set(stateColors[entity.state as EntityState] ?? '#6b7590')
    } else {
      this.color.set(info?.color || '#4c8dff')
    }

    mesh.setColorAt(slot, this.color)
  }

  /** Keeps the camera behind a followed entity without wrenching it around. */
  private updateFollowCamera() {
    if (this.followId === null) return

    const target = this.lastPositions.get(this.followId)
    if (!target) return

    // Damped rather than snapped: a camera that teleports every frame makes
    // the scene unreadable, and an entity's position is interpolated anyway.
    this.controls.target.lerp(target, 0.15)
  }

  /** Frames the whole site, which is where a viewer should open. */
  frameSite(bounds: Manifest['bounds']) {
    const cx = (bounds.minX + bounds.maxX) / 2
    const cy = (bounds.minY + bounds.maxY) / 2
    const extent = Math.max(bounds.maxX - bounds.minX, bounds.maxY - bounds.minY, 10)

    const [x, , z] = toScene(cx, cy, 0)
    this.controls.target.set(x, 0, z)

    const distance = extent * 0.9
    this.camera.position.set(x + distance * 0.55, distance * 0.6, z + distance * 0.75)
    this.camera.far = Math.max(20000, extent * 8)
    this.camera.updateProjectionMatrix()

    if (this.scene.fog instanceof THREE.Fog) {
      this.scene.fog.near = extent * 0.8
      this.scene.fog.far = extent * 4
    }

    this.controls.update()
  }

  /** Moves the camera to look at one entity, once, without following it. */
  focusOn(id: number) {
    const target = this.lastPositions.get(id)
    if (!target) return

    this.controls.target.copy(target)

    const offset = new THREE.Vector3(20, 16, 20)
    this.camera.position.copy(target).add(offset)
    this.controls.update()
  }

  /** Loads a GLB environment, as the original viewer did. */
  async loadEnvironment(url: string, targetSize: number): Promise<void> {
    const loader = new GLTFLoader()
    const draco = new DRACOLoader()
    // Decoders are loaded from the same origin, since the page's policy does
    // not allow fetching them from a CDN.
    draco.setDecoderPath('/draco/')
    loader.setDRACOLoader(draco)

    const gltf = await loader.loadAsync(url)
    const root = gltf.scene ?? gltf.scenes[0]
    if (!root) throw new Error('That file contains no 3D scene.')

    fitUniform(root, targetSize)
    this.setEnvironment(root)
  }

  /** Lays a calibrated plan image flat under the scene, at its real size. */
  async loadPlanImage(url: string, plan: SitePlan): Promise<void> {
    const texture = await new THREE.TextureLoader().loadAsync(url)
    texture.colorSpace = THREE.SRGBColorSpace
    texture.anisotropy = Math.min(16, this.renderer.capabilities.getMaxAnisotropy())

    // The calibration is what makes this meaningful: the plan is placed at the
    // size it actually represents, so a truck drawn beside it is to scale.
    const widthMeters = plan.imageWidth * plan.metersPerPixel
    const heightMeters = plan.imageHeight * plan.metersPerPixel

    const geometry = new THREE.PlaneGeometry(widthMeters, heightMeters)
    const material = new THREE.MeshBasicMaterial({
      map: texture,
      transparent: true,
      opacity: 0.85,
      depthWrite: false,
    })

    const mesh = new THREE.Mesh(geometry, material)
    mesh.rotation.x = -Math.PI / 2
    mesh.rotation.z = (plan.rotationDeg * Math.PI) / 180

    // The plan's origin pixel sits at the world origin, so the image lands
    // where the model's coordinates say it should.
    const offsetX = (plan.imageWidth / 2 - plan.originPxX) * plan.metersPerPixel
    const offsetY = (plan.imageHeight / 2 - plan.originPxY) * plan.metersPerPixel

    const [x, y, z] = toScene(offsetX, plan.flipY ? -offsetY : offsetY, 0)
    mesh.position.set(x, y + 0.01, z)

    if (this.planMesh) {
      this.scene.remove(this.planMesh)
      disposeObject(this.planMesh)
    }
    this.planMesh = mesh
    this.scene.add(mesh)
  }

  setEnvironmentOpacity(opacity: number) {
    for (const target of [this.environment, this.planMesh]) {
      target?.traverse?.((node) => {
        if (node instanceof THREE.Mesh) {
          for (const material of materialsOf(node)) {
            material.transparent = opacity < 1
            material.opacity = opacity
            material.depthWrite = opacity >= 1
          }
        }
      })
    }
  }

  private setEnvironment(root: THREE.Object3D) {
    if (this.environment) {
      this.scene.remove(this.environment)
      disposeObject(this.environment)
    }
    this.environment = root
    this.scene.add(root)
  }

  resize() {
    const width = this.container.clientWidth || 1
    const height = this.container.clientHeight || 1

    this.renderer.setSize(width, height, false)
    this.camera.aspect = width / height
    this.camera.updateProjectionMatrix()
  }

  render() {
    if (this.disposed) return
    this.controls.update()
    this.renderer.render(this.scene, this.camera)
  }

  /** Finds the entity under a pointer position, for click-to-select. */
  pick(clientX: number, clientY: number): number | null {
    const rect = this.renderer.domElement.getBoundingClientRect()
    const ndc = new THREE.Vector2(
      ((clientX - rect.left) / rect.width) * 2 - 1,
      -((clientY - rect.top) / rect.height) * 2 + 1,
    )

    const raycaster = new THREE.Raycaster()
    raycaster.setFromCamera(ndc, this.camera)

    const hits = raycaster.intersectObjects(this.instanced.filter(Boolean), false)
    if (hits.length === 0) return null

    const hit = hits[0]
    if (hit.instanceId === undefined) return null

    const classIndex = this.instanced.indexOf(hit.object as THREE.InstancedMesh)
    if (classIndex === -1) return null

    const entityId = this.slotEntities[classIndex]?.[hit.instanceId]
    return entityId === undefined || entityId < 0 ? null : entityId
  }

  private disposeInstances() {
    for (const mesh of this.instanced) {
      if (!mesh) continue
      this.scene.remove(mesh)
      mesh.geometry.dispose()
      disposeMaterial(mesh.material)
      mesh.dispose()
    }
    this.instanced = []
    this.counts = []
    this.slotEntities = []
    this.renderHeading.clear()
  }

  dispose() {
    this.disposed = true
    this.disposeInstances()

    if (this.environment) disposeObject(this.environment)
    if (this.planMesh) disposeObject(this.planMesh)
    if (this.grid) this.grid.dispose()

    this.annotations.clear()
    this.controls.dispose()
    this.renderer.dispose()

    this.renderer.domElement.remove()
  }
}

function geometryFor(info: ClassInfo): THREE.BufferGeometry {
  const length = Math.max(info.length || 2, 0.2)
  const width = Math.max(info.width || 2, 0.2)
  const height = Math.max(info.height || 2, 0.2)

  // The form is chosen from the class's shape, or inferred from its label, and
  // is always built with its length along +Z so the renderer can turn it to
  // face travel.
  return formGeometry(resolveForm(info), length, width, height)
}

/** Scales an object so its longest axis matches a target size, and sets it on
 *  the ground. Carried over from the original viewer. */
function fitUniform(object: THREE.Object3D, targetLongest: number) {
  const box = new THREE.Box3().setFromObject(object)
  const size = new THREE.Vector3()
  const center = new THREE.Vector3()
  box.getSize(size)
  box.getCenter(center)

  object.position.sub(center)
  object.position.y -= box.min.y - center.y

  const longest = Math.max(size.x, size.y, size.z) || 1
  object.scale.setScalar(targetLongest / longest)
}

function materialsOf(mesh: THREE.Mesh): THREE.Material[] {
  return Array.isArray(mesh.material) ? mesh.material : [mesh.material]
}

function disposeMaterial(material: THREE.Material | THREE.Material[]) {
  for (const m of Array.isArray(material) ? material : [material]) m.dispose()
}

function disposeObject(object: THREE.Object3D) {
  object.traverse((node) => {
    if (node instanceof THREE.Mesh) {
      node.geometry.dispose()
      disposeMaterial(node.material)
    }
  })
}
