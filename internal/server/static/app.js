import * as THREE from './lib/three.module.js';
import { OrbitControls } from './lib/OrbitControls.js';
import { buildLocalList, sizeNotice } from './local.js';

const $ = (id) => document.getElementById(id);
const PALETTE = [0x6aa9ff, 0xffb454, 0x7ee787, 0xff7b72, 0xd2a8ff, 0x56d4dd, 0xf2cc60, 0xff9bce];

const state = {
  files: [],
  selected: null, // 選択中ファイルの相対パス
  model: null, // 読み込み済みモデル (API レスポンス)
  item: 'all', // 'all' | item index
  loadSeq: 0,
  loading: 0, // 進行中のプレビュー読み込み数
  // 'server': 起動時のフォルダ (サーバが読む) / 'local': ブラウザで選んだフォルダ (中身を解析用に送る)
  source: 'server',
  localRoot: '', // local のときに選んだフォルダ名
  gen: 0, // 一覧の世代。source 切替・フォルダ再選択で増やし、古い非同期結果を捨てる
};
const isLocal = () => state.source === 'local';
const LOCAL_REASON = 'ブラウザで読み込んだフォルダでは使えない (絶対パスが取れず、サーバ側のファイルとして扱えないため)';

// ---------- ユーティリティ ----------

let toastTimer = 0;
function toast(msg) {
  const el = $('toast');
  el.textContent = msg;
  el.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove('show'), 1800);
}

async function copyText(text, label) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
    } else {
      legacyCopy(text);
    }
    toast(`${label}をコピーした: ${text}`);
  } catch (e) {
    try {
      legacyCopy(text);
      toast(`${label}をコピーした: ${text}`);
    } catch (e2) {
      toast('コピーに失敗した');
    }
  }
}

function legacyCopy(text) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  const ok = document.execCommand('copy');
  document.body.removeChild(ta);
  if (!ok) throw new Error('copy failed');
}

