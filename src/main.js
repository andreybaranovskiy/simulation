/* ========================
   Imports & constants
======================== */
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { DRACOLoader } from 'three/addons/loaders/DRACOLoader.js';
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js';

const FOLLOW_DIST = 12;
const FOLLOW_HEIGHT = 4;
const PORT_URL = './port.glb';      // auto-load if present (GLB)
const PORT_TARGET_SIZE = 10000;     // fit longest axis to this size

/* ========================
   Scene setup
======================== */
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

/* ========================
   Helpers
======================== */
function resolveColor(input, fallback = 0x8888ff) {
  const c = new THREE.Color();
  try {
    if (input === undefined || input === null || input === '') c.set(fallback);
    else c.set(input); // accepts names, hex, rgb(), numbers
  } catch {
    c.set(fallback);
  }
  return c;
}

function makeTruck(color = 0xff6655) {
  const g = new THREE.Group();
  const bodyMat = new THREE.MeshStandardMaterial({ color, roughness: 0.6 });
  const body = new THREE.Mesh(new THREE.BoxGeometry(4.5, 2, 2.5), bodyMat);
  body.position.y = 1.2; g.add(body);

  const cabColor = (typeof color === 'number') ? ((color & 0xfefefe) ^ 0x222222) : 0xffffff;
  const cab = new THREE.Mesh(
    new THREE.BoxGeometry(2, 1.6, 2.4),
    new THREE.MeshStandardMaterial({ color: cabColor, roughness: 0.4 })
  );
  cab.position.set(-2.4, 1.5, 0); g.add(cab);
  return g;
}

/* ========================
   Environment (GLB or 2D PNG/JPG on plane)
======================== */
const gltfLoader = new GLTFLoader();
const draco = new DRACOLoader();
draco.setDecoderPath('https://unpkg.com/three@0.160.0/examples/jsm/libs/draco/');
gltfLoader.setDRACOLoader(draco);

const texLoader = new THREE.TextureLoader();

let envRoot = null;       // current environment node (GLB or plane group)
let envYawRad = 0;        // radians
let envPitchRad = 0;      // radians
let envOpacity = 1.0;
let envUserScale = 1.0;   // multiplies auto-fit
let animationTitle = '';

/** GLB loader */
async function loadPortGLB(url){
  return new Promise((resolve,reject)=>{
    gltfLoader.load(url, (gltf)=>{
      const root = gltf.scene || gltf.scenes?.[0];
      if (!root) return reject(new Error('No scene in GLB'));
      fitObjectUniform(root, PORT_TARGET_SIZE);
      root.userData.baseScale = root.scale.x || 1;
      applyEnvAppearance(root);
      resolve(root);
    }, undefined, reject);
  });
}

/** PNG/JPG loader -> plane at y=0, facing up (flat map) */
async function loadPortPNG(url){
  return new Promise((resolve, reject)=>{
    texLoader.load(url, (tex)=>{
      tex.colorSpace = THREE.SRGBColorSpace;
      tex.anisotropy = Math.min(16, renderer.capabilities.getMaxAnisotropy?.() || 8);

      const iw = tex.image?.naturalWidth || tex.image?.width || 1024;
      const ih = tex.image?.naturalHeight || tex.image?.height || 1024;
      const longest = Math.max(iw, ih);

      // Plane in "pixel units", then auto-fit scale applied to group
      const geo = new THREE.PlaneGeometry(iw, ih, 1, 1);
      const mat = new THREE.MeshBasicMaterial({
        map: tex,
        transparent: true,
        opacity: 1.0,
        depthWrite: true
      });
      const mesh = new THREE.Mesh(geo, mat);
      mesh.rotation.x = -Math.PI / 2; // lay flat
      mesh.position.y = 0.0;

      const group = new THREE.Group();
      group.add(mesh);

      const s = (PORT_TARGET_SIZE / longest);
      group.scale.setScalar(s);
      group.userData.baseScale = s;

      applyEnvAppearance(group);
      resolve(group);
    }, undefined, reject);
  });
}

