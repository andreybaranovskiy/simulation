/* ======================== Imports & constants ======================== */
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { DRACOLoader } from 'three/addons/loaders/DRACOLoader.js';
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js';

const FOLLOW_DIST = 12;
const FOLLOW_HEIGHT = 4;
const PORT_URL = './port.glb'; // auto-load if present
const PORT_TARGET_SIZE = 10000; // fit longest axis to this size

/* ======================== Scene setup ======================== */
const wrap = document.getElementById('canvas-wrap');
const renderer = new THREE.WebGLRenderer({ antialias: true });
renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
wrap.appendChild(renderer.domElement);

const scene = new THREE.Scene();
scene.background = new THREE.Color(0x0b1020);

const camera = new THREE.PerspectiveCamera(60, 1, 0.1, 8000);
camera.position.set(80, 45, 80);

const controls = new OrbitControls(camera, renderer.domElement);
controls.enableDamping = true;

// Lights
scene.add(new THREE.HemisphereLight(0xb8c6ff, 0x18203a, 0.8));
const dir = new THREE.DirectionalLight(0xffffff, 0.7);
dir.position.set(80, 120, 30);
dir.castShadow = false;
scene.add(dir);

/* ======================== Port GLB (replaces ground) ======================== */
const gltfLoader = new GLTFLoader();
const draco = new DRACOLoader();
draco.setDecoderPath('https://unpkg.com/three@0.160.0/examples/jsm/libs/draco/');
gltfLoader.setDRACOLoader(draco);

let portRoot = null;
let portYawRad = 0; // stored yaw (radians)
let portOpacity = 1.0;

function fitObjectUniform(obj, targetLongest = 100) {
  const box = new THREE.Box3().setFromObject(obj);
  const size = new THREE.Vector3(); box.getSize(size);
  const center = new THREE.Vector3(); box.getCenter(center);

  // center and place on ground
  obj.position.sub(center);
  obj.position.y -= (box.min.y - center.y);

  const longest = Math.max(size.x, size.y, size.z) || 1;
  const s = targetLongest / longest;
  obj.scale.setScalar(s);
}

function applyPortAppearance(root) {
  if (!root) return;
  root.rotation.set(0, portYawRad, 0);
  root.traverse(n => {
    if (n.isMesh) {
      const mats = Array.isArray(n.material) ? n.material : [n.material];
      mats.forEach(m => {
        if (!m) return;
        m.transparent = portOpacity < 1.0 || m.transparent;
        m.opacity = portOpacity;
        m.depthWrite = portOpacity >= 1.0; // avoid sorting issues when transparent
      });
    }
  });
}

async function loadPort(url) {
  return new Promise((resolve, reject) => {
    gltfLoader.load(
      url,
      gltf => {
        const root = gltf.scene || gltf.scenes?.[0];
        if (!root) return reject(new Error('No scene in GLB'));
        fitObjectUniform(root, PORT_TARGET_SIZE);
        applyPortAppearance(root);
        resolve(root);
      },
      undefined,
      reject
    );
  });
}

// Try autoload port.glb; if missing, skip
loadPort(PORT_URL)
  .then(root => {
    portRoot = root;
    scene.add(portRoot);
    console.log('Port GLB loaded:', PORT_URL);
  })
  .catch(() => console.log('No port.glb found (skipping ground).'));

/* ======================== Road ribbon from curve ======================== */
let roadMesh = null, roadLine = null;
let roadWidth = 6;

