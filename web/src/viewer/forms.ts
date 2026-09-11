import * as THREE from 'three'
import { mergeGeometries } from 'three/examples/jsm/utils/BufferGeometryUtils.js'

import type { ClassInfo } from '@/api/types'

/**
 * Simple recognisable shapes for the entities on the plan.
 *
 * A plain box tells a viewer nothing about which way it faces, so a truck
 * turning a corner reads as a square sliding sideways. These forms give each
 * kind of thing a front and a silhouette — a cab, a bow, a mast — so its
 * heading is legible and the movement looks like driving rather than sliding.
 *
 * Every form is built the same way the box was: real metres, centred on the
 * origin, with its length running along +Z, which is the direction the renderer
 * turns to face travel. So a form drops in wherever a box did and orients the
 * same.
 */

export type Form =
  | 'box'
  | 'container'
  | 'truck'
  | 'ship'
  | 'agv'
  | 'crane'
  | 'gate'
  | 'person'
  | 'cylinder'
  | 'marker'

const FORMS: ReadonlySet<string> = new Set<Form>([
  'box', 'container', 'truck', 'ship', 'agv', 'crane', 'gate', 'person', 'cylinder', 'marker',
])

/**
 * Decides which form a class draws as.
 *
 * An explicit shape wins. Otherwise the class label is read for the obvious
 * words, so a model that never set a shape — every run made before these forms
 * existed — still shows trucks as trucks and ships as ships. Only when nothing
 * matches does it fall back to a box.
 */
export function resolveForm(info: ClassInfo | undefined): Form {
  if (!info) return 'box'
  if (FORMS.has(info.shape)) return info.shape as Form

  const label = `${info.label} ${info.id}`.toLowerCase()
  if (/\b(ship|vessel|barge|boat|tanker|freighter)\b/.test(label)) return 'ship'
  if (/\b(truck|lorry|hgv|tractor|trailer|van|chassis)\b/.test(label)) return 'truck'
  if (/\b(agv|forklift|reachstacker|reach|straddle|shuttle|loader)\b/.test(label)) return 'agv'
  if (/\b(crane|gantry|rtg|rmg|sts|quay)\b/.test(label)) return 'crane'
  if (/\b(gate|barrier|checkpoint|booth)\b/.test(label)) return 'gate'
  if (/\b(person|people|worker|pedestrian|passenger|staff)\b/.test(label)) return 'person'
  if (/\b(container|teu|crate|cargo|box|pallet|unit)\b/.test(label)) return 'container'

  // The old vocabulary still resolves for anything that set it explicitly.
  if (info.shape === 'cylinder' || info.shape === 'marker') return info.shape
  return 'box'
}

/**
 * Builds a class's geometry at its real dimensions.
 *
 * length is the along-travel axis (+Z), width is across (X), height is up (Y).
 * The pieces are merged into one geometry so a whole fleet can still draw as a
 * single instanced mesh.
 */
export function formGeometry(form: Form, length: number, width: number, height: number): THREE.BufferGeometry {
  const l = Math.max(length, 0.2)
  const w = Math.max(width, 0.2)
  const h = Math.max(height, 0.2)

  switch (form) {
    case 'truck':
      return truck(l, w, h)
    case 'ship':
      return ship(l, w, h)
    case 'agv':
      return agv(l, w, h)
    case 'crane':
      return crane(l, w, h)
    case 'gate':
      return gate(l, w, h)
    case 'person':
      return person(l, w, h)
    case 'cylinder':
      return new THREE.CylinderGeometry(w / 2, w / 2, h, 12)
    case 'marker':
      return new THREE.SphereGeometry(Math.max(w, h) / 2, 12, 8)
    case 'container':
    case 'box':
    default:
      return new THREE.BoxGeometry(w, h, l)
  }
}

// box builds one piece and shifts it into place, forward being +Z and the
// ground at y = -h/2 so the merged result sits centred like a plain box.
function box(w: number, h: number, l: number, x: number, y: number, z: number): THREE.BufferGeometry {
  const g = new THREE.BoxGeometry(w, h, l)
  g.translate(x, y, z)
  return g
}