/** Fit GLB model so its largest axis = PORT_TARGET_SIZE and sits roughly at y=0 */
function fitObjectUniform(obj, targetLongest=100){
  const box = new THREE.Box3().setFromObject(obj);
  const size = new THREE.Vector3(); box.getSize(size);
  const center = new THREE.Vector3(); box.getCenter(center);
  obj.position.sub(center);
  obj.position.y -= (box.min.y - center.y);
  const longest = Math.max(size.x, size.y, size.z) || 1;
  const s = targetLongest / longest;
  obj.scale.setScalar(s);
}

/** Apply yaw + pitch + opacity + scale to envRoot (GLB or plane) */
function applyEnvAppearance(root){
  if (!root) return;
  const base = root.userData.baseScale || 1;
  root.scale.setScalar(base * envUserScale);
  root.rotation.set(envPitchRad, envYawRad, 0);

  root.traverse?.(n=>{
    if (n.isMesh) {
      const mats = Array.isArray(n.material) ? n.material : [n.material];
      mats.forEach(m=>{
        if (!m) return;
        m.transparent = envOpacity < 1.0 || m.transparent;
        m.opacity = envOpacity;
        m.depthWrite = envOpacity >= 1.0;
      });
    }
  });

  // Simple plane group material toggle
  if (root.isGroup && root.children?.[0]?.material) {
    const m = root.children[0].material;
    m.transparent = envOpacity < 1.0 || m.transparent;
    m.opacity = envOpacity;
    m.depthWrite = envOpacity >= 1.0;
  }
}

/** Replace current environment with new root (GLB or plane) */
function setEnvironment(newRoot){
  if (envRoot) scene.remove(envRoot);
  envRoot = newRoot;
  if (envRoot) {
    scene.add(envRoot);
    applyEnvAppearance(envRoot);
  }
}

/** Autoload GLB if present (PNG/JPG is user-provided) */
fetch(PORT_URL, { method: 'HEAD' })
  .then(res => { if (res.ok) return loadPortGLB(PORT_URL); throw 0; })
  .then(root => { setEnvironment(root); console.log('Environment GLB loaded:', PORT_URL); })
  .catch(()=> console.log('No port.glb found (environment not autoloaded).'));

/* ========================
   Road ribbon from curve (legacy)
======================== */
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
    const a = i * 2;
    const b = a + 1;
    const c = a + 2;
    const d = a + 3;
    indices[idx++] = a; indices[idx++] = b; indices[idx++] = c;
    indices[idx++] = b; indices[idx++] = d; indices[idx++] = c;
  }

  const geom = new THREE.BufferGeometry();
  geom.setAttribute('position', new THREE.BufferAttribute(positions, 3));
  geom.setAttribute('uv', new THREE.BufferAttribute(uvs, 2));
  geom.setIndex(new THREE.BufferAttribute(indices, 1));
  geom.computeVertexNormals();

  const mat = new THREE.MeshStandardMaterial({
    color: 0x3a4368,
    roughness: 0.95,
    metalness: 0.0
  });

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
  roadMesh = built.mesh;
  roadLine = built.line;
  scene.add(roadMesh);
  scene.add(roadLine);
}

setRoadFromPoints([], 6);

/* ========================
   Sim state & registry
======================== */
let playing = false;
let playbackSpeed = 1.0;
let timeScale = 1.0;       // animation.time_scale
let simTime = 0;           // simulation seconds
let simEnd = 0;            // duration
let followId = null;
let loopOn = false;

const registry = new Map(); // id -> { mesh, color, keyframes: [{t,pos,quat}], tMin, tMax, lastPos, lastForward }

/* ========================
   Interpolation (pos only in legacy loadTracks)
======================== */
function lerp(a,b,t){ return a+(b-a)*t; }

