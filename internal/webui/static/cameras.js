// Cameras (docs/cameras.md) — live view + archive. First feature pulled
// out of index.html's single inline script into its own module, as a
// template for doing the same to the rest over time: self-contained
// (its own DOM queries, its own state), importing only the one thing it
// genuinely shares with everything else — the i18n dictionary.
import { text } from './shared.js';

const camerasToggle = document.querySelector('#cameras-toggle');
const camerasDialog = document.querySelector('#cameras-dialog');
const language = document.querySelector('#language');

// The 📹 button is only shown once GET /api/cameras/list reports at
// least one camera (see loadCamerasAvailability), the same "hide the
// button, don't error" idiom the 📊/🔗 buttons already use.
let camerasKnown = [];
let activeCamera = null;
// Refreshed while the dialog is open (see the toggle/close listeners
// below) so a camera going offline — or coming back — shows up without
// needing to close and reopen the dialog. A dead camera otherwise left
// its live view frozen with no explanation at all (docs/cameras.md).
let camerasStatusTimer = null;

async function loadCamerasAvailability() {
  try {
    const response = await fetch('/api/cameras/list');
    const data = await response.json();
    camerasKnown = Array.isArray(data.cameras) ? data.cameras : [];
  } catch {
    camerasKnown = [];
  }
  camerasToggle.hidden = camerasKnown.length === 0;
}

function renderCameraPicker() {
  const picker = document.querySelector('#cameras-picker');
  picker.innerHTML = '';
  if (camerasKnown.length < 2) return; // nothing to pick between
  camerasKnown.forEach(camera => {
    const button = document.createElement('button');
    button.type = 'button';
    const dot = document.createElement('span');
    dot.className = camera.connected ? 'camera-picker-dot online' : 'camera-picker-dot';
    button.appendChild(dot);
    button.appendChild(document.createTextNode(camera[language.value === 'en' ? 'label_en' : 'label_ru'] || camera.name));
    button.className += camera.name === activeCamera ? ' active' : '';
    button.addEventListener('click', () => selectCamera(camera.name));
    picker.appendChild(button);
  });
}

function updateCameraStatus() {
  const camera = camerasKnown.find(c => c.name === activeCamera);
  const dot = document.querySelector('#cameras-status-dot');
  const label = document.querySelector('#cameras-status-text');
  const online = !!(camera && camera.connected);
  dot.className = online ? 'camera-status-dot online' : 'camera-status-dot';
  label.textContent = online ? text[language.value].camerasOnline : text[language.value].camerasOffline;
}

// Tracks what refreshLiveView last actually set the <img> to (null right
// after selecting a new camera, so the very next call always decides
// fresh) — an <img> keeps showing its last successfully-loaded bitmap
// while a new src is still pending, so switching to an offline camera
// without this would silently leave the *previous* camera's frame on
// screen looking like it's still live. Re-evaluated on every periodic
// refresh tick too, so a camera coming back online while the dialog is
// still open picks the live view back up without needing to reselect it.
let liveViewConnected = null;

function refreshLiveView() {
  const live = document.querySelector('#cameras-live');
  const camera = camerasKnown.find(c => c.name === activeCamera);
  const online = !!(camera && camera.connected);
  if (online === liveViewConnected) return;
  liveViewConnected = online;
  if (online) {
    // A fresh src (rather than reusing the same URL) makes the browser
    // actually open a new connection to the relay endpoint instead of
    // assuming nothing changed.
    live.src = `/api/cameras/${encodeURIComponent(activeCamera)}/stream?_=${Date.now()}`;
  } else {
    live.removeAttribute('src');
  }
}

function selectCamera(name) {
  activeCamera = name;
  liveViewConnected = null;
  renderCameraPicker();
  updateCameraStatus();
  refreshLiveView();
  const player = document.querySelector('#cameras-archive-player');
  player.hidden = true;
  player.removeAttribute('src');
  loadCameraArchive(name);
}

async function loadCameraArchive(name) {
  const list = document.querySelector('#cameras-archive-list');
  const empty = document.querySelector('#cameras-archive-empty');
  list.innerHTML = '';
  try {
    const response = await fetch(`/api/cameras/${encodeURIComponent(name)}/archive`);
    const data = await response.json();
    const segments = Array.isArray(data.segments) ? data.segments : [];
    empty.hidden = segments.length > 0;
    segments.forEach(segment => {
      const li = document.createElement('li');
      const sizeMB = (segment.size_bytes / 1e6).toFixed(1);
      const when = new Date(segment.last_modified).toLocaleString(language.value === 'ru' ? 'ru-RU' : 'en-US');
      li.textContent = `${segment.name} — ${sizeMB} MB — ${when}`;
      li.addEventListener('click', () => {
        const player = document.querySelector('#cameras-archive-player');
        player.src = `/api/cameras/${encodeURIComponent(name)}/archive/${encodeURIComponent(segment.name)}`;
        player.hidden = false;
        player.play().catch(() => {});
      });
      list.appendChild(li);
    });
  } catch {
    // Best-effort: connectivity issues already surface via the status pill.
  }
}

camerasToggle.addEventListener('click', () => {
  if (!activeCamera && camerasKnown.length > 0) activeCamera = camerasKnown[0].name;
  renderCameraPicker();
  updateCameraStatus();
  if (activeCamera) selectCamera(activeCamera);
  camerasDialog.showModal();
  if (camerasStatusTimer) clearInterval(camerasStatusTimer);
  camerasStatusTimer = setInterval(async () => {
    await loadCamerasAvailability();
    renderCameraPicker();
    updateCameraStatus();
    refreshLiveView();
  }, 5000);
});
document.querySelector('#cameras-close').addEventListener('click', () => camerasDialog.close());
camerasDialog.addEventListener('close', () => {
  if (camerasStatusTimer) { clearInterval(camerasStatusTimer); camerasStatusTimer = null; }
  // Stop consuming the relay's single-buffer subscriber slot the
  // moment nobody's actually looking — clearing src closes the
  // browser's connection to /stream.
  document.querySelector('#cameras-live').removeAttribute('src');
  const player = document.querySelector('#cameras-archive-player');
  player.pause();
  player.removeAttribute('src');
});

// Called from the main script's updateLanguage() — see index.html.
export function updateCamerasLanguage(locale) {
  camerasToggle.title = text[locale].camerasToggleTitle;
  camerasToggle.setAttribute('aria-label', text[locale].camerasToggleTitle);
  document.querySelector('#cameras-title').textContent = text[locale].camerasTitle;
  document.querySelector('#cameras-archive-label').textContent = text[locale].camerasArchiveLabel;
  document.querySelector('#cameras-archive-empty').textContent = text[locale].camerasArchiveEmpty;
  document.querySelector('#cameras-close').textContent = text[locale].camerasClose;
  renderCameraPicker();
  updateCameraStatus();
}

// Called once from the main script's bootstrap sequence — see index.html.
export function initCameras() {
  loadCamerasAvailability();
}