function buildFlatRoadFromCurve(curve, width = 6, segments = 400) {
  const points = curve.getSpacedPoints(segments);
  const positions = new Float32Array((segments + 1) * 2 * 3);
  const uvs = new Float32Array((segments + 1) * 2 * 2);
  const indices = new Uint32Array(segments * 2 * 3);

  const left = new THREE.Vector3();
  const right = new THREE.Vector3();
  const tangent = new THREE.Vector3();
  const normal = new THREE.Vector3();

  for (let i = 0; i <= segments; i++) {
    const t = i / segments;
    const p = points[i];
    curve.getTangent(t, tangent);
    // 2D perp in XZ plane
    normal.set(-tangent.z, 0, tangent.x).normalize();
    left.copy(p).addScaledVector(normal, width * 0.5);
    right.copy(p).addScaledVector(normal, -width * 0.5);
    left.y = right.y = 0.02;

    const base = i * 6;
    positions[base + 0] = left.x;  positions[base + 1] = left.y;  positions[base + 2] = left.z;
    positions[base + 3] = right.x; positions[base + 4] = right.y; positions[base + 5] = right.z;

    const vbase = i * 4;
    uvs[vbase + 0] = 0; uvs[vbase + 1] = t * 20;
    uvs[vbase + 2] = 1; uvs[vbase + 3] = t * 20;
  }

  let idx = 0;
  for (let i = 0; i < segments; i++) {
    const a = i * 2, b = a + 1, c = a + 2, d = a + 3;
    indices[idx++] = a; indices[idx++] = b; indices[idx++] = c;
    indices[idx++] = b; indices[idx++] = d; indices[idx++] = c;
  }

  const geom = new THREE.BufferGeometry();
  geom.setAttribute('position', new THREE.BufferAttribute(positions, 3));
  geom.setAttribute('uv', new THREE.BufferAttribute(uvs, 2));
  geom.setIndex(new THREE.BufferAttribute(indices, 1));
  geom.computeVertexNormals();

  const mat = new THREE.MeshStandardMaterial({ color: 0x3a4368, roughness: 0.95, metalness: 0.0 });
  const mesh = new THREE.Mesh(geom, mat);
  mesh.receiveShadow = true;

  const lineGeom = new THREE.BufferGeometry().setFromPoints(points.map(p => new THREE.Vector3(p.x, 0.05, p.z)));
  const lineMat = new THREE.LineBasicMaterial({ color: 0x9bb4ff });
  const line = new THREE.Line(lineGeom, lineMat);
  return { mesh, line };
}

function setRoadFromPoints(pts, width = 6) {
  roadWidth = width || roadWidth;
  if (!pts || pts.length < 2) return;
  const v = pts.map(p => new THREE.Vector3(+p.x, +p.y || 0, +p.z));
  const curve = new THREE.CatmullRomCurve3(v);

  if (roadMesh) scene.remove(roadMesh);
  if (roadLine) scene.remove(roadLine);

  const built = buildFlatRoadFromCurve(curve, roadWidth, 400);
  roadMesh = built.mesh; roadLine = built.line;
  scene.add(roadMesh); scene.add(roadLine);
}

// Default empty (you’ll load from file)
setRoadFromPoints([{x:0,y:0,z:0},{x:1,y:0,z:1}], 6);

/* ======================== Sim state & registry ======================== */
let playing = false;
let playbackSpeed = 1.0;
let simTime = 0;
let simEnd = 0;
let followId = null;
let loopOn = false;

const registry = new Map(); // id -> { mesh, color, keyframes, tMin, tMax, lastPos }

/* ======================== Utilities ======================== */
function resize() {
  const w = wrap.clientWidth || window.innerWidth;
  const headerH = document.querySelector('header').offsetHeight;
  const tlH = document.getElementById('timelineBar').offsetHeight;
  const h = wrap.clientHeight || (window.innerHeight - headerH - tlH);
  renderer.setSize(w, h);
  camera.aspect = w / h;
  camera.updateProjectionMatrix();
}
window.addEventListener('resize', resize);

function lerp(a,b,t){ return a+(b-a)*t; }

function interpolateKF_safe(kfs, t) {
  if (!kfs || kfs.length === 0) return new THREE.Vector3();
  if (t <= kfs[0].t) return new THREE.Vector3(kfs[0].x, kfs[0].y, kfs[0].z);
  if (t >= kfs[kfs.length - 1].t) {
    const k = kfs[kfs.length - 1];
    return new THREE.Vector3(k.x,k.y,k.z);
  }
  let lo = 0, hi = kfs.length - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (kfs[mid].t <= t) lo = mid; else hi = mid;
  }
  const A = kfs[lo], B = kfs[hi];
  const f = (t - A.t) / (B.t - A.t);            // ← FIXED (no (b - a).t bug)
  return new THREE.Vector3(
    lerp(A.x,B.x,f),
    lerp(A.y,B.y,f),
    lerp(A.z,B.z,f)
  );
}
const interpolate = interpolateKF_safe;