function interpolatePR(kfs, t) {
  if (!kfs || !kfs.length) return { pos:new THREE.Vector3(), quat:new THREE.Quaternion() };
  if (t <= kfs[0].t) return { pos:kfs[0].pos.clone(), quat:kfs[0].quat.clone() };
  if (t >= kfs[kfs.length - 1].t) {
    const K = kfs[kfs.length - 1];
    return { pos:K.pos.clone(), quat:K.quat.clone() };
  }
  let lo = 0, hi = kfs.length - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (kfs[mid].t <= t) lo = mid; else hi = mid;
  }
  const A = kfs[lo], B = kfs[hi];
  const f = (t - A.t) / (B.t - A.t);
  return {
    pos: new THREE.Vector3( lerp(A.pos.x, B.pos.x, f), lerp(A.pos.y, B.pos.y, f), lerp(A.pos.z, B.pos.z, f) ),
    quat: A.quat.clone() // (legacy loadTracks not rotating)
  };
}

/* ========== Legend & UI references ========== */
const overlayEl = document.getElementById('overlay');
const legendEl = document.getElementById('legend');
const truckSelect = document.getElementById('truckSelect');

function focusCameraOnId(id, {hideLegend=true, instant=true} = {}) {
  if (!registry.has(id)) return;

  // Switch follow to this id and reflect in the dropdown
  followId = id;
  if (truckSelect) truckSelect.value = id;

  const rec = registry.get(id);
  const pos = rec.mesh.position.clone();
  const forward = rec.lastForward.lengthSq() > 0 ? rec.lastForward : new THREE.Vector3(1,0,0);

  // Where the follow-cam normally sits
  const desiredPos = pos.clone()
    .addScaledVector(forward, -FOLLOW_DIST)
    .add(new THREE.Vector3(0, FOLLOW_HEIGHT, 0));
  const lookTarget = new THREE.Vector3(pos.x, pos.y + 1.5, pos.z);

  if (instant) {
    camera.position.copy(desiredPos);
    controls.target.copy(lookTarget);
  } else {
    // A little nudge; the render loop lerps further
    camera.position.lerp(desiredPos, 0.35);
    controls.target.lerp(lookTarget, 0.35);
  }
  controls.update();

  if (hideLegend) setLegendVisible(false);
}


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
function focusCameraOnce(id, {instant=true, keepLegend=false} = {}) {
  const rec = registry.get(id);
  if (!rec) return;

  // Always release follow
  followId = null;
  if (truckSelect) truckSelect.value = '';

  const pos = rec.mesh.position.clone();
  const forward = rec.lastForward.lengthSq() > 0 ? rec.lastForward : new THREE.Vector3(1,0,0);

  // Same offset the follow-cam uses, but applied once
  const desiredPos = pos.clone()
    .addScaledVector(forward, -FOLLOW_DIST)
    .add(new THREE.Vector3(0, FOLLOW_HEIGHT, 0));
  const lookTarget = new THREE.Vector3(pos.x, pos.y + 1.5, pos.z);

  if (instant) {
    camera.position.copy(desiredPos);
    controls.target.copy(lookTarget);
  } else {
    camera.position.lerp(desiredPos, 0.35);
    controls.target.lerp(lookTarget, 0.35);
  }
  controls.update();


}

function updateLegend() {
  legendEl.innerHTML = '';
  if (animationTitle){
    const tchip = document.createElement('div');
    tchip.className = 'chip titlechip';
    tchip.textContent = animationTitle;
    legendEl.appendChild(tchip);
  }
  for (const [id, rec] of registry) {
    const chip = document.createElement('div'); chip.className='chip';
    const dot = document.createElement('span'); dot.className='dot';
    dot.style.background = '#' + rec.color.toString(16).padStart(6,'0');
    const txt = document.createElement('span'); txt.textContent = id;

    // Focus once, then stay in Free mode
    chip.addEventListener('click', () => focusCameraOnce(id));

    chip.appendChild(dot); chip.appendChild(txt);
    legendEl.appendChild(chip);
  }
  refreshTruckSelect(); // will show '' (Free) after focusing
}



