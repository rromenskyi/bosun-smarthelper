// File dump (docs/filedump.md) — a raw file tree with real folders,
// drag-and-drop upload/reorganize, and an optional per-upload checkbox
// to also feed a file into the bosun's RAG search index. Follows
// cameras.js's template: self-contained, own DOM queries, importing only
// the i18n dictionary.
import { text } from './shared.js';

const filedumpToggle = document.querySelector('#filedump-toggle');
const filedumpDialog = document.querySelector('#filedump-dialog');
const filedumpList = document.querySelector('#filedump-list');
const filedumpBreadcrumb = document.querySelector('#filedump-breadcrumb');
const filedumpDropzone = document.querySelector('#filedump-dropzone');
const filedumpFileInput = document.querySelector('#filedump-file-input');
const filedumpChooseFiles = document.querySelector('#filedump-choose-files');
const filedumpAddToRAG = document.querySelector('#filedump-add-to-rag');
const filedumpRagOptions = document.querySelector('#filedump-rag-options');
const filedumpDocTitle = document.querySelector('#filedump-doc-title');
const filedumpOCRLanguage = document.querySelector('#filedump-ocr-language');
const filedumpNewFolder = document.querySelector('#filedump-new-folder');
const language = document.querySelector('#language');

// A file above this size gets a confirm() warning before upload — there's
// deliberately no server-side hard cap to lean on instead (docs/filedump.md).
const SIZE_WARNING_BYTES = 200 * 1024 * 1024;

// Current folder, tree-relative ("" is the root) — same forward-slash,
// no-leading-slash form the server uses (internal/filedump.relPath).
let currentPath = '';

// Set on a row's dragstart, read by a folder/breadcrumb's drop handler —
// simpler and more robust than round-tripping the path through
// DataTransfer, since Firefox/Safari restrict reading custom MIME types
// during dragover (only on drop), which would break live drop-target
// highlighting.
let draggingPath = null;

function locale() {
  return language.value === 'en' ? 'en' : 'ru';
}

function joinPath(...parts) {
  return parts.filter(p => p !== '' && p != null).join('/');
}

// pendingPollTimer re-fetches the listing every few seconds while any
// visible file has rag_pending — ingestion now runs in a background
// goroutine (docs/filedump.md), so there's no request left to await the
// result on; polling the listing is how the badge (⏳ → ✓/⚠️) actually
// updates once it finishes. Always reads the *current* currentPath at
// fire time, so navigating away mid-poll just re-checks wherever the
// user ended up instead of a stale folder.
let pendingPollTimer = null;

async function loadListing() {
  let data;
  try {
    const response = await fetch(`/api/files?path=${encodeURIComponent(currentPath)}`);
    data = await response.json();
  } catch {
    return;
  }
  if (!data.enabled) {
    filedumpToggle.hidden = true;
    return;
  }
  filedumpToggle.hidden = false;
  renderBreadcrumb();
  renderList(data.folders || [], data.files || []);

  if (pendingPollTimer) {
    clearTimeout(pendingPollTimer);
    pendingPollTimer = null;
  }
  if ((data.files || []).some(file => file.rag_pending)) {
    pendingPollTimer = setTimeout(loadListing, 3000);
  }
}

function renderBreadcrumb() {
  filedumpBreadcrumb.innerHTML = '';
  const segments = currentPath ? currentPath.split('/') : [];
  const rootButton = document.createElement('button');
  rootButton.type = 'button';
  rootButton.textContent = text[locale()].fileDumpRoot;
  rootButton.addEventListener('click', () => { currentPath = ''; loadListing(); });
  addDropTarget(rootButton, '');
  filedumpBreadcrumb.appendChild(rootButton);

  let built = '';
  segments.forEach((segment, index) => {
    built = joinPath(built, segment);
    const sep = document.createElement('span');
    sep.textContent = ' / ';
    filedumpBreadcrumb.appendChild(sep);
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = segment;
    const targetPath = built;
    if (index < segments.length - 1) {
      button.addEventListener('click', () => { currentPath = targetPath; loadListing(); });
    }
    addDropTarget(button, targetPath);
    filedumpBreadcrumb.appendChild(button);
  });
}