function colorForIndex(i) {
  const palette = [0xff6b6b, 0x6bafff, 0x6bff95, 0xffe66b, 0xd86bff, 0x6bfff2, 0xffa06b];
  return palette[i % palette.length];
}

function makeTruck(color = 0xff6655) {
  const g = new THREE.Group();
  const body = new THREE.Mesh(
    new THREE.BoxGeometry(4.5, 2, 2.5),
    new THREE.MeshStandardMaterial({ color, roughness: 0.6 })
  );
  body.position.y = 1.2; g.add(body);

  const cab = new THREE.Mesh(
    new THREE.BoxGeometry(2, 1.6, 2.4),
    new THREE.MeshStandardMaterial({ color: (color & 0xfefefe) ^ 0x222222, roughness: 0.4 })
  );
  cab.position.set(-2.4, 1.5, 0); g.add(cab);
  return g;
}

/* ========== Legend & UI ========== */
const overlayEl = document.getElementById('overlay');
const truckSelect = document.getElementById('truckSelect');

function refreshTruckSelect(){
  const prev = truckSelect.value;
  truckSelect.innerHTML = '';
  const none = document.createElement('option');
  none.value = ''; none.textContent = '(none)';
  truckSelect.appendChild(none);

  for (const [id] of registry) {
    const opt = document.createElement('option');
    opt.value = id; opt.textContent = id;
    truckSelect.appendChild(opt);
  }
  const hasPrev = [...registry.keys()].includes(prev);
  truckSelect.value = hasPrev ? prev : '';
}

function updateLegend() {
  const el = document.getElementById('legend');
  el.innerHTML = '';
  const items = [...registry.entries()];
  const MAX = 12;

  const renderChip = (id, colorHex) => {
    const chip = document.createElement('div'); chip.className='chip';
    const dot = document.createElement('span'); dot.className='dot';
    dot.style.background = '#' + colorHex.toString(16).padStart(6,'0');
    const txt = document.createElement('span'); txt.textContent = id;
    chip.appendChild(dot); chip.appendChild(txt);
    return chip;
  };

  for (let i=0; i<Math.min(items.length, MAX); i++){
    const [id, rec] = items[i];
    el.appendChild(renderChip(id, rec.color));
  }
  const remaining = items.length - MAX;
  if (remaining > 0) {
    const more = document.createElement('button');
    more.className = 'btn';
    more.textContent = `${remaining} more`;      // ← FIXED template
    more.style.padding = '4px 8px';
    more.addEventListener('click', () => {
      el.innerHTML = '';
      for (const [id, rec] of items) el.appendChild(renderChip(id, rec.color));
    });
    el.appendChild(more);
  }

  refreshTruckSelect();
}

function setLegendVisible(v){ overlayEl.classList.toggle('hidden', !v); }

/* ======================== Orientation helpers ======================== */
const _tmpF = new THREE.Vector3();
const _tmpU = new THREE.Vector3();
const _tmpR = new THREE.Vector3();
const _quat = new THREE.Quaternion();
const _mat = new THREE.Matrix4();
const WORLD_UP = new THREE.Vector3(0,1,0);

function _quatFromForwardUp(forward, up) {
  _tmpF.copy(forward).normalize();
  _tmpR.copy(up).cross(_tmpF).normalize();
  _tmpU.copy(_tmpF).cross(_tmpR).normalize();
  _mat.makeBasis(_tmpF, _tmpU, _tmpR);
  _quat.setFromRotationMatrix(_mat);
  return _quat.clone();
}

function faceAlongMotion(mesh, prevPos, currPos, smooth = 0.25) {
  const dir = _tmpF.subVectors(currPos, prevPos);
  if (dir.lengthSq() < 1e-8) return;
  const target = _quatFromForwardUp(dir, WORLD_UP);
  mesh.quaternion.slerp(target, smooth);
}

/* ======================== Timeline helpers ======================== */
const timeline = document.getElementById('timeline');
const tlNow = document.getElementById('tlNow');
const tlDur = document.getElementById('tlDur');
const timeLabel = document.getElementById('timeLabel');