function setLegendVisible(v){ overlayEl.classList.toggle('hidden', !v); }

/* ========================
   Timeline
======================== */
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

/* ========================
   Public APIs (legacy tracks)
======================== */
window.loadTracks = function(tracks) {
  for (const [, rec] of registry) scene.remove(rec.mesh);
  registry.clear();
  animationTitle = '';
  timeScale = 1;

  // global time span
  let globalMin = Infinity, globalMax = -Infinity;
  for (const t of tracks) {
    const kfs = [...t.keyframes].sort((a,b)=>a.t-b.t);
    if (!kfs.length) continue;
    globalMin = Math.min(globalMin, kfs[0].t);
    globalMax = Math.max(globalMax, kfs[kfs.length-1].t);
  }
  if (!isFinite(globalMin) || !isFinite(globalMax)) return;
  simEnd = Math.max(0, globalMax - globalMin);
  updateTimelineMax();

  for (const t of tracks) {
    const sorted = [...t.keyframes].sort((a,b)=>a.t-b.t);
    if (!sorted.length) continue;
    const kfs = sorted.map(k=>{
      const pos = new THREE.Vector3(+k.x, +(k.y||0), +k.z);
      return { t:+k.t - globalMin, pos, quat:new THREE.Quaternion() };
    });
    const color = 0x6bafff;
    const mesh = makeTruck(color);
    mesh.quaternion.identity();
    scene.add(mesh);
    registry.set(String(t.id), {
      mesh, color,
      keyframes: kfs,
      tMin: kfs[0].t, tMax: kfs[kfs.length-1].t,
      lastPos: kfs[0].pos.clone(),
      lastForward: new THREE.Vector3(1,0,0)
    });
  }

  setSimTime(0);
  playing = false;
  document.getElementById('playBtn').disabled = false;
  document.getElementById('pauseBtn').disabled = true;

  updateLegend();

  for (const [, rec] of registry) {
    const a = interpolatePR(rec.keyframes, 0);
    rec.mesh.position.copy(a.pos);
    rec.lastPos.copy(a.pos);
  }
};

window.loadRoad = function(points, opts = {}) {
  setRoadFromPoints(points, opts.width ?? roadWidth);
};