function renderList(folders, files) {
  filedumpList.innerHTML = '';
  if (!folders.length && !files.length) {
    const empty = document.createElement('li');
    empty.textContent = text[locale()].fileDumpEmpty;
    filedumpList.appendChild(empty);
    return;
  }

  folders.forEach(folder => {
    const path = joinPath(currentPath, folder.name);
    const item = document.createElement('li');
    item.className = 'filedump-row filedump-folder';
    item.draggable = true;

    const name = document.createElement('span');
    name.className = 'filedump-row-name';
    name.textContent = `📁 ${folder.name}`;
    item.appendChild(name);

    const actions = document.createElement('div');
    actions.className = 'filedump-row-actions';
    const deleteButton = document.createElement('button');
    deleteButton.type = 'button';
    deleteButton.textContent = '✕';
    deleteButton.title = text[locale()].fileDumpDeleteTitle;
    deleteButton.addEventListener('click', async event => {
      event.stopPropagation();
      await deleteFolder(path, folder.name);
    });
    actions.appendChild(deleteButton);
    item.appendChild(actions);

    item.addEventListener('click', () => { currentPath = path; loadListing(); });
    addDragSource(item, path);
    addDropTarget(item, path);
    filedumpList.appendChild(item);
  });

  files.forEach(file => {
    const path = joinPath(currentPath, file.name);
    const item = document.createElement('li');
    item.className = 'filedump-row';
    item.draggable = true;

    const name = document.createElement('span');
    name.className = 'filedump-row-name';
    // At most one of these is ever true at a time (see
    // internal/filedump.FileInfo) — pending while a background
    // ingestion job runs, then either in_rag (done) or rag_error
    // (failed) once it finishes.
    if (file.rag_pending) {
      const badge = document.createElement('span');
      badge.className = 'filedump-rag-badge filedump-rag-pending';
      badge.title = text[locale()].fileDumpRagPendingTitle;
      name.appendChild(badge);
    } else if (file.rag_error) {
      const badge = document.createElement('span');
      badge.className = 'filedump-rag-badge filedump-rag-error';
      badge.title = text[locale()].fileDumpRagWarningPrefix + file.rag_error;
      name.appendChild(badge);
    } else if (file.in_rag) {
      const badge = document.createElement('span');
      badge.className = 'filedump-rag-badge';
      badge.title = text[locale()].fileDumpRagBadgeTitle;
      name.appendChild(badge);
    }
    const link = document.createElement('a');
    link.href = `/files/${path.split('/').map(encodeURIComponent).join('/')}`;
    link.target = '_blank';
    link.rel = 'noopener';
    link.textContent = file.name;
    name.appendChild(link);
    item.appendChild(name);

    const actions = document.createElement('div');
    actions.className = 'filedump-row-actions';
    const deleteButton = document.createElement('button');
    deleteButton.type = 'button';
    deleteButton.textContent = '✕';
    deleteButton.title = text[locale()].fileDumpDeleteTitle;
    deleteButton.addEventListener('click', async () => {
      if (!confirm(text[locale()].fileDumpDeleteFileConfirm(file.name))) return;
      deleteButton.disabled = true;
      await fetch(`/api/files?path=${encodeURIComponent(path)}`, { method: 'DELETE' }).catch(() => {});
      loadListing();
    });
    actions.appendChild(deleteButton);
    item.appendChild(actions);

    addDragSource(item, path);
    filedumpList.appendChild(item);
  });
}

// countRecursive walks every subfolder via GET /api/files so the delete
// confirm() can name an accurate file count — there's no dedicated
// server-side "preview" endpoint, so this just does the same listing
// calls the browser itself would do to display each level.
async function countRecursive(path) {
  let count = 0;
  try {
    const response = await fetch(`/api/files?path=${encodeURIComponent(path)}`);
    const data = await response.json();
    count += (data.files || []).length;
    for (const folder of data.folders || []) {
      count += await countRecursive(joinPath(path, folder.name));
    }
  } catch {
    // Best-effort — an unreachable subfolder just doesn't add to the count.
  }
  return count;
}

async function deleteFolder(path, name) {
  const fileCount = await countRecursive(path);
  if (!confirm(text[locale()].fileDumpDeleteFolderConfirm(name, fileCount))) return;
  await fetch(`/api/files?path=${encodeURIComponent(path)}&recursive=true`, { method: 'DELETE' }).catch(() => {});
  loadListing();
}

function addDragSource(element, path) {
  element.addEventListener('dragstart', event => {
    draggingPath = path;
    element.classList.add('filedump-dragging');
    event.dataTransfer.effectAllowed = 'move';
    event.dataTransfer.setData('text/plain', path); // Safari requires some data to be set to allow the drag
  });
  element.addEventListener('dragend', () => {
    draggingPath = null;
    element.classList.remove('filedump-dragging');
  });
}

// addDropTarget makes element (a folder row or breadcrumb segment) accept
// an existing item dragged onto it (a move) — OS file drops for upload
// are handled separately, on the dropzone/list container as a whole.
function addDropTarget(element, targetFolderPath) {
  element.addEventListener('dragover', event => {
    if (draggingPath == null) return; // an OS file drag, not a row move — let it bubble to the dropzone
    if (draggingPath === targetFolderPath) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'move';
    element.classList.add('filedump-drop-target');
  });
  element.addEventListener('dragleave', () => element.classList.remove('filedump-drop-target'));
  element.addEventListener('drop', async event => {
    element.classList.remove('filedump-drop-target');
    if (draggingPath == null || draggingPath === targetFolderPath) return;
    event.preventDefault();
    event.stopPropagation();
    const name = draggingPath.split('/').pop();
    const to = joinPath(targetFolderPath, name);
    if (to === draggingPath) return;
    await fetch('/api/files/move', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ from: draggingPath, to })
    }).catch(() => {});
    draggingPath = null;
    loadListing();
  });
}