function fmtTime(t){
  t = Math.max(0, t);
  const m = Math.floor(t/60);
  const sWhole = Math.floor(t - m*60);
  const tenth = Math.floor((t - Math.floor(t))*10);
  return `${String(m).padStart(2,'0')}:${String(sWhole).padStart(2,'0')}.${tenth}`;
}
function clamp(v,min,max){ return Math.min(max, Math.max(min, v)); }

function setSimTime(t){
  simTime = clamp(t, 0, simEnd);
  timeline.value = simTime.toFixed(3);
  tlNow.textContent = fmtTime(simTime);
  timeLabel.textContent = simTime.toFixed(1) + 's';
}
function updateTimelineMax(){
  timeline.max = simEnd.toFixed(3);
  tlDur.textContent = fmtTime(simEnd);
}
function nudge(dt){ setSimTime(simTime + dt); }

/* ======================== Public APIs ======================== */
window.loadTracks = function(tracks) {
  for (const [, rec] of registry) scene.remove(rec.mesh);
  registry.clear();

  // global time span
  let globalMin = Infinity, globalMax = -Infinity, idx = 0;
  for (const t of tracks) {
    const kfs = [...t.keyframes].sort((a,b)=>a.t-b.t);
    if (!kfs.length) continue;
    globalMin = Math.min(globalMin, kfs[0].t);
    globalMax = Math.max(globalMax, kfs[kfs.length-1].t);
  }
  if (!isFinite(globalMin) || !isFinite(globalMax)) return;

  simEnd = Math.max(0, globalMax - globalMin);
  updateTimelineMax();

  // meshes
  idx = 0;
  for (const t of tracks) {
    const sorted = [...t.keyframes].sort((a,b)=>a.t-b.t);
    if (!sorted.length) continue;
    const kfs = sorted.map(k=>({ t: k.t - globalMin, x:+k.x, y:+(k.y||0), z:+k.z }));
    const color = colorForIndex(idx++);
    const mesh = makeTruck(color);
    scene.add(mesh);
    registry.set(String(t.id), {
      mesh, color, keyframes: kfs,
      tMin: kfs[0].t, tMax: kfs[kfs.length-1].t,
      lastPos: new THREE.Vector3(kfs[0].x,kfs[0].y,kfs[0].z)
    });
  }

  // reset state
  setSimTime(0);
  playing = false;
  document.getElementById('playBtn').disabled = false;
  document.getElementById('pauseBtn').disabled = true;

  updateLegend();

  // initial pose
  for (const [, rec] of registry) {
    const p0 = interpolate(rec.keyframes, 0);
    const p1 = interpolate(rec.keyframes, Math.min(0.1, rec.tMax));
    rec.mesh.position.copy(p0);
    rec.lastPos.copy(p0);
    faceAlongMotion(rec.mesh, p0, p1, 1.0);
  }
};

window.loadRoad = function(points, opts = {}) {
  setRoadFromPoints(points, opts.width ?? roadWidth);
};

/* ======================== Controls (incl. timeline & GLB) ======================== */
const playBtn = document.getElementById('playBtn');
const pauseBtn = document.getElementById('pauseBtn');
const resetBtn = document.getElementById('resetBtn');
const speedSlider = document.getElementById('speedSlider');
const speedLabel = document.getElementById('speedLabel');
const freeCamBtn = document.getElementById('freeCamBtn');
const legendBtn = document.getElementById('legendBtn');
const loadBtn = document.getElementById('loadBtn');
const fileInput = document.getElementById('fileInput');
const loadPortBtn = document.getElementById('loadPortBtn');
const portInput = document.getElementById('portInput');
const tlHome = document.getElementById('tlHome');
const tlBack = document.getElementById('tlBack');
const tlFwd = document.getElementById('tlFwd');
const tlLoop = document.getElementById('tlLoop');
const glbYaw = document.getElementById('glbYaw');
const glbYawLabel = document.getElementById('glbYawLabel');
const glbOpacity = document.getElementById('glbOpacity');
const glbOpacityLabel = document.getElementById('glbOpacityLabel');

