'use strict';

(() => {
  const $ = (selector, parent = document) => parent.querySelector(selector);
  const $$ = (selector, parent = document) => [...parent.querySelectorAll(selector)];
  const token = $('meta[name="pageport-token"]').content;
  let state = null;
  let online = false;
  let refreshing = false;
  let selectedIP = '';
  let filter = 'all';
  let tableSignature = '';
  let pendingDelete = null;
  let uploading = false;
  const uploadQueue = [];
  let uploadResults = [];
  try { selectedIP = sessionStorage.getItem('pageport-ip') || ''; } catch (_) { /* Optional preference. */ }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function icon(name) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'icon');
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', '#i-' + name);
    svg.append(use);
    return svg;
  }

  function toast(message, error = false) {
    const node = element('div', 'toast' + (error ? ' error' : ''));
    node.append(icon(error ? 'help' : 'check'), element('span', 'toast-message', message));
    const close = element('button', '', '×');
    close.type = 'button';
    close.setAttribute('aria-label', 'Dismiss notification');
    close.addEventListener('click', () => node.remove());
    node.append(close);
    $('#toast-region').append(node);
    setTimeout(() => node.remove(), error ? 12000 : 5500);
  }

  function formatBytes(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 ** 2) return (bytes / 1024).toFixed(1) + ' KB';
    if (bytes < 1024 ** 3) return (bytes / 1024 ** 2).toFixed(1) + ' MB';
    return (bytes / 1024 ** 3).toFixed(2) + ' GB';
  }

  function shortDate(value) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? '' : date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  }

  async function api(path, options = {}) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 10000);
    try {
      const response = await fetch(path, {
        ...options,
        headers: { 'X-Pageport-Token': token, ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...options.headers },
        cache: 'no-store', signal: controller.signal
      });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'The request could not be completed.');
      return data;
    } catch (error) {
      if (error.name === 'AbortError' || error instanceof TypeError) throw new Error('Pageport isn’t responding. Make sure the app is still running.');
      throw error;
    } finally { clearTimeout(timeout); }
  }

  async function refresh() {
    if (refreshing) return;
    refreshing = true;
    try {
      state = await api('/api/state');
      online = true;
      $('#connection-error').hidden = true;
      renderState();
    } catch (_) {
      online = false;
      $('#connection-error').hidden = false;
      $('#server-status').classList.add('offline');
      $('#server-status-text').textContent = 'App is offline';
      $$('.reader-status-text').forEach(node => { node.textContent = 'App is offline'; });
      $$('.reader-status').forEach(node => node.classList.remove('seen'));
    } finally {
      refreshing = false;
      updateControls();
    }
  }

  function selectedAddress() {
    if (!state) return null;
    return state.addresses.find(address => address.ip === selectedIP) || state.addresses[0] || null;
  }

  function addressOrigin() {
    const address = selectedAddress();
    if (!address) return '';
    try { return new URL(address.url).origin; } catch (_) { return ''; }
  }

  function renderState() {
    $('#server-status').classList.toggle('offline', !state.sharing);
    $('#server-status-text').textContent = state.sharing ? 'Local server is running' : 'Sharing is paused';
    $('#paused-banner').hidden = state.sharing;
    $('#version').textContent = state.version === 'dev' ? 'LOCAL EDITION' : 'v' + state.version;
    $('#nav-count').textContent = state.files.length;
    $('#library-count').textContent = state.files.length;
    const totalBytes = state.files.reduce((sum, file) => sum + file.size, 0);
    $('#library-size').textContent = state.files.length ? formatBytes(totalBytes) + ' on this computer' : 'Ready for a good read';

    const networkSelect = $('#network-select');
    const networkSignature = JSON.stringify(state.addresses.map(address => address.ip));
    if (networkSelect.dataset.signature !== networkSignature) {
      networkSelect.replaceChildren();
      for (const address of state.addresses) networkSelect.append(new Option(address.ip + ':' + state.port, address.ip));
      if (!state.addresses.length) networkSelect.append(new Option('No network address found', ''));
      networkSelect.dataset.signature = networkSignature;
    }
    const address = selectedAddress();
    if (address) { selectedIP = address.ip; networkSelect.value = selectedIP; }
    networkSelect.disabled = state.addresses.length < 2;
    $$('.reader-address').forEach(node => { node.textContent = addressOrigin() || 'Connect to your Wi-Fi'; });
    $('#network-help').textContent = state.addresses.length ? 'If you have several addresses, choose the one on your Wi-Fi network.' : 'No Wi-Fi address is available. Connect to a network and start Pageport with its default network settings.';
    $$('.session-code').forEach(node => { node.textContent = state.sessionCode; node.setAttribute('aria-label', 'Session code ' + state.sessionCode.split('').join(' ')); });
    $$('.reader-preview').forEach(node => { node.href = '/read/' + encodeURIComponent(state.sessionCode); });
    $$('.sharing-toggle').forEach(button => {
      $('span', button).textContent = state.sharing ? 'Pause sharing' : 'Resume sharing';
      $('use', button).setAttribute('href', state.sharing ? '#i-pause' : '#i-wifi');
    });
    let readerStatus = 'Waiting for a reader';
    if (!state.sharing) readerStatus = 'Sharing is paused';
    else if (state.lastReaderSeen) readerStatus = 'Reader page visited';
    $$('.reader-status-text').forEach(node => { node.textContent = readerStatus; });
    $$('.reader-status').forEach(node => {
      node.classList.toggle('seen', state.sharing && !!state.lastReaderSeen);
      node.title = state.lastReaderSeen ? 'Last reader page visit: ' + new Date(state.lastReaderSeen).toLocaleString() : 'Open this address on your Kobo to get started.';
    });
    renderBooks();
  }

  function updateControls() {
    $$('.add-files, .sharing-toggle, #reset-code').forEach(button => { button.disabled = !online; });
    $$('.copy-address').forEach(button => { button.disabled = !online || !addressOrigin(); });
  }

  function bookCategory(file) {
    if (['EPUB', 'KEPUB'].includes(file.format.toUpperCase())) return 'epub';
    return file.format.toUpperCase() === 'PDF' ? 'pdf' : 'other';
  }

  function downloadURL(file) {
    return '/download/' + encodeURIComponent(state.sessionCode) + '/' + encodeURIComponent(file.id) + '/' + encodeURIComponent(file.name);
  }

  function renderBooks() {
    if (!state) return;
    const query = $('#search').value.trim().toLocaleLowerCase();
    const sort = $('#sort').value;
    const signature = JSON.stringify([state.files, state.sharing, state.sessionCode, filter, query, sort]);
    if (signature === tableSignature) return;
    tableSignature = signature;
    let books = state.files.filter(file => (filter === 'all' || bookCategory(file) === filter) && file.name.toLocaleLowerCase().includes(query));
    books = [...books].sort((a, b) => sort === 'name' ? a.name.localeCompare(b.name) : sort === 'size' ? b.size - a.size : new Date(b.addedAt) - new Date(a.addedAt));
    $('#empty-library').hidden = state.files.length !== 0;
    $('#no-results').hidden = state.files.length === 0 || books.length !== 0;
    $('#book-table-wrap').hidden = books.length === 0;
    const list = $('#book-list');
    list.replaceChildren();
    for (const file of books) {
      const category = bookCategory(file);
      const row = element('tr');
      const nameCell = element('td');
      const nameWrap = element('div', 'book-row-name');
      const art = element('span', 'file-art ' + category);
      art.append(icon(category === 'epub' ? 'book' : 'file'));
      const info = element('div', 'file-text');
      const name = element('div', 'book-name', file.name);
      name.title = file.name;
      info.append(name, element('div', 'book-date', 'Added ' + shortDate(file.addedAt)));
      nameWrap.append(art, info);
      nameCell.append(nameWrap);
      const formatCell = element('td');
      formatCell.append(element('span', 'format-badge ' + category, file.format));
      const sizeCell = element('td', 'file-size', formatBytes(file.size));
      const activityCell = element('td');
      const activity = element('div', 'file-activity');
      if (file.downloads > 0) {
        activity.append(document.createTextNode('Served ' + file.downloads + (file.downloads === 1 ? ' time' : ' times')));
        activity.title = 'Full file downloads served. This does not confirm import on a Kobo.';
        activity.append(element('small', '', 'Last download ' + shortDate(file.lastDownloadedAt)));
      } else {
        activity.append(element('span', 'status-dot'), document.createTextNode(state.sharing ? 'Ready to download' : 'Sharing paused'));
      }
      activityCell.append(activity);
      const actionCell = element('td');
      const actions = element('div', 'file-actions');
      if (state.sharing) {
        const download = element('a', 'icon-button download-book');
        download.href = downloadURL(file);
        download.title = 'Download a copy to this computer';
        download.setAttribute('aria-label', 'Download ' + file.name + ' to this computer');
        download.append(icon('download'));
        actions.append(download);
      }
      const remove = element('button', 'icon-button delete-book');
      remove.type = 'button';
      remove.title = 'Remove from Pageport';
      remove.setAttribute('aria-label', 'Remove ' + file.name);
      remove.append(icon('trash'));
      remove.addEventListener('click', () => askDelete(file));
      actions.append(remove);
      actionCell.append(actions);
      row.append(nameCell, formatCell, sizeCell, activityCell, actionCell);
      list.append(row);
    }
  }

  function setView() {
    const requested = location.hash.slice(1);
    const view = ['library', 'connect', 'guide'].includes(requested) ? requested : 'library';
    $$('.view').forEach(node => { node.hidden = node.id !== 'view-' + view; });
    $$('.nav-link').forEach(link => {
      link.classList.toggle('active', link.dataset.view === view);
      if (link.dataset.view === view) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    $('#breadcrumb-current').textContent = { library: 'Library', connect: 'Connect your Kobo', guide: 'How it works' }[view];
    document.title = 'Pageport — ' + { library: 'Your wireless bookshelf', connect: 'Connect your Kobo', guide: 'How it works' }[view];
  }

  async function copyAddress(button) {
    const address = addressOrigin();
    if (!address) return;
    try {
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(address);
      else {
        const textarea = element('textarea', 'sr-only', address);
        document.body.append(textarea);
        textarea.select();
        const copied = document.execCommand('copy');
        textarea.remove();
        button.focus();
        if (!copied) throw new Error('Copy unavailable');
      }
      toast('Address copied. Enter the session code when prompted.');
    } catch (_) { toast('Copy isn’t available. Type the displayed address on your Kobo.', true); }
  }

  async function mutation(button, path, body, message) {
    button.disabled = true;
    try {
      await api(path, { method: 'POST', ...(body ? { body: JSON.stringify(body) } : {}) });
      await refresh();
      toast(message);
    } catch (error) { toast(error.message, true); }
    finally { updateControls(); }
  }

  function askDelete(file) {
    pendingDelete = file;
    $('#delete-dialog').returnValue = '';
    $('#delete-description').textContent = file.name + ' will be removed from your shared library.';
    $('#delete-dialog').showModal();
  }

  $('#delete-dialog').addEventListener('close', async () => {
    const file = pendingDelete;
    pendingDelete = null;
    if ($('#delete-dialog').returnValue !== 'confirm' || !file) return;
    try {
      await api('/api/files/' + encodeURIComponent(file.id), { method: 'DELETE' });
      await refresh();
      toast('Book removed from Pageport.');
    } catch (error) { toast(error.message, true); }
  });

  function validateFile(file) {
    const ext = '.' + file.name.split('.').pop().toLowerCase();
    if (ext === '.acsm') return 'An ACSM file is a DRM license. Use Adobe Digital Editions for this book.';
    if (!state.supportedExtensions.includes(ext)) return 'This format isn’t supported. Choose an EPUB, PDF, comic, text, or image file.';
    if (file.size === 0) return 'This file is empty.';
    if (file.size > state.maxFileBytes) return 'This file is larger than the 512 MiB limit.';
    return '';
  }

  function renderUploadResult(result) {
    result.statusNode.textContent = result.status;
    result.statusNode.classList.toggle('failed', result.failed);
    result.progressNode.hidden = result.done;
    result.progressNode.value = result.progress;
  }

  function enqueueFiles(files) {
    if (!online || !state) { toast('Start Pageport before adding books.', true); return; }
    if (!files.length) return;
    if (!uploading) { uploadResults = []; $('#upload-list').replaceChildren(); }
    location.hash = 'library';
    $('#upload-panel').hidden = false;
    $('#dismiss-uploads').hidden = true;
    for (const file of files) {
      const error = validateFile(file);
      const result = { file, status: error || 'Waiting…', failed: !!error, done: !!error, progress: 0 };
      const row = element('div', 'upload-item');
      const name = element('span', 'upload-item-name', file.name);
      name.title = file.name;
      result.statusNode = element('span', 'upload-item-status');
      result.progressNode = element('progress', 'upload-progress');
      result.progressNode.max = 100;
      result.progressNode.setAttribute('aria-label', 'Upload progress for ' + file.name);
      row.append(name, result.statusNode, result.progressNode);
      $('#upload-list').append(row);
      uploadResults.push(result);
      renderUploadResult(result);
      if (!error) uploadQueue.push(result);
    }
    if (!uploading) void processUploads();
  }

  function uploadFile(result) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/files');
      xhr.setRequestHeader('X-Pageport-Token', token);
      xhr.responseType = 'json';
      xhr.timeout = 30 * 60 * 1000;
      xhr.upload.addEventListener('progress', event => {
        if (event.lengthComputable) {
          result.progress = Math.round(event.loaded / event.total * 100);
          result.status = result.progress === 100 ? 'Saving to your library…' : 'Adding… ' + result.progress + '%';
          renderUploadResult(result);
        }
      });
      xhr.addEventListener('load', () => {
        if (xhr.status >= 200 && xhr.status < 300) resolve(xhr.response);
        else reject(new Error(xhr.response?.error || 'Upload failed. Please try again.'));
      });
      xhr.addEventListener('error', () => reject(new Error('Connection lost. Keep Pageport open and try again.')));
      xhr.addEventListener('timeout', () => reject(new Error('Upload timed out. Please try again.')));
      xhr.addEventListener('abort', () => reject(new Error('Upload cancelled.')));
      const body = new FormData();
      body.append('files', result.file);
      xhr.send(body);
    });
  }

  async function processUploads() {
    uploading = true;
    $('#upload-title').textContent = 'Adding books to your library…';
    while (uploadQueue.length) {
      const result = uploadQueue.shift();
      result.status = 'Adding…';
      renderUploadResult(result);
      try {
        await uploadFile(result);
        result.status = 'Added to your library';
        result.progress = 100;
      } catch (error) {
        result.failed = true;
        result.status = error.message;
      }
      result.done = true;
      renderUploadResult(result);
      await refresh();
    }
    uploading = false;
    const failed = uploadResults.filter(result => result.failed).length;
    const added = uploadResults.length - failed;
    $('#upload-title').textContent = added + (added === 1 ? ' book added' : ' books added') + (failed ? ' · ' + failed + ' couldn’t be added' : ' — ready for your next chapter');
    $('#dismiss-uploads').hidden = false;
    if (added) toast(added + (added === 1 ? ' book is' : ' books are') + ' ready. Open your Kobo’s reader page to download.');
    // Release File objects once their uploads have finished.
    uploadResults.forEach(result => { result.file = null; });
  }

  $$('.add-files').forEach(button => button.addEventListener('click', () => $('#file-input').click()));
  $('#file-input').addEventListener('change', event => { enqueueFiles([...event.target.files]); event.target.value = ''; });
  let dragDepth = 0;
  const dropzone = $('#dropzone');
  document.addEventListener('dragover', event => { if (event.dataTransfer.types.includes('Files')) event.preventDefault(); });
  document.addEventListener('drop', event => { if (event.dataTransfer.types.includes('Files')) event.preventDefault(); });
  dropzone.addEventListener('dragenter', event => { event.preventDefault(); dragDepth++; dropzone.classList.add('dragover'); });
  dropzone.addEventListener('dragleave', () => { dragDepth = Math.max(0, dragDepth - 1); if (!dragDepth) dropzone.classList.remove('dragover'); });
  dropzone.addEventListener('dragover', event => { event.preventDefault(); event.dataTransfer.dropEffect = 'copy'; });
  dropzone.addEventListener('drop', event => {
    event.preventDefault(); dragDepth = 0; dropzone.classList.remove('dragover');
    enqueueFiles([...event.dataTransfer.files]);
  });
  $('#dismiss-uploads').addEventListener('click', () => { $('#upload-panel').hidden = true; });
  $$('.copy-address').forEach(button => button.addEventListener('click', () => void copyAddress(button)));
  $('#network-select').addEventListener('change', event => {
    selectedIP = event.target.value;
    try { sessionStorage.setItem('pageport-ip', selectedIP); } catch (_) { /* Optional preference. */ }
    renderState(); updateControls();
  });
  $$('.sharing-toggle').forEach(button => button.addEventListener('click', () => {
    if (!state) return;
    void mutation(button, '/api/sharing', { enabled: !state.sharing }, state.sharing ? 'Sharing paused. New downloads are stopped.' : 'Sharing resumed. Your books are ready.');
  }));
  $('#reset-code').addEventListener('click', event => void mutation(event.currentTarget, '/api/session/reset', null, 'New session code created. Enter it again on your Kobo.'));
  $$('.filter').forEach(button => button.addEventListener('click', () => {
    filter = button.dataset.filter;
    $$('.filter').forEach(node => { node.classList.toggle('active', node === button); node.setAttribute('aria-pressed', String(node === button)); });
    renderBooks();
  }));
  $('#search').addEventListener('input', renderBooks);
  $('#sort').addEventListener('change', renderBooks);
  $('#clear-filters').addEventListener('click', () => {
    $('#search').value = '';
    $('[data-filter="all"]').click();
  });
  $('#retry-connection').addEventListener('click', () => void refresh());
  window.addEventListener('hashchange', setView);
  window.addEventListener('focus', () => void refresh());
  document.addEventListener('visibilitychange', () => { if (!document.hidden) void refresh(); });
  window.addEventListener('beforeunload', event => { if (uploading) { event.preventDefault(); event.returnValue = ''; } });
  setView();
  updateControls();
  void refresh();
  setInterval(() => { if (!document.hidden && !uploading) void refresh(); }, 4000);
})();