/* ========================
   NEW: Animation JSON API (pos only here; ignore any non-"move" transitions)
======================== */
window.loadAnimationJSON = function(obj){
  const { animation, objects, transition } = obj || {};
  animationTitle = animation?.name ? String(animation.name) : '';
  timeScale = Number.isFinite(+animation?.time_scale) ? +animation.time_scale : 1.0;

  for (const [, rec] of registry) scene.remove(rec.mesh);
  registry.clear();

  const byId = new Map();

  (objects || []).forEach(o=>{
    const id = String(o.id);
    const type = String(o.type || 'cube').toLowerCase();
    const col = resolveColor(o.color, type === 'container_truck' ? 0x333333 : 0x8888ff);

    let mesh;
    if (type === 'container_truck') mesh = makeTruck(col.getHex());
    else {
      const w = +(o.width || 2), h = +(o.height || 2), d = +(o.depth || o.width || 2);
      mesh = new THREE.Mesh(
        new THREE.BoxGeometry(Math.max(0.1, w/10), Math.max(0.1, h/10), Math.max(0.1, d/10)),
        new THREE.MeshStandardMaterial({ color: col, roughness: 0.6 })
      );
      mesh.position.y = Math.max(0.05, h/20);
    }
    mesh.quaternion.identity();
    scene.add(mesh);

    const p0 = new THREE.Vector3(+(o.x || 0), +(o.y || 0), +(o.z || 0));
    byId.set(id, {
      mesh,
      color: col.getHex(),
      keyframes: [{ t: 0, pos: p0.clone(), quat: new THREE.Quaternion() }],
      lastPos: p0.clone(),
      lastForward: new THREE.Vector3(1, 0, 0)
    });
  });

  (transition || []).forEach(tr=>{
    const id = String(tr.objId ?? tr.id ?? '');
    if (!byId.has(id)) return;
    const rec = byId.get(id);
    const t = +tr.time || 0;
    const kind = String(tr.type||'').toLowerCase();

    if (kind !== 'move') return; // anything else ignored

    let kf = rec.keyframes.find(k => k.t === t);
    if (!kf) {
      const last = rec.keyframes[rec.keyframes.length - 1];
      kf = { t, pos: last.pos.clone(), quat: last.quat?.clone?.() || new THREE.Quaternion() };
      rec.keyframes.push(kf);
    }
    kf.pos.set(+(tr.x ?? kf.pos.x), +(tr.y ?? kf.pos.y), +(tr.z ?? kf.pos.z));
  });

  let maxT = 0;
  for (const [id, rec] of byId.entries()){
    rec.keyframes.sort((a,b)=>a.t-b.t);
    for (let i = rec.keyframes.length - 2; i >= 0; i--) {
      if (rec.keyframes[i].t === rec.keyframes[i+1].t) rec.keyframes.splice(i, 1);
    }
    maxT = Math.max(maxT, rec.keyframes[rec.keyframes.length-1].t);
    registry.set(id, {
      mesh: rec.mesh,
      color: rec.color,
      keyframes: rec.keyframes,
      tMin: rec.keyframes[0].t,
      tMax: rec.keyframes[rec.keyframes.length-1].t,
      lastPos: rec.keyframes[0].pos.clone(),
      lastForward: new THREE.Vector3(1,0,0)
    });
  }
  simEnd = maxT;
  updateTimelineMax();

  for (const [, rec] of registry) {
    const a = interpolatePR(rec.keyframes, 0);
    rec.mesh.position.copy(a.pos);
    rec.mesh.quaternion.identity();
    rec.lastPos.copy(a.pos);
  }

  setSimTime(0);
  playing = false;
  document.getElementById('playBtn').disabled = false;
  document.getElementById('pauseBtn').disabled = true;
  updateLegend();
};

/* ========================
   Controls (incl. env load)
======================== */
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
const tlFwd  = document.getElementById('tlFwd');
const tlLoop = document.getElementById('tlLoop');

const glbYaw = document.getElementById('glbYaw');
const glbYawLabel = document.getElementById('glbYawLabel');
const glbPitch = document.getElementById('glbPitch');
const glbPitchLabel = document.getElementById('glbPitchLabel');
const glbScale = document.getElementById('glbScale');
const glbScaleLabel = document.getElementById('glbScaleLabel');
const glbOpacity = document.getElementById('glbOpacity');
const glbOpacityLabel = document.getElementById('glbOpacityLabel');

playBtn.addEventListener('click', ()=>{ playing = true; playBtn.disabled = true; pauseBtn.disabled = false; setLegendVisible(false); });
pauseBtn.addEventListener('click', ()=>{ playing = false; playBtn.disabled = false; pauseBtn.disabled = true; setLegendVisible(true); });
resetBtn.addEventListener('click', ()=>{ setSimTime(0); playing = false; playBtn.disabled = false; pauseBtn.disabled = true; setLegendVisible(true); });
speedSlider.addEventListener('input', ()=>{ const v=parseFloat(speedSlider.value); playbackSpeed=v; speedLabel.textContent = v.toFixed(1) + '×'; });
legendBtn.addEventListener('click', ()=> overlayEl.classList.toggle('hidden'));

freeCamBtn.addEventListener('click', ()=>{ followId = null; truckSelect.value = ''; });
truckSelect.addEventListener('change', () => { followId = truckSelect.value || null; });

loadBtn.addEventListener('click', () => fileInput.click());
fileInput.addEventListener('change', async (e) => {
  const f = e.target.files && e.target.files[0];
  if (f) { await handleDataFile(f); fileInput.value = ''; }
});