async function uploadFiles(fileList) {
  const files = Array.from(fileList);
  if (!files.length) return;
  const addToRAG = filedumpAddToRAG.checked;
  // A custom title only makes sense for a single file at a time — for a
  // multi-file drop/select every file falls back to its own filename
  // (see handleFileDumpUpload's default), so the field isn't repeated.
  const title = files.length === 1 ? filedumpDocTitle.value.trim() : '';
  const ocrLanguage = filedumpOCRLanguage.value;

  for (const file of files) {
    if (file.size > SIZE_WARNING_BYTES) {
      const mb = Math.round(file.size / (1024 * 1024));
      if (!confirm(text[locale()].fileDumpSizeWarning(mb))) continue;
    }
    const formData = new FormData();
    formData.append('path', currentPath);
    if (addToRAG) {
      formData.append('add_to_rag', 'true');
      if (title) formData.append('title', title);
      formData.append('ocr_language', ocrLanguage);
    }
    formData.append('file', file);
    try {
      const response = await fetch('/api/files/upload', { method: 'POST', body: formData });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) {
        alert(body.error || text[locale()].fileDumpUploadFailed);
      } else if (body.rag_warning) {
        alert(text[locale()].fileDumpRagWarningPrefix + body.rag_warning);
      }
    } catch {
      alert(text[locale()].fileDumpUploadFailed);
    }
  }
  filedumpDocTitle.value = '';
  loadListing();
}

filedumpChooseFiles.addEventListener('click', () => filedumpFileInput.click());
filedumpFileInput.addEventListener('change', () => {
  uploadFiles(filedumpFileInput.files);
  filedumpFileInput.value = '';
});
filedumpAddToRAG.addEventListener('change', () => {
  filedumpRagOptions.hidden = !filedumpAddToRAG.checked;
});

// OS file drops land on the dropzone or the list itself — both accept a
// drop, sharing the same handler, since either is a natural place to
// drop files onto "the current folder."
[filedumpDropzone, filedumpList].forEach(target => {
  target.addEventListener('dragover', event => {
    if (draggingPath != null) return; // a row move, not an OS file drag — handled by addDropTarget instead
    if (!event.dataTransfer.types.includes('Files')) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'copy';
    filedumpDropzone.classList.add('dragover');
  });
  target.addEventListener('dragleave', () => filedumpDropzone.classList.remove('dragover'));
  target.addEventListener('drop', event => {
    filedumpDropzone.classList.remove('dragover');
    if (draggingPath != null) return; // a row move — its own drop handler already ran
    if (!event.dataTransfer.files || !event.dataTransfer.files.length) return;
    event.preventDefault();
    uploadFiles(event.dataTransfer.files);
  });
});

filedumpNewFolder.addEventListener('click', async () => {
  const name = prompt(text[locale()].fileDumpNewFolderPrompt);
  if (!name || !name.trim()) return;
  await fetch('/api/files/folder', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path: currentPath, name: name.trim() })
  }).catch(() => {});
  loadListing();
});

filedumpToggle.addEventListener('click', () => { currentPath = ''; loadListing(); filedumpDialog.showModal(); });
document.querySelector('#filedump-close').addEventListener('click', () => filedumpDialog.close());

// Called from the main script's updateLanguage() — see index.html.
export function updateFileDumpLanguage(locale) {
  filedumpToggle.title = text[locale].fileDumpToggleTitle;
  filedumpToggle.setAttribute('aria-label', text[locale].fileDumpToggleTitle);
  document.querySelector('#filedump-title').textContent = text[locale].fileDumpTitle;
  document.querySelector('#filedump-close').textContent = text[locale].fileDumpClose;
  filedumpNewFolder.textContent = text[locale].fileDumpNewFolder;
  document.querySelector('#filedump-drop-hint').textContent = text[locale].fileDumpDropHint;
  filedumpChooseFiles.textContent = text[locale].fileDumpChooseFiles;
  document.querySelector('#filedump-rag-label').textContent = text[locale].fileDumpAddToRAG;
  filedumpDocTitle.placeholder = text[locale].fileDumpTitlePlaceholder;
  renderBreadcrumb();
}

// Called once from the main script's bootstrap sequence — see index.html.
export function initFileDump() {
  loadListing();
}