playBtn.addEventListener('click', ()=>{
  playing = true;
  playBtn.disabled = true;
  pauseBtn.disabled = false;
  setLegendVisible(false);
});
pauseBtn.addEventListener('click', ()=>{
  playing = false;
  playBtn.disabled = false;
  pauseBtn.disabled = true;
  setLegendVisible(true);
});
resetBtn.addEventListener('click', ()=>{
  setSimTime(0);
  playing = false;
  playBtn.disabled = false;
  pauseBtn.disabled = true;
  setLegendVisible(true);
});
speedSlider.addEventListener('input', ()=>{
  playbackSpeed = parseFloat(speedSlider.value);
  speedLabel.textContent = playbackSpeed.toFixed(1) + '×';
});
legendBtn.addEventListener('click', ()=> overlayEl.classList.toggle('hidden'));
freeCamBtn.addEventListener('click', ()=>{ followId = null; truckSelect.value = ''; });
truckSelect.addEventListener('change', () => { followId = truckSelect.value || null; });

loadBtn.addEventListener('click', () => fileInput.click());
fileInput.addEventListener('change', async (e) => {
  const f = e.target.files && e.target.files[0];
  if (f) {
    await handleFile(f);
    fileInput.value = '';
  }
});

loadPortBtn.addEventListener('click', ()=> portInput.click());
portInput.addEventListener('change', async (e)=>{
  const f = e.target.files?.[0];
  if (!f) return;
  const url = URL.createObjectURL(f);
  try{
    const newPort = await loadPort(url);
    if (portRoot) scene.remove(portRoot);
    portRoot = newPort;
    scene.add(portRoot);
    applyPortAppearance(portRoot);
    console.log('Port GLB loaded from file:', f.name);
  }catch(err){
    console.error('Port GLB load failed:', err);
    alert('Failed to load GLB: ' + err.message);
  }finally{
    URL.revokeObjectURL(url);
  }
});

// Drag & drop (.json/.csv/.glb)
wrap.addEventListener('dragover', (e)=>{
  e.preventDefault();
  e.dataTransfer.dropEffect = 'copy';
});
wrap.addEventListener('drop', async (e)=>{
  e.preventDefault();
  const f = e.dataTransfer.files && e.dataTransfer.files[0];
  if (!f) return;
  const name = f.name.toLowerCase();
  if (name.endsWith('.glb') || name.endsWith('.gltf')){
    portInput.files = e.dataTransfer.files;
    portInput.dispatchEvent(new Event('change'));
  } else {
    await handleFile(f);
  }
});

// Timeline UI
let wasPlayingDuringScrub = false;
let scrubbing = false;
timeline.addEventListener('pointerdown', ()=>{
  wasPlayingDuringScrub = playing;
  playing = false;
  scrubbing = true;
});
window.addEventListener('pointerup', ()=>{
  if (scrubbing){
    scrubbing = false;
    if (wasPlayingDuringScrub) {
      playing = true;
      playBtn.disabled = true;
      pauseBtn.disabled = false;
    }
  }
});
timeline.addEventListener('input', ()=> setSimTime(+timeline.value));
tlHome.addEventListener('click', ()=> setSimTime(0));
tlBack.addEventListener('click', (e)=>{
  const step = e.shiftKey ? 5 : e.altKey ? 0.1 : 1;
  nudge(-step);
});
tlFwd.addEventListener('click', (e)=>{
  const step = e.shiftKey ? 5 : e.altKey ? 0.1 : 1;
  nudge(step);
});
tlLoop.addEventListener('click', ()=>{
  loopOn = !loopOn;
  tlLoop.classList.toggle('on', loopOn);
});