loadPortBtn.addEventListener('click', ()=> portInput.click());
portInput.addEventListener('change', async (e)=>{
  const f = e.target.files?.[0];
  if (!f) return;
  const name = f.name.toLowerCase();
  const url = URL.createObjectURL(f);
  try{
    if (name.endsWith('.glb') || name.endsWith('.gltf')) {
      setEnvironment(await loadPortGLB(url));
      console.log('Environment GLB loaded from file:', f.name);
    } else if (name.endsWith('.png') || name.endsWith('.jpg') || name.endsWith('.jpeg')) {
      setEnvironment(await loadPortPNG(url));
      console.log('Environment image loaded from file:', f.name);
    } else {
      alert('Unsupported environment file. Use .glb/.gltf or .png/.jpg');
    }
  }catch(err){
    console.error('Environment load failed:', err);
    alert('Failed to load environment: ' + err.message);
  }finally{
    URL.revokeObjectURL(url);
  }
});

// Drag & drop (.json/.csv for data; .glb/.gltf/.png/.jpg for environment)
wrap.addEventListener('dragover', (e)=>{ e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; });
wrap.addEventListener('drop', async (e)=>{
  e.preventDefault();
  const f = e.dataTransfer.files && e.dataTransfer.files[0];
  if (!f) return;
  const name = f.name.toLowerCase();
  if (name.endsWith('.glb') || name.endsWith('.gltf')) {
    const url = URL.createObjectURL(f);
    try { setEnvironment(await loadPortGLB(url)); }
    catch(err){ console.error(err); alert('Failed to load GLB: ' + err.message); }
    finally { URL.revokeObjectURL(url); }
  } else if (name.endsWith('.png') || name.endsWith('.jpg') || name.endsWith('.jpeg')) {
    const url = URL.createObjectURL(f);
    try { setEnvironment(await loadPortPNG(url)); }
    catch(err){ console.error(err); alert('Failed to load image: ' + err.message); }
    finally { URL.revokeObjectURL(url); }
  } else {
    await handleDataFile(f);
  }
});

/* --- Timeline UI events (FIX) --- */
let wasPlayingDuringScrub = false;
let scrubbing = false;

timeline.addEventListener('pointerdown', ()=>{
  wasPlayingDuringScrub = playing;
  playing = false;
  scrubbing = true;
});

window.addEventListener('pointerup', ()=>{
  if (!scrubbing) return;
  scrubbing = false;
  if (wasPlayingDuringScrub) {
    playing = true;
    playBtn.disabled = true;
    pauseBtn.disabled = false;
  }
});

timeline.addEventListener('input', ()=>{
  setSimTime(+timeline.value);
});

// Jump buttons
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

// Environment sliders (apply to GLB or PNG map)
glbYaw.addEventListener('input', ()=>{
  const deg = parseFloat(glbYaw.value);
  envYawRad = deg * Math.PI / 180;
  glbYawLabel.textContent = `${deg.toFixed(0)}°`;
  applyEnvAppearance(envRoot);
});
glbPitch.addEventListener('input', ()=>{
  const deg = parseFloat(glbPitch.value);
  envPitchRad = deg * Math.PI / 180;
  glbPitchLabel.textContent = `${deg.toFixed(0)}°`;
  applyEnvAppearance(envRoot);
});
glbScale.addEventListener('input', ()=>{
  envUserScale = parseFloat(glbScale.value);
  glbScaleLabel.textContent = `${envUserScale.toFixed(2)}×`;
  applyEnvAppearance(envRoot);
});
glbOpacity.addEventListener('input', ()=>{
  envOpacity = parseFloat(glbOpacity.value);
  glbOpacityLabel.textContent = envOpacity.toFixed(2);
  applyEnvAppearance(envRoot);
});