// A flatbed with a cab at the front and a box body behind: the cab is the front
// the heading points, which is the whole reason for the shape.
function truck(l: number, w: number, h: number): THREE.BufferGeometry {
  const bottom = -h / 2
  const deck = box(w, h * 0.14, l, 0, bottom + h * 0.07, 0)
  const cab = box(w, h * 0.5, l * 0.24, 0, bottom + h * 0.14 + h * 0.25, l * 0.5 - l * 0.12)
  const body = box(w * 0.96, h * 0.6, l * 0.64, 0, bottom + h * 0.14 + h * 0.3, -l * 0.5 + l * 0.34)
  return mergeGeometries([deck, cab, body], false)
}

// A hull with a pointed bow at +Z and a bridge near the stern. The footprint is
// drawn as a 2D outline and extruded, which is what gives the real ship taper a
// box cannot.
function ship(l: number, w: number, h: number): THREE.BufferGeometry {
  const hullHeight = h * 0.7
  const shape = new THREE.Shape()
  // Drawn in (x = beam, y = length) with the bow at +y; extruded to height,
  // then turned so length runs along +Z and height along +Y.
  shape.moveTo(-w / 2, -l / 2)
  shape.lineTo(w / 2, -l / 2)
  shape.lineTo(w / 2, l * 0.18)
  shape.lineTo(0, l / 2)
  shape.lineTo(-w / 2, l * 0.18)
  shape.closePath()

  const hull = new THREE.ExtrudeGeometry(shape, { depth: hullHeight, bevelEnabled: false })
  // Extrude runs the depth along +Z; rotate so that becomes up, and the drawn
  // length becomes forward.
  hull.rotateX(-Math.PI / 2)
  // After the rotation the hull sits from y = -hullHeight..0; drop it so the
  // whole form centres like the others.
  hull.translate(0, -h / 2 + hullHeight, 0)

  const bridge = box(w * 0.6, h * 0.3, l * 0.22, 0, -h / 2 + hullHeight + h * 0.15, -l * 0.28)
  return mergeGeometries([hull, bridge], false)
}

// A low body with a mast at the front, which reads as a forklift or an AGV.
function agv(l: number, w: number, h: number): THREE.BufferGeometry {
  const bottom = -h / 2
  const body = box(w, h * 0.5, l * 0.82, 0, bottom + h * 0.25, -l * 0.05)
  const mast = box(w * 0.7, h * 0.9, l * 0.12, 0, bottom + h * 0.45, l * 0.5 - l * 0.06)
  return mergeGeometries([body, mast], false)
}

// A gantry: two legs and a top beam. Cranes do not move here, but the form is
// used where a class stands in for one.
function crane(l: number, w: number, h: number): THREE.BufferGeometry {
  const legL = box(w * 0.12, h, l * 0.14, -w * 0.42, 0, 0)
  const legR = box(w * 0.12, h, l * 0.14, w * 0.42, 0, 0)
  const beam = box(w, h * 0.14, l * 0.3, 0, h / 2 - h * 0.07, 0)
  return mergeGeometries([legL, legR, beam], false)
}

// A barrier: two posts and a boom across them.
function gate(l: number, w: number, h: number): THREE.BufferGeometry {
  const postL = box(w * 0.12, h, l * 0.12, -w * 0.42, 0, 0)
  const postR = box(w * 0.12, h, l * 0.12, w * 0.42, 0, 0)
  const boom = box(w * 0.72, h * 0.12, l * 0.1, 0, h * 0.3, 0)
  return mergeGeometries([postL, postR, boom], false)
}

// A body and a head, so a pedestrian is a person rather than a small box.
function person(l: number, w: number, h: number): THREE.BufferGeometry {
  const radius = Math.min(w, l) * 0.3
  const bodyH = h * 0.62
  const body = new THREE.CylinderGeometry(radius, radius * 0.9, bodyH, 8)
  body.translate(0, -h / 2 + bodyH / 2, 0)
  const head = new THREE.SphereGeometry(radius * 0.9, 8, 6)
  head.translate(0, -h / 2 + bodyH + radius * 0.7, 0)
  return mergeGeometries([body, head], false)
}