// Keyboard shortcuts
window.addEventListener('keydown', (e)=>{
  if (e.target && (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA')) return;
  if (e.code === 'Space'){ e.preventDefault(); (playing ? pauseBtn : playBtn).click(); }
  if (e.code === 'KeyL'){ tlLoop.click(); }
  if (e.code === 'ArrowRight'){ const step = e.shiftKey ? 5 : e.altKey ? 0.1 : 1; nudge(step); }
  if (e.code === 'ArrowLeft'){ const step = e.shiftKey ? 5 : e.altKey ? 0.1 : 1; nudge(-step); }
  if (e.code === 'Home'){ setSimTime(0); }
  if (e.code === 'End'){ setSimTime(simEnd); }
});

// GLB controls
glbYaw.addEventListener('input', ()=>{
  const deg = parseFloat(glbYaw.value);
  portYawRad = deg * Math.PI / 180;
  glbYawLabel.textContent = `${deg.toFixed(0)}°`;
  applyPortAppearance(portRoot);
});
glbOpacity.addEventListener('input', ()=>{
  portOpacity = parseFloat(glbOpacity.value);
  glbOpacityLabel.textContent = portOpacity.toFixed(2);
  applyPortAppearance(portRoot);
});

/* ======================== File parsers ======================== */
async function handleFile(file) {
  const text = await file.text();
  try {
    if (file.name.toLowerCase().endsWith('.json') || text.trim().startsWith('{') || text.trim().startsWith('[')) {
      const { road, tracks } = parseJSONMixed(text);
      if (road?.points?.length) window.loadRoad(road.points, { width: road.width });
      if (tracks?.length) window.loadTracks(tracks);
      if (!road && !tracks) throw new Error('No road or tracks found in JSON.');
    } else {
      const res = parseCSVMixed(text);
      if (res.road?.length) window.loadRoad(res.road, { width: res.width });
      if (res.tracks?.length) window.loadTracks(res.tracks);
      if ((!res.road || !res.road.length) && (!res.tracks || !res.tracks.length)) {
        throw new Error('No road or tracks found in CSV.');
      }
    }
    if (registry.size) {
      playing = true;
      playBtn.disabled = true;
      pauseBtn.disabled = false;
      setLegendVisible(false);
    }
  } catch (err) {
    console.error(err);
    alert(
      'Failed to load file: ' + err.message +
      '\n\nJSON: {"road":{"points":[{"x":..,"y":..,"z":..}], "width":6}, "tracks":[...]} or just one of them.' +
      '\nCSV: road as x,y,z (or x,z) OR add kind=road; tracks as id,t,x,y,z.'
    );
  }
}

function parseJSONMixed(text) {
  const data = JSON.parse(text);
  let road = null;

  if (data?.road?.points) {
    road = {
      points: data.road.points.map(p => ({ x:+p.x, y:+(p.y ?? 0), z:+p.z })),
      width: +data.road.width || undefined
    };
  } else if (Array.isArray(data) && data.length && 'x' in data[0] && 'z' in data[0] && !('t' in data[0])) {
    road = { points: data.map(p => ({ x:+p.x, y:+(p.y ?? 0), z:+p.z })) };
  }

  let tracks = null;
  if (Array.isArray(data?.tracks)) {
    tracks = data.tracks.map(tr => ({
      id: String(tr.id ?? 'Obj'),
      keyframes: (tr.keyframes ?? []).map(p => ({ t:+p.t, x:+p.x, y:+(p.y ?? 0), z:+p.z }))
    }));
  } else if (Array.isArray(data) && data.length && 't' in data[0]) {
    const byId = new Map();
    for (const row of data) {
      const id = String(row.id ?? 'Obj');
      if (!byId.has(id)) byId.set(id, []);
      byId.get(id).push({ t:+row.t, x:+row.x, y:+(row.y ?? 0), z:+row.z });
    }
    tracks = [...byId.entries()].map(([id, keyframes]) => ({ id, keyframes }));
  } else if (Array.isArray(data) && data.length && data[0]?.keyframes) {
    tracks = data.map(tr => ({
      id: String(tr.id ?? 'Obj'),
      keyframes: (tr.keyframes ?? []).map(p => ({ t:+p.t, x:+p.x, y:+(p.y ?? 0), z:+p.z }))
    }));
  }
  return { road, tracks };
}

function parseCSVMixed(text) {
  const lines = text.replace(/\r/g, '').split('\n').map(l => l.trim()).filter(Boolean);
  if (!lines.length) throw new Error('Empty CSV.');
  const header = lines[0].split(',').map(s => s.trim().toLowerCase());
  const col = (name) => header.indexOf(name);

  const idx = {
    kind: col('kind'), id: col('id'), t: col('t'),
    x: col('x'), y: col('y'), z: col('z'),
    rx: col('rx'), ry: col('ry'), rz: col('rz'),
    width: col('width')
  };

  const byId = new Map();
  const roadPoints = [];
  let width = undefined;
  let uid = 1;

  for (let i = 1; i < lines.length; i++) {
    const cells = lines[i].split(',').map(s => s.trim());
    if (!cells.length) continue;

    const kind = idx.kind >= 0 ? (cells[idx.kind] || '').toLowerCase() : '';
    const hasR = (idx.rx>=0 && cells[idx.rx] !== undefined) || (idx.rz>=0 && cells[idx.rz] !== undefined);
    const X = hasR ? +cells[idx.rx] : +cells[idx.x];
    const Y = hasR ? +(cells[idx.ry] ?? 0) : +(cells[idx.y] ?? 0);
    const Z = hasR ? +cells[idx.rz] : +cells[idx.z];
    const hasT = idx.t >= 0 && cells[idx.t] !== undefined && cells[idx.t] !== '';

    const looksLikeRoad = !hasT && !Number.isNaN(X) && !Number.isNaN(Z) && (kind === 'road' || idx.t < 0);
    if (looksLikeRoad) {
      roadPoints.push({ x:X, y:Y || 0, z:Z });
      if (idx.width >= 0 && cells[idx.width]) width = +cells[idx.width];
      continue;
    }

    if (hasT && !Number.isNaN(X) && !Number.isNaN(Z)) {
      const id = (idx.id >= 0 && cells[idx.id]) ? String(cells[idx.id]) : `Obj ${uid}`; // ← FIXED backtick template
      const t = +cells[idx.t];
      const y = Number.isNaN(Y) ? 0 : Y;
      if (!byId.has(id)) { byId.set(id, []); uid++; }
      byId.get(id).push({ t, x:X, y, z:Z });
    }
  }

  const tracks = [...byId.entries()].map(([id, keyframes]) => ({ id, keyframes }));
  const road = roadPoints.length ? roadPoints : null;
  return { road, tracks, width };
}

/* ======================== Demo tracks (visible immediately) ======================== */
window.loadTracks([]);

/* ======================== Camera follow & render loop ======================== */
function interpolateAllAt(t){
  for (const [, rec] of registry) {
    const prev = rec.lastPos.clone();
    const pos = interpolate(rec.keyframes, t);
    rec.mesh.position.copy(pos);
    faceAlongMotion(rec.mesh, prev, pos, 0.25);
    rec.lastPos.copy(pos);
  }
}

resize();
refreshTruckSelect();
updateLegend();

const clock = new THREE.Clock();
function tick() {
  requestAnimationFrame(tick);
  const dt = clock.getDelta();

  if (playing) {
    setSimTime(simTime + dt * playbackSpeed);
    if (simTime >= simEnd) {
      if (loopOn && simEnd > 0) {
        setSimTime(0);
      } else {
        setSimTime(simEnd);
        playing = false;
        playBtn.disabled = false;
        pauseBtn.disabled = true;
        setLegendVisible(true);
      }
    }
  }

  interpolateAllAt(simTime);

  if (followId && registry.has(followId)) {
    const rec = registry.get(followId);
    const pos = rec.mesh.position;
    const forward = new THREE.Vector3(1, 0, 0).applyQuaternion(rec.mesh.quaternion);
    const camPos = new THREE.Vector3()
      .copy(pos)
      .addScaledVector(forward, -FOLLOW_DIST)
      .add(new THREE.Vector3(0, FOLLOW_HEIGHT, 0));
    camera.position.lerp(camPos, 0.12);
    const lookTarget = new THREE.Vector3(pos.x, pos.y + 1.5, pos.z);
    camera.lookAt(lookTarget);
    controls.target.lerp(lookTarget, 0.12);
  }

  controls.update();
  renderer.render(scene, camera);
}
tick();

/* ======================== Auto-load data on start ======================== */
fetch("./demo_road_tracks.json")
  .then(r => r.json())
  .then(data => {
    if (data.road?.points?.length) window.loadRoad(data.road.points, { width: data.road.width });
    if (data.tracks?.length) window.loadTracks(data.tracks);
    console.log("Loaded demo_road_tracks.json");
  })
  .catch(err => console.warn("Could not auto-load demo_road_tracks.json:", err));