function fmtSize(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

async function getJSON(url) {
  const res = await fetch(url, { cache: 'no-store' });
  let body = null;
  try { body = await res.json(); } catch (e) { /* ignore */ }
  if (!res.ok) throw new Error((body && body.error) || `HTTP ${res.status}`);
  return body;
}

const viewerToken = () => {
  const m = document.querySelector('meta[name="viewer-token"]');
  return m ? m.content : '';
};

// ブラウザで選んだファイルの中身を送り、サーバの既存パーサで解析した結果だけを受け取る (サーバには保存されない)。
async function parseLocal(f) {
  const res = await fetch(`/api/parse?name=${encodeURIComponent(f.name)}`, {
    method: 'POST',
    cache: 'no-store',
    headers: { 'Content-Type': 'application/octet-stream', 'X-Viewer-Token': viewerToken() },
    body: f.file,
  });
  let body = null;
  try { body = await res.json(); } catch (e) { /* ignore */ }
  if (!res.ok) throw new Error((body && body.error) || `HTTP ${res.status}`);
  return body;
}

function fetchModel(f) {
  return isLocal() ? parseLocal(f) : getJSON(`/api/model?path=${encodeURIComponent(f.path)}`);
}

async function postJSON(url, data) {
  const res = await fetch(url, {
    method: 'POST',
    cache: 'no-store',
    headers: { 'Content-Type': 'application/json', 'X-Viewer-Token': viewerToken() },
    body: JSON.stringify(data),
  });
  let body = null;
  try { body = await res.json(); } catch (e) { /* ignore */ }
  if (!res.ok) throw new Error((body && body.error) || `HTTP ${res.status}`);
  return body;
}

function b64ToBuffer(b64) {
  const bin = atob(b64);
  const buf = new ArrayBuffer(bin.length);
  const u8 = new Uint8Array(buf);
  for (let i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
  return buf;
}

function buildGeometry(item) {
  const g = new THREE.BufferGeometry();
  g.setAttribute('position', new THREE.BufferAttribute(new Float32Array(b64ToBuffer(item.vertices)), 3));
  g.setIndex(new THREE.BufferAttribute(new Uint32Array(b64ToBuffer(item.indices)), 1));
  g.computeBoundingBox();
  g.computeBoundingSphere();
  return g;
}

function makeMaterial(i, wire) {
  return new THREE.MeshStandardMaterial({
    color: PALETTE[i % PALETTE.length],
    metalness: 0.1,
    roughness: 0.6,
    flatShading: true,
    side: THREE.DoubleSide,
    wireframe: !!wire,
  });
}

function addLights(scene, camera) {
  scene.add(new THREE.HemisphereLight(0xffffff, 0x444455, 1.1));
  const key = new THREE.DirectionalLight(0xffffff, 2.2);
  key.position.set(0.4, 0.3, 1); // カメラ基準 (ヘッドライト)
  camera.add(key);
  scene.add(camera);
}

// 3D プリンタ系データに合わせて Z-up。斜め上から見る初期方向。
const VIEW_DIR = new THREE.Vector3(1, -1.2, 0.9).normalize();

// ---------- プレビュー ----------

const viewer = (() => {
  const canvas = $('canvas');
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(45, 1, 0.1, 1000);
  camera.up.set(0, 0, 1);
  addLights(scene, camera);
  const controls = new OrbitControls(camera, canvas);
  controls.screenSpacePanning = true;
  const group = new THREE.Group();
  scene.add(group);
  let grid = null;
  let queued = false;

  function render() {
    queued = false;
    renderer.render(scene, camera);
  }
  function requestRender() {
    if (queued) return;
    queued = true;
    requestAnimationFrame(render);
  }
  controls.addEventListener('change', requestRender);

  function resize() {
    const w = canvas.clientWidth;
    const h = canvas.clientHeight;
    if (w === 0 || h === 0) return;
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
    requestRender();
  }
  new ResizeObserver(resize).observe(canvas);

  function clear() {
    for (const m of group.children) {
      m.geometry.dispose();
      m.material.dispose();
    }
    group.clear();
    if (grid) {
      scene.remove(grid);
      grid.geometry.dispose();
      grid.material.dispose();
      grid = null;
    }
  }

  function fit() {
    const box = new THREE.Box3().setFromObject(group);
    if (box.isEmpty()) return;
    const sphere = box.getBoundingSphere(new THREE.Sphere());
    const r = Math.max(sphere.radius, 1e-3);
    const dist = (r / Math.sin((camera.fov * Math.PI) / 360)) * 1.1;
    camera.position.copy(sphere.center).addScaledVector(VIEW_DIR, dist);
    camera.near = dist / 1000;
    camera.far = dist * 100;
    camera.updateProjectionMatrix();
    controls.target.copy(sphere.center);
    controls.maxDistance = dist * 20;
    controls.update();
    requestRender();
  }

  // items: [{geometry, index}]
  function show(items, { keepCamera, wire, showGrid }) {
    clear();
    for (const it of items) {
      group.add(new THREE.Mesh(it.geometry, makeMaterial(it.index, wire)));
    }
    const box = new THREE.Box3().setFromObject(group);
    if (!box.isEmpty()) {
      const size = box.getSize(new THREE.Vector3());
      const span = Math.max(size.x, size.y, 1e-3);
      const cell = niceStep(span / 10);
      const n = Math.max(2, Math.ceil((span * 1.6) / cell));
      grid = new THREE.GridHelper(n * cell, n, 0x556070, 0x2f3846);
      grid.rotation.x = Math.PI / 2; // XY 平面
      const c = box.getCenter(new THREE.Vector3());
      grid.position.set(c.x, c.y, box.min.z);
      grid.visible = showGrid;
      scene.add(grid);
    }
    if (!keepCamera) fit();
    else requestRender();
  }

  function setGrid(v) {
    if (grid) grid.visible = v;
    requestRender();
  }
  function setWire(v) {
    for (const m of group.children) m.material.wireframe = v;
    requestRender();
  }

  resize();
  return { show, fit, clear, setGrid, setWire, requestRender };
})();

function niceStep(x) {
  const p = Math.pow(10, Math.floor(Math.log10(x)));
  const f = x / p;
  return (f < 1.5 ? 1 : f < 3.5 ? 2 : f < 7.5 ? 5 : 10) * p;
}

// ---------- サムネイル ----------

const thumbs = (() => {
  const W = 192;
  const H = 144;
  const mem = new Map(); // path -> {sig, url | null(失敗)}
  const queue = [];
  const queued = new Set();
  let running = false;
  let renderer = null;
  let scene = null;
  let camera = null;
  let holder = null;

  const MAX_THUMB_TRIANGLES = 1_500_000;
  const BIG_MARK = 'big'; // localStorage 上で「巨大のため省略」を表す値
  const sigOf = (f) => `${f.size}:${f.modTime}`;
  // ブラウザで選んだフォルダのファイルは、サーバ側の同名パスや、別のフォルダ (同名でも) と混ざらないよう、
  // フォルダを開くたびに変わる世代 (state.gen) 付きの名前空間にし、localStorage には保存しない。
  const keyOf = (path) => (isLocal() ? `local:${state.gen}:${path}` : path);
  const lsKeyOf = (path) => `3mfv:thumb:${keyOf(path)}`;
  const lsKey = (f) => lsKeyOf(f.path);

  function lsGet(f) {
    if (isLocal()) return null;
    try {
      const raw = localStorage.getItem(lsKey(f));
      if (!raw) return null;
      const i = raw.indexOf('|');
      if (raw.slice(0, i) !== sigOf(f)) return null;
      return raw.slice(i + 1);
    } catch (e) { return null; }
  }
  function lsSet(f, url) {
    if (isLocal()) return;
    const val = `${sigOf(f)}|${url}`;
    try {
      localStorage.setItem(lsKey(f), val);
    } catch (e) {
      try {
        for (const k of Object.keys(localStorage)) if (k.startsWith('3mfv:thumb:')) localStorage.removeItem(k);
        localStorage.setItem(lsKey(f), val);
      } catch (e2) { /* 保存できなくても動作には影響しない */ }
    }
  }

  function init() {
    if (renderer) return;
    renderer = new THREE.WebGLRenderer({ antialias: true, preserveDrawingBuffer: true });
    renderer.setSize(W, H, false);
    renderer.setClearColor(0x14171c, 1);
    scene = new THREE.Scene();
    camera = new THREE.PerspectiveCamera(40, W / H, 0.1, 1000);
    camera.up.set(0, 0, 1);
    addLights(scene, camera);
    holder = new THREE.Group();
    scene.add(holder);
  }

  function renderModel(model) {
    init();
    for (const m of holder.children) {
      m.geometry.dispose();
      m.material.dispose();
    }
    holder.clear();
    model.items.forEach((it, i) => holder.add(new THREE.Mesh(buildGeometry(it), makeMaterial(i, false))));
    const box = new THREE.Box3().setFromObject(holder);
    const s = box.getBoundingSphere(new THREE.Sphere());
    const r = Math.max(s.radius, 1e-3);
    const dist = (r / Math.sin((camera.fov * Math.PI) / 360)) * 1.05;
    camera.position.copy(s.center).addScaledVector(VIEW_DIR, dist);
    camera.near = dist / 1000;
    camera.far = dist * 100;
    camera.lookAt(s.center);
    camera.updateProjectionMatrix();
    camera.updateMatrixWorld(true);
    renderer.render(scene, camera);
    return renderer.domElement.toDataURL('image/jpeg', 0.82);
  }

  function apply(f, el) {
    const e = mem.get(keyOf(f.path));
    if (!e || e.sig !== sigOf(f)) return false;
    if (e.url) {
      el.style.backgroundImage = `url("${e.url}")`;
      el.textContent = '';
    } else {
      el.textContent = e.big ? '大容量' : '表示不可';
    }
    return true;
  }

  async function pump() {
    if (running) return;
    running = true;
    while (queue.length) {
      // ユーザーがプレビューを開いている間は、重い取得・描画が競合しないよう待つ。
      while (state.loading > 0) await new Promise((r) => setTimeout(r, 200));
      const f = queue.shift();
      queued.delete(f.path);
      const gen = state.gen;
      const cur = state.files.find((x) => x.path === f.path);
      if (!cur) continue;
      const key = keyOf(cur.path);
      let url = null;
      let big = false;
      try {
        const model = await fetchModel(cur);
        if (gen !== state.gen) continue; // 取得中にフォルダが切り替わった
        // 3MF は圧縮されているため、ファイルサイズが小さくても展開後は巨大なことがある。
        const tris = model.items.reduce((n, it) => n + it.triangleCount, 0);
        if (tris > MAX_THUMB_TRIANGLES) {
          big = true;
          lsSet(cur, BIG_MARK);
        } else {
          url = renderModel(model);
          lsSet(cur, url);
        }
      } catch (e) { if (gen !== state.gen) continue; /* サムネ失敗は「表示不可」とする */ }
      mem.set(key, { sig: sigOf(cur), url, big });
      for (const el of document.querySelectorAll('.thumb')) {
        if (el.dataset.path === cur.path) apply(cur, el);
      }
    }
    running = false;
  }

  const io = new IntersectionObserver((entries) => {
    for (const ent of entries) {
      if (!ent.isIntersecting) continue;
      io.unobserve(ent.target);
      const f = state.files.find((x) => x.path === ent.target.dataset.path);
      if (f) request(f, ent.target);
    }
  }, { root: $('file-list'), rootMargin: '200px' });

  // 巨大なモデルはサムネイル生成のための転送・描画が重いので省略する (プレビューは可能)。
  const MAX_THUMB_BYTES = 16 * 1024 * 1024;

  function request(f, el) {
    if (apply(f, el)) return;
    if (f.size > MAX_THUMB_BYTES) {
      mem.set(keyOf(f.path), { sig: sigOf(f), url: null, big: true });
      apply(f, el);
      return;
    }
    const cached = lsGet(f);
    if (cached === BIG_MARK) {
      mem.set(keyOf(f.path), { sig: sigOf(f), url: null, big: true });
      apply(f, el);
      return;
    }
    if (cached) {
      mem.set(keyOf(f.path), { sig: sigOf(f), url: cached });
      apply(f, el);
      return;
    }
    if (queued.has(f.path)) return;
    queued.add(f.path);
    queue.push(f);
    pump();
  }

  // ファイルが変更・削除されたら、メモリと localStorage の両方のキャッシュを捨てる。
  // (サイズ・更新時刻が過去の値に戻ったときに古いサムネイルが復活しないようにする)
  function forget(path) {
    mem.delete(keyOf(path));
    try { localStorage.removeItem(lsKeyOf(path)); } catch (e) { /* 無視 */ }
  }

  // 改名時は、サイズ・更新時刻が変わらないので、キャッシュを新しいパスへ引き継ぐ。
  function move(from, to) {
    const e = mem.get(keyOf(from));
    mem.delete(keyOf(from));
    if (e) mem.set(keyOf(to), e);
    try {
      const raw = localStorage.getItem(lsKeyOf(from));
      localStorage.removeItem(lsKeyOf(from));
      if (raw) localStorage.setItem(lsKeyOf(to), raw);
    } catch (e2) { /* 無視 */ }
  }

  // 一覧を差し替えるとき、待機中の生成を捨てる (進行中の 1 件は gen の不一致で結果が捨てられる)。
  function reset() {
    queue.length = 0;
    queued.clear();
    for (const k of [...mem.keys()]) if (k.startsWith('local:')) mem.delete(k); // 前のフォルダ分の Data URL を捨てる
  }

  return { observe: (el) => io.observe(el), forget, move, reset };
})();

// ---------- 一覧 ----------

function visibleFiles() {
  const q = $('filter').value.trim().toLowerCase();
  if (!q) return state.files;
  return state.files.filter((f) => f.path.toLowerCase().includes(q));
}

function renderList() {
  const ul = $('file-list');
  const files = visibleFiles();
  ul.textContent = '';
  $('count').textContent = `${files.length} / ${state.files.length} ファイル`;
  for (const f of files) {
    const li = document.createElement('li');
    li.className = 'file' + (f.path === state.selected ? ' selected' : '');
    li.dataset.path = f.path;
    li.title = isLocal() ? f.path : f.absPath;

    const th = document.createElement('div');
    th.className = 'thumb';
    th.dataset.path = f.path;
    th.textContent = '…';

    const body = document.createElement('div');
    body.className = 'file-body';
    const name = document.createElement('div');
    name.className = 'file-name';
    name.textContent = f.name;
    const dir = document.createElement('div');
    dir.className = 'file-dir muted';
    dir.textContent = f.dir || '(ルート直下)';
    const meta = document.createElement('div');
    meta.className = 'file-meta';
    const badge = document.createElement('span');
    badge.className = 'badge';
    badge.textContent = f.kind;
    const size = document.createElement('span');
    size.className = 'muted';
    size.textContent = fmtSize(f.size);
    const sp = document.createElement('span');
    sp.className = 'spacer';
    const cn = document.createElement('button');
    cn.type = 'button';
    cn.textContent = '名前';
    cn.title = 'ファイル名をコピー';
    cn.addEventListener('click', (e) => { e.stopPropagation(); copyText(f.name, 'ファイル名'); });
    const cp = document.createElement('button');
    cp.type = 'button';
    cp.textContent = isLocal() ? '相対' : 'パス';
    cp.title = isLocal() ? '選んだフォルダからの相対パスをコピー (絶対パスではない)' : '絶対パスをコピー';
    cp.addEventListener('click', (e) => { e.stopPropagation(); copyText(...pathCopy(f)); });
    const rn = document.createElement('button');
    rn.type = 'button';
    rn.textContent = '改名';
    rn.title = isLocal() ? `名前の変更は使えない: ${LOCAL_REASON}` : 'ファイル名を変更';
    rn.disabled = isLocal();
    rn.addEventListener('click', (e) => { e.stopPropagation(); openRename(f); });
    meta.append(badge, size, sp, rn, cn, cp);
    body.append(name, dir, meta);
    li.append(th, body);
    li.addEventListener('click', () => select(f.path));
    ul.appendChild(li);
    thumbs.observe(th);
  }
}

// コピーするパス文字列とラベル。ブラウザ読み込み時は絶対パスが無いので、選んだフォルダからの相対パスにする。
function pathCopy(f) {
  return isLocal() ? [f.path, '相対パス'] : [f.absPath, 'パス'];
}

// 過去に見たファイル情報 (path -> file)。SSE の removed/added から「改名」を判別するために使う。
const lastSeen = new Map();

async function refreshFiles() {
  if (isLocal()) return; // ブラウザで選んだフォルダは自動更新しない
  try {
    state.files = await getJSON('/api/files');
  } catch (e) {
    toast(`一覧の取得に失敗: ${e.message}`);
    return;
  }
  for (const f of state.files) lastSeen.set(f.path, f);
  renderList();
}

// SSE の差分のうち、removed と added が同一ファイル (同じフォルダ・種別・サイズ・更新時刻) の
// 対になっているものを改名とみなし、旧パス -> 新パスの Map を返す。
function detectRenames(c) {
  const out = new Map();
  const used = new Set();
  for (const p of c.removed) {
    const o = lastSeen.get(p);
    if (!o) continue;
    const q = c.added.find((a) => {
      if (used.has(a)) return false;
      const n = state.files.find((f) => f.path === a);
      return n && n.dir === o.dir && n.kind === o.kind && n.size === o.size && n.modTime === o.modTime;
    });
    if (q) {
      used.add(q);
      out.set(p, q);
    }
  }
  return out;
}

// 選択中のファイルが改名されたとき、モデルは再取得せず、選択・ハッシュ・表示だけを新しいパスへ移す。
function followRename(from, to) {
  if (state.selected !== from) return;
  state.selected = to;
  history.replaceState(null, '', `#${encodeURIComponent(to)}`);
  for (const li of document.querySelectorAll('.file')) li.classList.toggle('selected', li.dataset.path === to);
  updateHeader();
}

// ---------- 選択・プレビュー ----------

function currentFile() {
  return state.files.find((f) => f.path === state.selected) || null;
}

function setMessage(text, isError) {
  const el = $('message');
  el.textContent = text || '';
  el.classList.toggle('error', !!isError);
  el.hidden = !text;
}

function updateHeader() {
  const f = currentFile();
  $('title').textContent = f ? f.name : (state.selected || 'ファイルを選択');
  $('subtitle').textContent = f ? (isLocal() ? `${state.localRoot}/ からの相対パス: ${f.path}` : f.absPath) : '';
  $('rename').disabled = !f || isLocal();
  $('rename').title = isLocal() ? `名前の変更は使えない: ${LOCAL_REASON}` : '';
  $('copy-name').disabled = !f;
  $('copy-path').disabled = !f;
  $('copy-path').textContent = isLocal() ? '相対パスをコピー' : 'パスをコピー';
  $('copy-path').title = isLocal() ? '選んだフォルダからの相対パス (絶対パスではない)' : '';
}

function select(path) {
  state.selected = path;
  state.item = 'all';
  state.model = null;
  if (!isLocal()) location.hash = encodeURIComponent(path);
  for (const li of document.querySelectorAll('.file')) li.classList.toggle('selected', li.dataset.path === path);
  loadModel({ keepCamera: false });
}

async function loadModel(opts) {
  state.loading++;
  try {
    await loadModelInner(opts);
  } finally {
    state.loading--;
  }
}

async function loadModelInner({ keepCamera }) {
  const f = currentFile();
  updateHeader();
  if (!f) {
    viewer.clear();
    viewer.requestRender();
    $('items').hidden = true;
    $('toolbar').hidden = true;
    setMessage(state.selected ? 'ファイルが見つからない (削除された可能性)' : '左の一覧からモデルを選択してください', !!state.selected);
    $('status').textContent = '';
    return;
  }
  const seq = ++state.loadSeq;
  const notice = isLocal() ? sizeNotice(f.size) : null;
  if (notice && notice.block) {
    viewer.clear();
    viewer.requestRender();
    $('items').hidden = true;
    $('toolbar').hidden = true;
    setMessage(notice.text, true);
    return;
  }
  if (!keepCamera) setMessage(notice ? notice.text : '読み込み中…');
  let model;
  try {
    model = await fetchModel(f);
  } catch (e) {
    if (seq !== state.loadSeq) return;
    viewer.clear();
    viewer.requestRender();
    $('items').hidden = true;
    $('toolbar').hidden = true;
    setMessage(`読み込みに失敗: ${e.message}`, true);
    return;
  }
  if (seq !== state.loadSeq) return; // より新しい読み込みが走っている
  state.model = model;
  if (state.item !== 'all' && !model.items[state.item]) state.item = 'all';
  setMessage('');
  $('toolbar').hidden = false;
  renderItems();
  showItems({ keepCamera });
}

function renderItems() {
  const box = $('items');
  const items = state.model.items;
  box.textContent = '';
  if (items.length <= 1) {
    box.hidden = true;
    return;
  }
  box.hidden = false;
  const mk = (label, key) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'chip' + (state.item === key ? ' active' : '');
    b.textContent = label;
    b.addEventListener('click', () => {
      state.item = key;
      renderItems();
      showItems({ keepCamera: false });
    });
    box.appendChild(b);
  };
  mk(`全体 (${items.length})`, 'all');
  items.forEach((it, i) => mk(`${i + 1}. ${it.name}`, i));
}

function showItems({ keepCamera }) {
  const model = state.model;
  const list = state.item === 'all'
    ? model.items.map((it, i) => ({ it, i }))
    : [{ it: model.items[state.item], i: state.item }];
  viewer.show(
    list.map(({ it, i }) => ({ geometry: buildGeometry(it), index: i })),
    { keepCamera, wire: $('opt-edges').checked, showGrid: $('opt-grid').checked },
  );
  const mn = [Infinity, Infinity, Infinity];
  const mx = [-Infinity, -Infinity, -Infinity];
  let tris = 0;
  for (const { it } of list) {
    tris += it.triangleCount;
    for (let k = 0; k < 3; k++) {
      mn[k] = Math.min(mn[k], it.min[k]);
      mx[k] = Math.max(mx[k], it.max[k]);
    }
  }
  const dim = mx.map((v, k) => (v - mn[k]).toFixed(1)).join(' × ');
  $('status').textContent = `${model.format.toUpperCase()} · 三角形 ${tris.toLocaleString()} · 寸法 ${dim} (モデル単位, 3MF は mm) · 左ドラッグ: 回転 / ホイール: ズーム / 右ドラッグ: パン`;
}

// ---------- 名前の変更 ----------

const renameDlg = {
  dlg: $('rename-dialog'),
  input: $('rename-input'),
  err: $('rename-error'),
  file: null,
  busy: false,
};

function splitExt(name) {
  const i = name.lastIndexOf('.');
  return i > 0 ? [name.slice(0, i), name.slice(i)] : [name, ''];
}

function openRename(f) {
  if (isLocal()) return;
  const [base, ext] = splitExt(f.name);
  renameDlg.file = f;
  renameDlg.err.textContent = '';
  $('rename-dir').textContent = f.dir ? `フォルダ: ${f.dir}` : 'フォルダ: (ルート直下)';
  $('rename-ext').textContent = ext;
  renameDlg.input.value = base;
  if (!renameDlg.dlg.open) renameDlg.dlg.showModal();
  renameDlg.input.focus();
  renameDlg.input.select();
}

async function submitRename(ev) {
  ev.preventDefault();
  const f = renameDlg.file;
  if (!f || renameDlg.busy) return;
  const name = renameDlg.input.value;
  renameDlg.busy = true;
  $('rename-ok').disabled = true;
  try {
    const res = await postJSON('/api/rename', { path: f.path, name });
    renameDlg.dlg.close();
    thumbs.move(f.path, res.path);
    toast(`名前を変更した: ${res.path.split('/').pop()}`);
    await refreshFiles();
    followRename(f.path, res.path); // 直後の SSE (removed/added) は改名として判別され、再読み込みしない
  } catch (e) {
    renameDlg.err.textContent = e.message;
    renameDlg.input.focus();
  } finally {
    renameDlg.busy = false;
    $('rename-ok').disabled = false;
  }
}

$('rename-form').addEventListener('submit', submitRename);
$('rename-cancel').addEventListener('click', () => renameDlg.dlg.close());
$('rename').addEventListener('click', () => { const f = currentFile(); if (f) openRename(f); });

// ---------- 変更監視 (SSE) ----------

function connectEvents() {
  const es = new EventSource('/api/events');
  es.addEventListener('change', async (ev) => {
    let c;
    try { c = JSON.parse(ev.data); } catch (e) { return; }
    if (isLocal()) return; // サーバ側フォルダの変更通知。ブラウザ読み込み中は表示に影響しない
    await refreshFiles();
    const renames = detectRenames(c);
    const renamedTo = new Set(renames.values());
    for (const p of c.modified) thumbs.forget(p);
    for (const p of c.removed) {
      const to = renames.get(p);
      if (to) {
        thumbs.move(p, to);
        followRename(p, to);
      } else {
        thumbs.forget(p);
      }
      lastSeen.delete(p);
    }
    if (!state.selected) return;
    if (renamedTo.has(state.selected)) return; // 改名のみ: 内容は同じなので再読み込みしない
    if (c.removed.includes(state.selected) || c.added.includes(state.selected)) {
      loadModel({ keepCamera: false });
    } else if (c.modified.includes(state.selected)) {
      loadModel({ keepCamera: true });
    }
  });
  es.onopen = () => { if (!isLocal()) refreshFiles(); }; // 再接続時に取りこぼしを補う
}

// ---------- フォルダの読み込み (ブラウザ) ----------

function resetSelection() {
  state.gen++;
  state.loadSeq++;
  state.selected = null;
  state.model = null;
  state.item = 'all';
  thumbs.reset();
  viewer.clear();
  viewer.requestRender();
  $('items').hidden = true;
  $('toolbar').hidden = true;
  setMessage('左の一覧からモデルを選択してください');
  $('status').textContent = '';
}

function updateSourceUI(info) {
  const local = isLocal();
  $('open-folder').textContent = local ? '別のフォルダを開く' : 'フォルダを開く';
  $('back-server').hidden = !local;
  const rp = $('root-path');
  if (local) {
    rp.textContent = `📁 ${state.localRoot} (ブラウザで読み込み)`;
    rp.title = `ブラウザで選んだフォルダ「${state.localRoot}」。絶対パスは不明で、一覧のパスは選んだフォルダからの相対パス`;
  } else {
    rp.textContent = info || rp.dataset.server || '';
    rp.title = rp.textContent;
  }
  if (!local) $('local-note').hidden = true;
}

function openFolderPicked(fileList) {
  const r = buildLocalList(Array.from(fileList));
  if (!r.rootName && r.files.length === 0) {
    toast('フォルダが選ばれなかった、または空だった');
    return;
  }
  resetSelection();
  state.source = 'local';
  state.localRoot = r.rootName || '(選択したフォルダ)';
  state.files = r.files;
  history.replaceState(null, '', location.pathname + location.search);
  updateSourceUI();
  const notes = ['名前の変更・ファイル監視による自動反映は使えない (更新するには、もう一度フォルダを開く)。'];
  if (r.hiddenSkipped) notes.push(`隠しファイル ${r.hiddenSkipped} 件は除外した。`);
  if (r.truncated) notes.push('ファイルが多いため一覧は先頭の一部のみ表示している。');
  if (r.files.length === 0) notes.unshift('.3mf / .stl が見つからなかった。');
  $('local-note').textContent = notes.join(' ');
  $('local-note').hidden = false;
  updateHeader();
  renderList();
}

async function backToServer() {
  resetSelection();
  state.source = 'server';
  state.localRoot = '';
  state.files = [];
  updateSourceUI();
  updateHeader();
  await refreshFiles();
}

$('open-folder').addEventListener('click', () => $('folder-input').click());
$('folder-input').addEventListener('change', (e) => {
  const list = e.target.files;
  if (list && list.length) openFolderPicked(list);
  e.target.value = ''; // 同じフォルダを続けて選んでも change が発火するように
});
$('back-server').addEventListener('click', backToServer);

// ---------- 起動 ----------

function selectedFromHash() {
  try { return decodeURIComponent(location.hash.slice(1)) || null; } catch (e) { return null; }
}

$('filter').addEventListener('input', renderList);
$('copy-name').addEventListener('click', () => { const f = currentFile(); if (f) copyText(f.name, 'ファイル名'); });
$('copy-path').addEventListener('click', () => { const f = currentFile(); if (f) copyText(...pathCopy(f)); });
$('reset-view').addEventListener('click', () => viewer.fit());
$('opt-grid').addEventListener('change', (e) => viewer.setGrid(e.target.checked));
$('opt-edges').addEventListener('change', (e) => viewer.setWire(e.target.checked));
window.addEventListener('hashchange', () => {
  if (isLocal()) return;
  const p = selectedFromHash();
  if (p && p !== state.selected) select(p);
});

(async () => {
  try {
    const info = await getJSON('/api/info');
    $('root-path').dataset.server = info.root;
    updateSourceUI(info.root);
  } catch (e) { /* 表示のみ */ }
  await refreshFiles();
  const p = selectedFromHash();
  if (p) select(p);
  connectEvents();
})();