/* ========================
   Data file parsers
======================== */
async function handleDataFile(file) {
  const text = await file.text();
  try {
    if (file.name.toLowerCase().endsWith('.json') || text.trim().startsWith('{') || text.trim().startsWith('[')) {
      const data = JSON.parse(text);
      if (data.animation && (data.objects || data.transition)) {
        window.loadAnimationJSON(data);
      } else {
        const { road, tracks } = parseJSONMixedObject(data);
        if (road?.points?.length) window.loadRoad(road.points, { width: road.width });
        if (tracks?.length) window.loadTracks(tracks);
        if (!road && !tracks) throw new Error('No road or tracks found in JSON.');
      }
    } else {
      const res = parseCSVMixed(text);
      if (res.road?.length) window.loadRoad(res.road, { width: res.width });
      if (res.tracks?.length) window.loadTracks(res.tracks);
      if ((!res.road || !res.road.length) && (!res.tracks || !res.tracks.length)) {
        throw new Error('No road or tracks found in CSV.');
      }
    }
    if (registry.size) { playing = true; playBtn.disabled = true; pauseBtn.disabled = false; setLegendVisible(false); }
  } catch (err) {
    console.error(err);
    alert('Failed to load file: ' + err.message +
      '\n\nJSON (legacy): {"road":{"points":[{"x":..,"y":..,"z":..}], "width":6}, "tracks":[...]}\n' +
      'JSON (animation): {"animation":{"name":"..","time_scale":60},"objects":[...],"transition":[...]}\n' +
      'CSV: road as x,y,z (or x,z) OR add kind=road; tracks as id,t,x,y,z.'
    );
  }
}

function parseJSONMixedObject(data) {
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
    kind: col('kind'),
    id: col('id'),
    t: col('t'),
    x: col('x'),
    y: col('y'),
    z: col('z'),
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
      const id = idx.id >= 0 && cells[idx.id] ? String(cells[idx.id]) : `Obj ${uid}`;
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

/* ========================
   Render loop & follow-cam (motion-based)
======================== */
function resize() {
  const w = wrap.clientWidth || window.innerWidth;
  const headerH = document.querySelector('header')?.offsetHeight || 0;
  const tlH = document.getElementById('timelineBar')?.offsetHeight || 0;
  const h = wrap.clientHeight || (window.innerHeight - headerH - tlH);
  renderer.setSize(w, h);
  camera.aspect = w / h;
  camera.updateProjectionMatrix();
}
window.addEventListener('resize', resize);

// Update all objects for time t (position only, no rotation)
function updateAllAt(t){
  for (const [, rec] of registry) {
    const PR = interpolatePR(rec.keyframes, t);

    // Update position only
    rec.mesh.position.copy(PR.pos);

    // Maintain motion-based forward for follow cam
    const motion = new THREE.Vector3().subVectors(PR.pos, rec.lastPos).setY(0);
    if (motion.lengthSq() > 1e-10) {
      motion.normalize();
      rec.lastForward.copy(motion);
    }

    rec.lastPos.copy(PR.pos);
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
    setSimTime(simTime + dt * playbackSpeed * timeScale);
    if (simTime >= simEnd) {
      if (loopOn && simEnd > 0) {
        setSimTime(0);
      } else {
        setSimTime(simEnd);
        playing = false; playBtn.disabled = false; pauseBtn.disabled = true;
        setLegendVisible(true);
      }
    }
  }

  updateAllAt(simTime);

  // Follow camera uses motion direction only
  if (followId && registry.has(followId)) {
    const rec = registry.get(followId);
    const pos = rec.mesh.position;
    const forward = rec.lastForward.lengthSq() > 0 ? rec.lastForward : new THREE.Vector3(1,0,0);
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

/* ========================
   Auto-load data on start (optional)
======================== */
fetch("./demo.json")
  .then(r => r.json())
  .then(data => {
    if (data.animation || data.objects || data.transition) {
      window.loadAnimationJSON(data);
    } else {
      const { road, tracks } = parseJSONMixedObject(data);
      if (data.road?.points?.length) window.loadRoad(data.road.points, { width: data.road.width });
      if (tracks?.length) window.loadTracks(tracks);
    }
    console.log("Loaded demo.json");
  })
  .catch(err => console.warn("Could not auto-load demo.json:", err));
