/**
 * InstaBlast WA Gateway — Frontend Application
 * Communicates with Go backend via REST API + WebSocket
 */

const $ = id => document.getElementById(id);
const API = '/api';
let ws = null;
let _logCount = 0;
let csvRawData = '';
let csvColumns = [];
let imageUploads = [];
let imageUploadsPersonal = [];
let contactLists = [];
let contactCSVColumns = [];
let contactCSVRows = [];
let contactsView = 'groups';
let unsubscribeEntries = [];
let unsubscribeSettingsState = {
  enabled: false,
  keyword: 'STOP',
  instruction: 'Ketik STOP untuk berhenti menerima pesan dari kami.',
  auto_reply: 'Baik, nomor Anda sudah kami masukkan ke daftar berhenti berlangganan. Kami tidak akan mengirimkan pesan broadcast lagi.',
};
let mediaFiles = [];
let selectedBroadcastMediaIDs = [];
let groupMembersData = [];
let chatHistoryData = [];
let lastValidateResult = [];
let aiStatsTimer = null;
let waAccounts = [];
let activeWAAccountId = '';
let aiSelectedAccountIDs = [];
let aiKnowledgeProducts = [];
let aiAccountProductIDs = {};
let aiKnowledgeImageDraft = null;
let aiSelectedKnowledgeID = '';
let aiKnowledgeMappingAccountID = '';
let currentUser = null;
let editingManagedUser = null;
let metaSignupPopup = null;
let metaSignupState = '';
let metaSignupConfig = null;
let metaSignupCode = '';
let metaSignupSessionInfo = null;
let metaSignupPreparing = null;
let metaSignupCompleting = false;
let metaSignupReadyAt = 0;
let metaSDKReadyPromise = null;
let metaSignupFallbackTimer = null;
let metaSignupSDKReady = false;
let broadcastSchedulesCache = [];
let broadcastHistoryCache = [];
let broadcastMode = 'list';
let broadcastType = 'unofficial';
let latestBroadcastProgress = null;
let historyQueueSchedulesCache = [];
let broadcastAIImprovedDraft = '';
let broadcastSpintaxOriginalMessage = '';

// ===== Toast =====
function showToast(msg, type) {
  const container = $('toastContainer');
  const toast = document.createElement('div');
  toast.className = 'toast' + (type ? ' ' + type : '');
  toast.textContent = msg;
  container.appendChild(toast);
  setTimeout(() => { toast.style.opacity = '0'; setTimeout(() => toast.remove(), 300); }, 3000);
}

// ===== Fetch Helper =====
async function api(path, options = {}) {
  try {
    const res = await fetch(API + path, {
      headers: { 'Content-Type': 'application/json', ...options.headers },
      ...options
    });
    const data = await res.json();
    if (res.status === 401) {
      location.href = '/login';
      throw new Error('Unauthorized');
    }
    if (!res.ok) throw new Error(data.error || 'API Error');
    return data;
  } catch (e) {
    appendLog('API Error: ' + e.message, 'error');
    throw e;
  }
  updateBroadcastQuickStats();
}

function appendAccountQuery(path, accountID = activeWAAccountId) {
  if (!accountID) return path;
  return path + (path.includes('?') ? '&' : '?') + 'account_id=' + encodeURIComponent(accountID);
}

function withAccountBody(body = {}, accountID = activeWAAccountId) {
  return { ...body, account_id: accountID || '' };
}

function getActiveAccount() {
  return waAccounts.find(acc => acc.id === activeWAAccountId) || waAccounts[0] || null;
}

function jsString(str) {
  return String(str || '').replace(/\\/g, '\\\\').replace(/'/g, "\\'").replace(/\r/g, '').replace(/\n/g, '\\n');
}

function normalizeAIMessageFormatting(text) {
  const value = String(text || '').trim();
  if (!value) return '';
  return value
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n')
    .replace(/\\r\\n/g, '\n')
    .replace(/\\n/g, '\n')
    .replace(/\\r/g, '\n')
    .trim();
}

function findAccountCard(accountID) {
  return Array.from(document.querySelectorAll('.account-card[data-account-id]'))
    .find(card => card.dataset.accountId === accountID) || null;
}

// ===== Log System =====
function appendLog(text, type) {
  const el = $('logArea');
  if (!el) return;
  const div = document.createElement('div');
  div.className = 'log-line' + (type ? ' ' + type : '');
  div.textContent = `[${new Date().toLocaleTimeString()}] ${text}`;
  el.appendChild(div);
  el.scrollTop = el.scrollHeight;
  while (el.children.length > 300) el.removeChild(el.firstChild);
  _logCount++;
  const badge = $('logCount');
  if (badge) badge.textContent = _logCount > 99 ? '99+' : _logCount;
}

function toggleLogPanel() {
  const overlay = $('logOverlay');
  if (!overlay) return;
  overlay.classList.toggle('open');
  if (overlay.classList.contains('open')) {
    const el = $('logArea');
    if (el) el.scrollTop = el.scrollHeight;
  }
  if ($('tab-history')?.classList.contains('active')) {
    renderHistoryQueue();
  }
}

function clearLog() {
  const el = $('logArea');
  if (el) el.innerHTML = '';
  _logCount = 0;
  const badge = $('logCount');
  if (badge) badge.textContent = '0';
}

function isMobileViewport() {
  return window.innerWidth <= 980;
}

function closeSidebar() {
  document.body.classList.remove('sidebar-open');
}

function openSidebar() {
  if (!isMobileViewport()) return;
  document.body.classList.add('sidebar-open');
}

function toggleSidebar() {
  if (!isMobileViewport()) return;
  document.body.classList.toggle('sidebar-open');
}

function normalizeLegacyTopbar() {
  document.querySelectorAll('.topbar-center').forEach((el) => el.remove());
  document.querySelectorAll('.account-switcher-icon').forEach((el) => el.remove());
}

function normalizeLegacySettingsUI() {
  document.querySelectorAll('.nav-item[data-tab="settings"]').forEach((el) => el.remove());
  document.querySelectorAll('#tab-settings').forEach((el) => {
    el.classList.remove('active');
    el.remove();
  });
}

function normalizeLegacyBroadcastHeader() {
  document.querySelectorAll('.blast-composer-head button, .blast-composer-head .btn').forEach((btn) => {
    const label = String(btn.textContent || '').trim().toLowerCase();
    if (label === 'simpan draf' || label === 'kembali') {
      btn.remove();
    }
  });
}

function sanitizeWabaCloudCopy() {
  const wabaTab = $('tab-waba');
  if (!wabaTab) return;
  const subtitle = wabaTab.querySelector('.page-header .page-subtitle');
  if (subtitle) {
    const text = String(subtitle.textContent || '');
    if (
      text.includes('Sambungkan akun WhatsApp Business resmi secara manual') ||
      text.includes('Flow popup otomatis disembunyikan') ||
      text.includes('koneksi manual yang lebih stabil')
    ) {
      subtitle.remove();
    }
  }
  wabaTab.querySelectorAll('p, .hint, .status-text').forEach((el) => {
    const text = String(el.textContent || '');
    if (text.includes('Flow popup otomatis disembunyikan')) {
      el.textContent = text.replace(/Flow popup otomatis disembunyikan dulu agar fokus ke koneksi manual yang lebih stabil\.?/gi, '').trim();
    }
    if (text.includes('tanpa menunggu Embedded Signup siap')) {
      el.textContent = text.replace(/Cocok untuk login manual dulu tanpa menunggu Embedded Signup siap\.?/gi, '').trim();
    }
  });
}

function ensureToolsAccountSelector() {
  const toolsTab = $('tab-tools');
  if (!toolsTab) return;
  let select = $('toolsAccountSelect');
  if (!select) {
    const existingGrid = toolsTab.querySelector('.tools-grid');
    const card = document.createElement('div');
    card.className = 'card';
    card.style.marginBottom = '18px';
    card.innerHTML = `
      <div class="card-title"><span>Akun</span> Pilih Akun WhatsApp untuk Tools</div>
      <div class="form-group" style="margin-top:14px;">
        <label>Akun yang dipakai</label>
        <select id="toolsAccountSelect" onchange="loadWAGroups(); loadChatHistory()"><option value="">Memuat akun...</option></select>
      </div>
    `;
    if (existingGrid) {
      toolsTab.insertBefore(card, existingGrid);
    } else {
      toolsTab.appendChild(card);
    }
    select = $('toolsAccountSelect');
  }
  renderToolsAccountSelect();
}

function normalizeLegacyLayout() {
  normalizeLegacyTopbar();
  normalizeLegacySettingsUI();
  normalizeLegacyBroadcastHeader();
  sanitizeWabaCloudCopy();
  ensureToolsAccountSelector();
}

function applyAccounts(accounts = [], activeID = '') {
  waAccounts = Array.isArray(accounts) ? accounts : [];
  activeWAAccountId = activeID || (waAccounts[0]?.id || '');
  normalizeLegacyLayout();
  renderAccountSwitcher();
  renderAccountCards();
  renderActiveAccountSummary();
  renderBroadcastAccountSelect();
  renderToolsAccountSelect();
  renderPersonalAccountSelect();
  renderAIAccountPicker();
}

function renderBroadcastAccountSelect() {
  const sel = $('broadcastAccountSelect');
  if (!sel) return;
  const currentValue = sel.value || activeWAAccountId;
  if (!waAccounts.length) {
    sel.innerHTML = '<option value="">Belum ada akun WA</option>';
    sel.disabled = true;
    return;
  }
  sel.disabled = false;
  sel.innerHTML = waAccounts.map(acc => {
    const selected = acc.id === currentValue || (!currentValue && acc.id === activeWAAccountId) ? ' selected' : '';
    const phone = acc.phone ? ` - ${escapeHtml(acc.phone)}` : '';
    return `<option value="${escapeHtml(acc.id)}"${selected}>${escapeHtml(acc.name)}${phone}</option>`;
  }).join('');
  renderBroadcastPreview();
}

function getSelectedBroadcastAccountID() {
  return $('broadcastAccountSelect')?.value || activeWAAccountId || '';
}

function renderToolsAccountSelect() {
  const sel = $('toolsAccountSelect');
  if (!sel) return;
  const currentValue = sel.value || activeWAAccountId;
  if (!waAccounts.length) {
    sel.innerHTML = '<option value="">Belum ada akun WA</option>';
    sel.disabled = true;
    return;
  }
  sel.disabled = false;
  sel.innerHTML = waAccounts.map(acc => {
    const selected = acc.id === currentValue || (!currentValue && acc.id === activeWAAccountId) ? ' selected' : '';
    const phone = acc.phone ? ` - ${escapeHtml(acc.phone)}` : '';
    return `<option value="${escapeHtml(acc.id)}"${selected}>${escapeHtml(acc.name)}${phone}</option>`;
  }).join('');
}

function getSelectedToolsAccountID() {
  return $('toolsAccountSelect')?.value || activeWAAccountId || '';
}

function renderPersonalAccountSelect() {
  const sel = $('personalAccountSelect');
  if (!sel) return;
  const currentValue = sel.value || activeWAAccountId;
  if (!waAccounts.length) {
    sel.innerHTML = '<option value="">Belum ada akun WA</option>';
    sel.disabled = true;
    return;
  }
  sel.disabled = false;
  sel.innerHTML = waAccounts.map(acc => {
    const selected = acc.id === currentValue || (!currentValue && acc.id === activeWAAccountId) ? ' selected' : '';
    const phone = acc.phone ? ` - ${escapeHtml(acc.phone)}` : '';
    return `<option value="${escapeHtml(acc.id)}"${selected}>${escapeHtml(acc.name)}${phone}</option>`;
  }).join('');
}

function getSelectedPersonalAccountID() {
  return $('personalAccountSelect')?.value || activeWAAccountId || '';
}

function syncAISelectedAccountsFromDOM() {
  aiSelectedAccountIDs = Array.from(document.querySelectorAll('#aiAccountPicker input[type="checkbox"]:checked'))
    .map(el => el.value)
    .filter(Boolean);
  renderAIAccountKnowledgeMatrix();
}

function renderAIAccountPicker() {
  const wrap = $('aiAccountPicker');
  if (!wrap) return;
  if (!waAccounts.length) {
    wrap.innerHTML = '<div class="empty-state-inline">Belum ada akun WhatsApp.</div>';
    aiSelectedAccountIDs = [];
    renderAIAccountKnowledgeMatrix();
    return;
  }

  const existingIDs = new Set(waAccounts.map(acc => acc.id));
  let selectedIDs = (aiSelectedAccountIDs || []).filter(id => existingIDs.has(id));
  if (!selectedIDs.length && activeWAAccountId) {
    selectedIDs = [activeWAAccountId];
  }
  aiSelectedAccountIDs = selectedIDs;

  wrap.innerHTML = waAccounts.map(acc => {
    const checked = selectedIDs.includes(acc.id) ? ' checked' : '';
    const phone = acc.phone || acc.jid || '-';
    return `
      <label class="account-picker-item">
        <input type="checkbox" value="${escapeHtml(acc.id)}"${checked} onchange="syncAISelectedAccountsFromDOM()" />
        <span>
          <strong>${escapeHtml(acc.name || 'Akun WA')}</strong>
          <small>${escapeHtml(phone)}</small>
        </span>
      </label>
    `;
  }).join('');
  renderAIAccountKnowledgeMatrix();
}

function getSelectedAIAccountIDs() {
  syncAISelectedAccountsFromDOM();
  return [...aiSelectedAccountIDs];
}

function normalizeAIKnowledgeProducts(items) {
  return (Array.isArray(items) ? items : [])
    .map(item => ({
      id: String(item.id || '').trim(),
      name: String(item.name || '').trim(),
      content: String(item.content || '').trim(),
      image_path: String(item.image_path || '').trim(),
      image_url: String(item.image_url || '').trim(),
    }))
    .filter(item => item.id && item.name);
}

function normalizeAIAccountProductMap(raw) {
  const map = {};
  const validProductIDs = new Set(aiKnowledgeProducts.map(item => item.id));
  Object.entries(raw || {}).forEach(([accountID, ids]) => {
    const filtered = Array.from(new Set((Array.isArray(ids) ? ids : [])
      .map(id => String(id || '').trim())
      .filter(id => id && validProductIDs.has(id))));
    if (filtered.length) {
      map[String(accountID || '').trim()] = filtered;
    }
  });
  return map;
}

function renderAIKnowledgeImagePreview() {
  const preview = $('aiKnowledgeImagePreview');
  if (!preview) return;
  const draft = aiKnowledgeImageDraft;
  if (!draft && !$('aiKnowledgeEditingId')?.value) {
    preview.innerHTML = '';
    return;
  }

  const current = draft || aiKnowledgeProducts.find(item => item.id === $('aiKnowledgeEditingId')?.value);
  if (!current) {
    preview.innerHTML = '';
    return;
  }

  const imageURL = current.image_url || '';
  const imageName = current.name || 'Gambar produk';
  preview.innerHTML = `
    <div class="ai-knowledge-image-preview-card">
      ${imageURL ? `<img src="${escapeHtml(imageURL)}" alt="${escapeHtml(imageName)}" class="ai-knowledge-preview-thumb" />` : ''}
      <div>
        <div class="ai-knowledge-preview-title">${escapeHtml(imageName)}</div>
        <div class="ai-knowledge-preview-meta">${imageURL ? 'Gambar siap dipakai untuk product knowledge ini.' : 'Belum ada gambar.'}</div>
      </div>
    </div>
  `;
}

function renderAIKnowledgeList() {
  renderAIKnowledgeSelect();
  renderAIKnowledgeDetail();
}

function renderAIKnowledgeSelect() {
  const select = $('aiKnowledgeSelect');
  if (!select) return;
  if (!aiKnowledgeProducts.length) {
    select.innerHTML = '<option value="">Belum ada product knowledge</option>';
    select.disabled = true;
    aiSelectedKnowledgeID = '';
    return;
  }

  const exists = aiKnowledgeProducts.some(item => item.id === aiSelectedKnowledgeID);
  if (!exists) aiSelectedKnowledgeID = aiKnowledgeProducts[0].id;
  select.disabled = false;
  select.innerHTML = aiKnowledgeProducts.map(item => {
    const selected = item.id === aiSelectedKnowledgeID ? ' selected' : '';
    return `<option value="${escapeHtml(item.id)}"${selected}>${escapeHtml(item.name)}</option>`;
  }).join('');
}

function renderAIKnowledgeDetail() {
  const wrap = $('aiKnowledgeDetail');
  if (!wrap) return;
  const item = aiKnowledgeProducts.find(product => product.id === aiSelectedKnowledgeID);
  if (!item) {
    wrap.innerHTML = '<div class="empty-state-inline">Pilih produk untuk melihat knowledge.</div>';
    return;
  }

  wrap.innerHTML = `
    <div class="ai-knowledge-detail-card">
      ${item.image_url ? `<img src="${escapeHtml(item.image_url)}" alt="${escapeHtml(item.name)}" class="ai-knowledge-detail-thumb" />` : '<div class="ai-knowledge-detail-thumb ai-knowledge-card-thumb-empty">No Image</div>'}
      <div class="ai-knowledge-detail-body">
        <strong>${escapeHtml(item.name)}</strong>
        <div>${escapeHtml(item.content || '').replace(/\n/g, '<br>')}</div>
      </div>
    </div>
  `;
}

function handleAIKnowledgeSelect(productID) {
  aiSelectedKnowledgeID = String(productID || '').trim();
  renderAIKnowledgeDetail();
}

function startNewAIKnowledgeProduct() {
  clearAIKnowledgeForm();
  if ($('aiKnowledgeEditor')) $('aiKnowledgeEditor').style.display = 'block';
  if ($('aiKnowledgeName')) $('aiKnowledgeName').focus();
}

function editSelectedAIKnowledgeProduct() {
  if (!aiSelectedKnowledgeID) {
    showToast('Pilih produk yang ingin diedit', 'error');
    return;
  }
  editAIKnowledgeProduct(aiSelectedKnowledgeID);
}

function deleteSelectedAIKnowledgeProduct() {
  if (!aiSelectedKnowledgeID) {
    showToast('Pilih produk yang ingin dihapus', 'error');
    return;
  }
  deleteAIKnowledgeProduct(aiSelectedKnowledgeID);
}

function renderAIAccountKnowledgeMatrix() {
  const wrap = $('aiAccountKnowledgeMatrix');
  if (!wrap) return;
  renderAIKnowledgeAccountSelect();

  const selectedAccounts = waAccounts.filter(acc => (aiSelectedAccountIDs || []).includes(acc.id));
  if (!selectedAccounts.length) {
    wrap.innerHTML = '<div class="empty-state-inline">Pilih dulu akun WhatsApp yang akan memakai AI.</div>';
    return;
  }
  if (!aiKnowledgeProducts.length) {
    wrap.innerHTML = '<div class="empty-state-inline">Tambahkan product knowledge terlebih dahulu.</div>';
    return;
  }

  const accountExists = selectedAccounts.some(acc => acc.id === aiKnowledgeMappingAccountID);
  if (!accountExists) {
    aiKnowledgeMappingAccountID = selectedAccounts[0].id;
    renderAIKnowledgeAccountSelect();
  }

  const acc = selectedAccounts.find(item => item.id === aiKnowledgeMappingAccountID);
  if (!acc) {
    wrap.innerHTML = '<div class="empty-state-inline">Pilih akun WhatsApp terlebih dahulu.</div>';
    return;
  }

  const selected = new Set(aiAccountProductIDs[acc.id] || []);
  const phone = acc.phone || acc.jid || '-';
  wrap.innerHTML = `
    <div class="ai-account-mapping-card">
      <div class="ai-account-mapping-head">
        <strong>${escapeHtml(acc.name || 'Akun WA')}</strong>
        <span>${escapeHtml(phone)}</span>
      </div>
      <div class="ai-account-mapping-grid">
        ${aiKnowledgeProducts.map(item => `
          <label class="ai-product-select-item">
            <input type="checkbox" ${selected.has(item.id) ? 'checked' : ''} onchange="toggleAIProductForAccount('${jsString(acc.id)}','${jsString(item.id)}', this.checked)" />
            <span>${escapeHtml(item.name)}</span>
          </label>
        `).join('')}
      </div>
    </div>
  `;
}

function renderAIKnowledgeAccountSelect() {
  const select = $('aiKnowledgeAccountSelect');
  if (!select) return;
  const selectedAccounts = waAccounts.filter(acc => (aiSelectedAccountIDs || []).includes(acc.id));
  if (!selectedAccounts.length) {
    select.innerHTML = '<option value="">Belum ada akun AI</option>';
    select.disabled = true;
    aiKnowledgeMappingAccountID = '';
    return;
  }

  const exists = selectedAccounts.some(acc => acc.id === aiKnowledgeMappingAccountID);
  if (!exists) aiKnowledgeMappingAccountID = selectedAccounts[0].id;
  select.disabled = false;
  select.innerHTML = selectedAccounts.map(acc => {
    const phone = acc.phone || acc.jid || '';
    const selected = acc.id === aiKnowledgeMappingAccountID ? ' selected' : '';
    return `<option value="${escapeHtml(acc.id)}"${selected}>${escapeHtml(acc.name || 'Akun WA')}${phone ? ' - ' + escapeHtml(phone) : ''}</option>`;
  }).join('');
}

function handleAIKnowledgeAccountSelect(accountID) {
  aiKnowledgeMappingAccountID = String(accountID || '').trim();
  renderAIAccountKnowledgeMatrix();
}

function clearAIKnowledgeForm() {
  if ($('aiKnowledgeEditingId')) $('aiKnowledgeEditingId').value = '';
  if ($('aiKnowledgeName')) $('aiKnowledgeName').value = '';
  if ($('aiKnowledgeContent')) $('aiKnowledgeContent').value = '';
  if ($('aiKnowledgeImageInput')) $('aiKnowledgeImageInput').value = '';
  aiKnowledgeImageDraft = null;
  renderAIKnowledgeImagePreview();
  if ($('aiKnowledgeEditor')) $('aiKnowledgeEditor').style.display = 'none';
}

function editAIKnowledgeProduct(productID) {
  const item = aiKnowledgeProducts.find(product => product.id === productID);
  if (!item) return;
  if ($('aiKnowledgeEditingId')) $('aiKnowledgeEditingId').value = item.id;
  if ($('aiKnowledgeName')) $('aiKnowledgeName').value = item.name || '';
  if ($('aiKnowledgeContent')) $('aiKnowledgeContent').value = item.content || '';
  aiKnowledgeImageDraft = {
    image_path: item.image_path || '',
    image_url: item.image_url || '',
    name: item.name || '',
  };
  if ($('aiKnowledgeEditor')) $('aiKnowledgeEditor').style.display = 'block';
  renderAIKnowledgeImagePreview();
}

function deleteAIKnowledgeProduct(productID) {
  const item = aiKnowledgeProducts.find(product => product.id === productID);
  if (!item) return;
  if (!confirm(`Hapus product knowledge "${item.name}"?`)) return;
  aiKnowledgeProducts = aiKnowledgeProducts.filter(product => product.id !== productID);
  Object.keys(aiAccountProductIDs).forEach(accountID => {
    aiAccountProductIDs[accountID] = (aiAccountProductIDs[accountID] || []).filter(id => id !== productID);
    if (!aiAccountProductIDs[accountID].length) {
      delete aiAccountProductIDs[accountID];
    }
  });
  if ($('aiKnowledgeEditingId')?.value === productID) {
    clearAIKnowledgeForm();
  }
  renderAIKnowledgeList();
  renderAIAccountKnowledgeMatrix();
  showToast('Product knowledge dihapus. Klik Simpan AI agar perubahan aktif.', 'success');
}

function toggleAIProductForAccount(accountID, productID, checked) {
  const current = new Set(aiAccountProductIDs[accountID] || []);
  if (checked) current.add(productID);
  else current.delete(productID);
  const next = Array.from(current);
  if (next.length) aiAccountProductIDs[accountID] = next;
  else delete aiAccountProductIDs[accountID];
}

async function uploadAIKnowledgeImage(file) {
  const formData = new FormData();
  formData.append('image', file);
  const res = await fetch(API + '/ai/products/upload', {
    method: 'POST',
    body: formData,
  });
  const data = await res.json();
  if (res.status === 401) {
    location.href = '/login';
    throw new Error('Unauthorized');
  }
  if (!res.ok) {
    throw new Error(data.error || 'Gagal upload gambar');
  }
  return data;
}

async function handleAIKnowledgeImageUpload(event) {
  const file = event.target.files?.[0];
  if (!file) return;
  try {
    const uploaded = await uploadAIKnowledgeImage(file);
    aiKnowledgeImageDraft = {
      image_path: uploaded.image_path || '',
      image_url: uploaded.image_url || '',
      name: file.name,
    };
    renderAIKnowledgeImagePreview();
    showToast('Gambar produk berhasil diupload', 'success');
  } catch (e) {
    aiKnowledgeImageDraft = null;
    renderAIKnowledgeImagePreview();
    showToast(e.message, 'error');
  }
}

function saveAIKnowledgeProduct() {
  const editingID = $('aiKnowledgeEditingId')?.value?.trim() || '';
  const name = $('aiKnowledgeName')?.value?.trim() || '';
  const content = $('aiKnowledgeContent')?.value?.trim() || '';

  if (!name) {
    showToast('Nama produk wajib diisi', 'error');
    return;
  }
  if (!content) {
    showToast('Deskripsi / knowledge produk wajib diisi', 'error');
    return;
  }

  const nextItem = {
    id: editingID || `prod_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`,
    name,
    content,
    image_path: aiKnowledgeImageDraft?.image_path || '',
    image_url: aiKnowledgeImageDraft?.image_url || '',
  };

  if (editingID) {
    aiKnowledgeProducts = aiKnowledgeProducts.map(item => item.id === editingID ? nextItem : item);
  } else {
    aiKnowledgeProducts = [nextItem, ...aiKnowledgeProducts];
  }

  aiSelectedKnowledgeID = nextItem.id;
  aiAccountProductIDs = normalizeAIAccountProductMap(aiAccountProductIDs);
  renderAIKnowledgeList();
  renderAIAccountKnowledgeMatrix();
  clearAIKnowledgeForm();
  showToast(editingID ? 'Product knowledge diperbarui. Klik Simpan AI agar aktif.' : 'Product knowledge ditambahkan. Klik Simpan AI agar aktif.', 'success');
}

function syncRajaOngkirFields() {
  const enabled = $('aiRajaOngkirEnabled')?.checked || false;
  const wrap = $('aiRajaOngkirFields');
  if (wrap) wrap.style.display = enabled ? 'block' : 'none';
}

function renderAccountSwitcher() {
  const sel = $('accountSwitcher');
  if (!sel) return;
  if (!waAccounts.length) {
    sel.innerHTML = '<option value="">Belum ada akun</option>';
    sel.disabled = true;
    return;
  }
  sel.disabled = false;
  sel.innerHTML = waAccounts.map(acc => {
    const phone = acc.phone ? ` - ${escapeHtml(acc.phone)}` : '';
    const status = acc.status ? ` (${escapeHtml(acc.status)})` : '';
    const selected = acc.id === activeWAAccountId ? ' selected' : '';
    return `<option value="${escapeHtml(acc.id)}"${selected}>${escapeHtml(acc.name)}${phone}${status}</option>`;
  }).join('');
}

function renderAccountCards() {
  const wrap = $('waAccountCards');
  if (!wrap) return;
  if (!waAccounts.length) {
    wrap.innerHTML = '<div class="empty-state-inline">Belum ada akun WA.</div>';
    return;
  }
  wrap.innerHTML = waAccounts.map(acc => {
    const active = acc.id === activeWAAccountId ? ' active' : '';
    const phone = acc.phone || acc.jid || '-';
    const pending = acc.is_pending ? '<span class="account-chip pending">Siap scan QR</span>' : '';
    const webhookEnabled = acc.webhook_enabled ? ' checked' : '';
    const webhookStatus = acc.webhook_enabled ? '<span class="account-chip webhook-on">Webhook aktif</span>' : '';
    const webhookOpen = acc.webhook_enabled || String(acc.webhook_url || '').trim() ? ' open' : '';
    return `
      <div class="account-card${active}" data-account-id="${escapeHtml(acc.id || '')}">
        <div class="account-card-head">
          <div>
            <div class="account-card-title">${escapeHtml(acc.name || 'Akun WA')}</div>
            <div class="account-card-subtitle">${escapeHtml(phone)}</div>
          </div>
          <div class="account-chip-row">
            <span class="account-chip">${escapeHtml(acc.status || 'Offline')}</span>
            ${pending}
            ${webhookStatus}
          </div>
        </div>
        <details class="account-webhook-panel"${webhookOpen}>
          <summary class="account-webhook-summary">
            <div>
              <div class="account-webhook-title">Webhook</div>
            </div>
            <span class="account-webhook-summary-badge">${acc.webhook_enabled ? 'Aktif' : 'Opsional'}</span>
          </summary>
          <label class="account-webhook-toggle">
            <input type="checkbox" class="account-webhook-enabled"${webhookEnabled} />
            <span>Aktifkan webhook</span>
          </label>
          <div class="account-webhook-grid">
            <input class="account-webhook-url" type="url" placeholder="https://domain-anda.com/webhook" value="${escapeHtml(acc.webhook_url || '')}" />
          </div>
          <div class="account-webhook-actions">
            <button class="btn btn-secondary btn-sm" onclick="saveAccountWebhook('${escapeHtml(jsString(acc.id))}')">Simpan Webhook</button>
          </div>
        </details>
        <div class="account-card-actions">
          <button class="btn btn-secondary btn-sm" onclick="handleAccountSwitch('${escapeHtml(jsString(acc.id))}')">Aktifkan</button>
          <button class="btn btn-secondary btn-sm" onclick="renameAccount('${escapeHtml(jsString(acc.id))}')">Rename</button>
          <button class="btn btn-danger btn-sm" onclick="deleteAccount('${escapeHtml(jsString(acc.id))}')">Hapus</button>
        </div>
      </div>
    `;
  }).join('');
}

function renderActiveAccountSummary() {
  const active = getActiveAccount();
  if ($('heroAccountName')) $('heroAccountName').textContent = active?.name || 'Belum ada akun';
  if ($('heroAccountStatus')) $('heroAccountStatus').textContent = active?.status || 'Tambahkan akun lalu scan QR.';
  if ($('accountCount')) $('accountCount').textContent = waAccounts.length;
  if ($('activeAccountName')) $('activeAccountName').textContent = active?.name || '-';
  if ($('activeAccountStatus')) $('activeAccountStatus').textContent = active?.status || '-';
  if ($('activeAccountPhone')) $('activeAccountPhone').textContent = active?.phone || active?.jid || '-';
}

async function loadCurrentUser() {
  const data = await api('/me');
  currentUser = data.user || null;
  renderCurrentUser();
  return currentUser;
}

function renderCurrentUser() {
  const role = currentUser?.is_admin ? 'Admin' : (currentUser?.is_trial ? 'User Trial' : 'User');
  const aiStatus = currentUser?.can_use_ai ? 'AI aktif' : 'AI terkunci';
  const expires = currentUser?.expires_at
    ? new Date(currentUser.expires_at).toLocaleDateString('id-ID')
    : '-';
  const maxDevices = currentUser?.max_devices || 0;

  if ($('currentUserLabel')) $('currentUserLabel').textContent = role;
  if ($('dashboardUserEmail')) $('dashboardUserEmail').textContent = currentUser?.email || '-';
  if ($('dashboardUserRole')) $('dashboardUserRole').textContent = `${role} | ${aiStatus}`;
  if ($('dashboardUserExpires')) $('dashboardUserExpires').textContent = expires;
  if ($('dashboardUserMaxDevices')) $('dashboardUserMaxDevices').textContent = String(maxDevices);
  if ($('adminNavItem')) $('adminNavItem').style.display = currentUser?.is_admin ? 'flex' : 'none';
  renderAPIDocsMeta();
}

function renderAPIDocsMeta() {
  if ($('apiDocsBaseUrl')) $('apiDocsBaseUrl').textContent = location.origin;
  if ($('apiDocsUsername')) $('apiDocsUsername').textContent = currentUser?.email || 'Email user yang login di aplikasi ini';
}

async function copyAPIDocValue(id, label) {
  const el = $(id);
  if (!el) return;
  const text = el.textContent || '';
  try {
    await navigator.clipboard.writeText(text);
    showToast(`${label} disalin`, 'success');
  } catch (_) {
    showToast(`Gagal menyalin ${label.toLowerCase()}`, 'error');
  }
}

async function logoutApp() {
  try {
    await api('/auth/logout', { method: 'POST' });
  } catch (_) {}
  location.href = '/login';
}

async function loadAccounts() {
  try {
    const data = await api('/accounts');
    applyAccounts(data.accounts || [], data.active_account_id || '');
  } catch (e) {
    appendLog('Gagal memuat akun: ' + e.message, 'error');
  }
}

async function handleAccountSwitch(accountID) {
  if (!accountID || accountID === activeWAAccountId) return;
  try {
    const data = await api('/accounts/active', {
      method: 'POST',
      body: JSON.stringify({ account_id: accountID })
    });
    activeWAAccountId = data.active_account_id || accountID;
    await checkConnection();
    showToast('Akun aktif diganti', 'success');
    if ($('tab-tools')?.classList.contains('active')) {
      loadWAGroups();
      loadChatHistory();
    }
  } catch (e) {
    showToast('Gagal ganti akun: ' + e.message, 'error');
  }
}

async function createAccount() {
  try {
    const data = await api('/accounts', {
      method: 'POST',
      body: JSON.stringify({ name: $('newAccountName')?.value?.trim() || '' })
    });
    const newAccountID = data.active_account_id || data.account?.id || '';
    if ($('newAccountName')) $('newAccountName').value = '';
    if (newAccountID) {
      try {
        await api('/accounts/active', {
          method: 'POST',
          body: JSON.stringify({ account_id: newAccountID })
        });
      } catch (_) {}
    }
    await loadAccounts();
    showToast('Akun WA ditambahkan', 'success');
    if (newAccountID) {
      activeWAAccountId = newAccountID;
      renderAccountSwitcher();
      renderAccountCards();
      renderActiveAccountSummary();
      await openQRForAccount(newAccountID);
    }
  } catch (e) {
    showToast('Gagal tambah akun: ' + e.message, 'error');
  }
}

async function openQRForAccount(accountID = activeWAAccountId) {
  switchTab('home');
  await new Promise(resolve => setTimeout(resolve, 50));
  return requestQR(accountID);
}

async function renameAccount(accountID) {
  const current = waAccounts.find(acc => acc.id === accountID);
  const name = prompt('Nama akun baru:', current?.name || '');
  if (!name) return;
  try {
    await api('/accounts/' + encodeURIComponent(accountID), {
      method: 'PATCH',
      body: JSON.stringify({ name })
    });
    await loadAccounts();
    showToast('Nama akun diperbarui', 'success');
  } catch (e) {
    showToast('Gagal rename akun: ' + e.message, 'error');
  }
}

async function saveAccountWebhook(accountID) {
  const card = findAccountCard(accountID);
  if (!card) return;
  const enabled = card.querySelector('.account-webhook-enabled')?.checked || false;
  const url = card.querySelector('.account-webhook-url')?.value?.trim() || '';

  if (enabled && !url) {
    showToast('Isi URL webhook dulu atau matikan checklist webhook.', 'error');
    return;
  }

  try {
    const data = await api('/accounts/' + encodeURIComponent(accountID) + '/webhook', {
      method: 'PATCH',
      body: JSON.stringify({ enabled, url, secret: '' })
    });
    waAccounts = waAccounts.map(acc => acc.id === accountID ? data.account : acc);
    renderAccountCards();
    showToast(enabled ? 'Webhook nomor aktif' : 'Webhook nomor dimatikan', 'success');
  } catch (e) {
    showToast('Gagal simpan webhook: ' + e.message, 'error');
  }
}

async function deleteAccount(accountID) {
  if (!confirm('Hapus akun WhatsApp ini?')) return;
  try {
    const data = await api('/accounts/' + encodeURIComponent(accountID), { method: 'DELETE' });
    applyAccounts(data.accounts || [], data.active_account_id || '');
    await checkConnection();
    showToast('Akun dihapus', 'success');
  } catch (e) {
    showToast('Gagal hapus akun: ' + e.message, 'error');
  }
}

// ===== Tab Navigation =====
function switchTab(tab) {
  normalizeLegacyLayout();
  if (tab === 'settings') tab = 'home';
  let paneTab = tab;
  if (tab === 'groups') paneTab = 'contacts';
  if (tab === 'templates') paneTab = 'personalisasi';
  if (tab === 'schedule' || tab === 'reports') paneTab = 'history';
  if (tab === 'history') tab = 'reports';
  document.querySelectorAll('.nav-item').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
  const btn = document.querySelector(`[data-tab="${tab}"]`);
  if (btn) btn.classList.add('active');
  const pane = $('tab-' + paneTab);
  if (pane) pane.classList.add('active');
  if (paneTab === 'accounts') loadAccounts();
  if (paneTab === 'contacts') {
    loadContactLists();
    loadUnsubscribeData();
    switchContactsView(contactsView);
  }
  if (paneTab === 'file-manager') loadMediaFiles();
  if (paneTab === 'history') loadHistory();
  if (paneTab === 'tools') {
    renderToolsAccountSelect();
    loadWAGroups();
    loadChatHistory();
  }
  if (paneTab === 'settings') loadSettings();
  if (paneTab === 'ai') {
    loadAISettings();
    refreshAIStats();
    startAIStatsPolling();
  }
  if (paneTab === 'broadcast') {
    setBroadcastMode('composer');
    loadSettings();
    loadContactLists();
    loadUnsubscribeSettings();
    loadMediaFiles();
    loadTemplates();
    renderBroadcastPreview();
  }
  if (paneTab === 'personalisasi') {
    loadTemplatesPersonal();
    loadPersonalSchedules();
  }
  if (paneTab === 'admin') {
    loadAdminUsers();
    loadAdminAIConfig();
    loadAdminMetaConfig();
    loadAdminTrialOTPConfig();
    loadAdminTrialOTPStatus();
  }
  if (paneTab === 'waba') {
    sanitizeWabaCloudCopy();
    loadMetaAccounts();
  }
  closeSidebar();
}

// ===== WebSocket =====
function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}/ws`);

  ws.onopen = () => {
    appendLog('WebSocket terhubung', 'success');
  };

  ws.onmessage = (evt) => {
    try {
      const data = JSON.parse(evt.data);
      if (data.type === 'log') {
        appendLog(data.message, data.level);
        if (data.level === 'unsubscribe' || String(data.message || '').startsWith('unsubscribe_updated:')) {
          loadUnsubscribeList();
        }
      } else if (data.type === 'status') {
        updateConnectionUI(data);
      } else if (data.type === 'progress') {
        updateProgressUI(data);
      }
    } catch (_) {}
  };

  ws.onclose = () => {
    appendLog('WebSocket terputus, reconnecting...', 'warning');
    setTimeout(connectWS, 3000);
  };

  ws.onerror = () => {};
}

// ===== Connection =====
async function checkConnection() {
  try {
    const data = await api(appendAccountQuery('/status'));
    applyAccounts(data.accounts || waAccounts, data.active_account_id || activeWAAccountId);
    updateConnectionUI(data);
  } catch (_) {
    updateConnectionUI({ connected: false, jid: '' });
  }
}

function updateConnectionUI(status) {
  const connected = !!status?.connected;
  const jid = status?.jid || '';
  const active = getActiveAccount();
  const dot = $('connDot');
  const text = $('connText');
  const stat = $('statWAStatus');
  const btnScan = $('btnScanQR');
  const qrContainer = $('qrContainer');
  const connectedInfo = $('connectedInfo');
  const connJID = $('connJID');
  const connAccountMeta = $('connAccountMeta');

  if (connected) {
    if (dot) dot.classList.add('connected');
    if (text) text.textContent = 'Terhubung';
    if (stat) stat.textContent = 'Online';
    if (btnScan) btnScan.style.display = 'none';
    if (qrContainer) qrContainer.style.display = 'none';
    if (connectedInfo) connectedInfo.style.display = 'block';
    if (connJID) connJID.textContent = jid || '';
    if (connAccountMeta) connAccountMeta.textContent = active ? `${active.name} • ${active.status || 'Online'}` : '';
  } else {
    if (dot) dot.classList.remove('connected');
    if (text) text.textContent = 'Tidak Terhubung';
    if (stat) stat.textContent = 'Offline';
    if (btnScan) btnScan.style.display = 'flex';
    if (connectedInfo) connectedInfo.style.display = 'none';
    if (connAccountMeta) connAccountMeta.textContent = active ? `${active.name} • ${active.status || 'Offline'}` : '-';
  }
  renderActiveAccountSummary();
}

// ===== QR Code =====
async function requestQR(accountID = activeWAAccountId) {
  const qrContainer = $('qrContainer');
  const qrImage = $('qrImage');
  const btnScan = $('btnScanQR');
  const targetAccountID = accountID || activeWAAccountId;

  appendLog('Meminta QR Code...', 'info');
  showToast('Meminta QR Code...', 'info');

  try {
    const data = await api(appendAccountQuery('/qr', targetAccountID));
    if (data.account_id && data.account_id !== activeWAAccountId) {
      activeWAAccountId = data.account_id;
      await loadAccounts();
    }
    if (data.status === 'already_logged_in') {
      showToast('Sudah login!', 'success');
      updateConnectionUI({ connected: true, jid: data.jid });
      return;
    }
    if (data.status === 'qr' && data.qr) {
      qrImage.src = data.qr;
      qrContainer.style.display = 'block';
      if (btnScan) btnScan.style.display = 'none';
      appendLog('QR Code ditampilkan, silakan scan', 'success');

      pollConnection(data.account_id || targetAccountID || activeWAAccountId);
    }
  } catch (e) {
    showToast('Gagal mendapatkan QR: ' + e.message, 'error');
  }
}

function pollConnection(accountID = activeWAAccountId) {
  let attempts = 0;
  const interval = setInterval(async () => {
    attempts++;
    if (attempts > 60) { clearInterval(interval); return; }
    try {
      const data = await api(appendAccountQuery('/status', accountID));
      if (data.connected) {
        clearInterval(interval);
        await loadAccounts();
        updateConnectionUI(data);
        showToast('WhatsApp Terhubung!', 'success');
        appendLog('WhatsApp terhubung: ' + data.jid, 'success');
      }
    } catch (_) {}
  }, 3000);
}

async function reconnectWA() {
  try {
    await api('/reconnect', { method: 'POST', body: JSON.stringify(withAccountBody()) });
    showToast('Reconnecting...', 'info');
    appendLog('Reconnecting WhatsApp...', 'info');
    setTimeout(checkConnection, 3000);
  } catch (e) {
    showToast('Gagal reconnect: ' + e.message, 'error');
  }
}

async function logoutWA() {
  if (!confirm('Yakin ingin logout dari WhatsApp?')) return;
  try {
    await api('/logout', { method: 'POST', body: JSON.stringify(withAccountBody()) });
    showToast('Logout berhasil', 'success');
    await checkConnection();
  } catch (e) {
    showToast('Gagal logout: ' + e.message, 'error');
  }
}

// ===== Broadcast =====
function countBroadcastNumbers(raw) {
  return String(raw || '')
    .split(/[\n,;]/)
    .map(item => item.trim())
    .filter(item => /\d{5,}/.test(item)).length;
}

function getBroadcastTimingConfig() {
  const inlineRandom = $('broadcastRandomDelay');
  const inlineBreak = $('broadcastBreakEnabled');
  const randomDelay = inlineRandom ? inlineRandom.checked : ($('randomDelay')?.checked || false);
  const breakEnabled = inlineBreak ? inlineBreak.checked : true;
  return {
    delay: parseInt($('broadcastDelay')?.value || $('delay')?.value || '3', 10) || 3,
    randomDelay,
    delayMin: parseInt($('broadcastDelayMin')?.value || $('delayMin')?.value || '2', 10) || 2,
    delayMax: parseInt($('broadcastDelayMax')?.value || $('delayMax')?.value || '5', 10) || 5,
    burstEvery: breakEnabled ? (parseInt($('broadcastBurstEvery')?.value || $('burstEvery')?.value || '0', 10) || 0) : 0,
    burstPause: breakEnabled ? (parseInt($('broadcastBurstPauseSec')?.value || $('burstPauseSec')?.value || '0', 10) || 0) : 0,
  };
}

function syncInlineBroadcastTimingFromSettings() {
  if ($('broadcastDelay') && $('delay')) $('broadcastDelay').value = $('delay').value || 3;
  if ($('broadcastRandomDelay') && $('randomDelay')) $('broadcastRandomDelay').checked = $('randomDelay').checked;
  if ($('broadcastDelayMin') && $('delayMin')) $('broadcastDelayMin').value = $('delayMin').value || 2;
  if ($('broadcastDelayMax') && $('delayMax')) $('broadcastDelayMax').value = $('delayMax').value || 5;
  if ($('broadcastBurstEvery') && $('burstEvery')) $('broadcastBurstEvery').value = $('burstEvery').value || 0;
  if ($('broadcastBurstPauseSec') && $('burstPauseSec')) $('broadcastBurstPauseSec').value = $('burstPauseSec').value || 0;
  if ($('broadcastBreakEnabled')) {
    const hasBreak = (parseInt($('broadcastBurstEvery')?.value || '0', 10) || 0) > 0 && (parseInt($('broadcastBurstPauseSec')?.value || '0', 10) || 0) > 0;
    $('broadcastBreakEnabled').checked = hasBreak;
  }
  if ($('broadcastRandomDelayRow')) {
    $('broadcastRandomDelayRow').style.display = $('broadcastRandomDelay')?.checked ? 'block' : 'none';
  }
}

function setBroadcastMode(mode) {
  broadcastMode = mode === 'composer' ? 'composer' : 'list';
  $('broadcastListView')?.classList.toggle('active', broadcastMode === 'list');
  $('broadcastComposerView')?.classList.toggle('active', broadcastMode === 'composer');
  if (broadcastMode === 'composer') {
    syncInlineBroadcastTimingFromSettings();
    restoreBroadcastDraft();
    renderBroadcastPreview();
  }
}

function switchBroadcastType(type) {
  broadcastType = type === 'waba' ? 'waba' : 'unofficial';
  $('broadcastTypeUnofficial')?.classList.toggle('active', broadcastType === 'unofficial');
  $('broadcastTypeWaba')?.classList.toggle('active', broadcastType === 'waba');
  $('broadcastUnofficialPanel')?.classList.toggle('active', broadcastType === 'unofficial');
  $('broadcastWabaPanel')?.classList.toggle('active', broadcastType === 'waba');
}

function openBroadcastComposer() {
  setBroadcastMode('composer');
}

function closeBroadcastComposer() {
  switchTab('history');
}

function toggleBroadcastComposerProgress() {
  if (broadcastMode !== 'composer') {
    openBroadcastComposer();
    return;
  }
  const wrap = $('progressWrap');
  if (!wrap) return;
  wrap.scrollIntoView({ behavior: 'smooth', block: 'center' });
}

function extractBroadcastPreviewTokens(message) {
  const text = String(message || '');
  const rawTokens = text.match(/\{[^{}\n]+\}/g) || [];
  const variables = [];
  const spintax = [];
  rawTokens.forEach((token) => {
    if (token.includes('|')) {
      if (!spintax.includes(token)) spintax.push(token);
      return;
    }
    if (!variables.includes(token)) variables.push(token);
  });
  return { variables, spintax };
}

function resolveBroadcastPreviewSpintax(text) {
  return String(text || '').replace(/\{([^{}\n]*\|[^{}\n]*)\}/g, (_, body) => {
    const options = String(body || '')
      .split('|')
      .map(item => item.trim())
      .filter(Boolean);
    return options[0] || '';
  });
}

function getPreviewContactValue(contact, key) {
  if (!contact || !key) return '';
  const normalizedKey = String(key).trim().toLowerCase();
  if (normalizedKey === 'nama_wa' || normalizedKey === 'wa_name') {
    return getPreviewContactValue(contact, 'nama')
      || getPreviewContactValue(contact, 'name')
      || getPreviewContactValue(contact, 'customer')
      || getPreviewContactValue(contact, 'pelanggan')
      || getPreviewContactValue(contact, 'nomor')
      || getPreviewContactValue(contact, 'wa')
      || getPreviewContactValue(contact, 'phone');
  }
  const exact = Object.keys(contact).find(item => String(item).trim().toLowerCase() === normalizedKey);
  if (!exact) return '';
  return String(contact[exact] ?? '').trim();
}

function resolveBroadcastPreviewVariables(text, contact) {
  return String(text || '').replace(/\{([^{}\n|]+)\}/g, (token, key) => {
    const value = getPreviewContactValue(contact, key);
    return value || token;
  });
}

function applyBroadcastPreviewWhatsappFormatting(text) {
  return String(text || '')
    .replace(/(^|[\s(>])\*([^*\n][^*\n]*?)\*(?=($|[\s).,!?:;<\]}]))/g, '$1<strong>$2</strong>')
    .replace(/(^|[\s(>])_([^_\n][^_\n]*?)_(?=($|[\s).,!?:;<\]}]))/g, '$1<em>$2</em>')
    .replace(/(^|[\s(>])~([^~\n][^~\n]*?)~(?=($|[\s).,!?:;<\]}]))/g, '$1<del>$2</del>')
    .replace(/(^|[\s(>])`([^`\n][^`\n]*?)`(?=($|[\s).,!?:;<\]}]))/g, '$1<code>$2</code>');
}

function formatBroadcastPreviewMessage(message, sampleContact) {
  const text = String(message || '').trim();
  if (!text) return 'Tulis isi pesan untuk melihat preview di sini.';
  const resolved = resolveBroadcastPreviewVariables(resolveBroadcastPreviewSpintax(text), sampleContact);
  const variableTokens = [];
  const tokenized = resolved.replace(/\{[^{}\n|]+\}/g, (token) => {
    const marker = `@@VAR_${variableTokens.length}@@`;
    variableTokens.push(`<span class="blast-preview-token variable">${escapeHtml(token)}</span>`);
    return marker;
  });

  let html = escapeHtml(tokenized);
  html = applyBroadcastPreviewWhatsappFormatting(html);
  html = html.replace(/@@VAR_(\d+)@@/g, (_, index) => variableTokens[Number(index)] || '');
  return html.replace(/\n/g, '<br>');
}

function normalizePreviewTextForCompare(value) {
  return String(value || '')
    .toLowerCase()
    .replace(/[*_~`\-—\r\n]/g, '')
    .replace(/\s+/g, ' ')
    .trim();
}

function previewTextContainsInstruction(message, instruction) {
  const main = normalizePreviewTextForCompare(message);
  const footer = normalizePreviewTextForCompare(instruction);
  return !!main && !!footer && main.includes(footer);
}

function renderBroadcastPreviewSyntaxSummary(messageText) {
  const wrap = $('broadcastPreviewSyntax');
  if (!wrap) return;
  const { variables, spintax } = extractBroadcastPreviewTokens(messageText);
  const sections = [];
  if (variables.length) {
    sections.push(`
      <div class="blast-preview-syntax-row">
        <div class="blast-preview-syntax-title">Variabel Terdeteksi</div>
        <div class="blast-preview-syntax-tags">
          ${variables.map(token => `<span class="blast-preview-syntax-tag">${escapeHtml(token)}</span>`).join('')}
        </div>
      </div>
    `);
  }
  if (spintax.length) {
    const sampleOptions = spintax
      .slice(0, 3)
      .map(token => {
        const first = token.slice(1, -1).split('|').map(item => item.trim()).filter(Boolean)[0] || '';
        return first ? `<span class="blast-preview-syntax-tag">${escapeHtml(first)}</span>` : '';
      })
      .filter(Boolean)
      .join('');
    sections.push(`
      <div class="blast-preview-syntax-row">
        <div class="blast-preview-syntax-title">Spintax Preview</div>
        <div class="blast-preview-syntax-tags">
          ${sampleOptions}
          <span class="blast-preview-syntax-tag">${spintax.length} spintax memakai opsi pertama</span>
        </div>
      </div>
    `);
  }
  if (!sections.length) {
    wrap.hidden = true;
    wrap.innerHTML = '';
    return;
  }
  wrap.hidden = false;
  wrap.innerHTML = sections.join('');
}

function renderBroadcastPreview() {
  const accountName = $('broadcastPreviewAccount');
  const audience = $('broadcastPreviewAudience');
  const avatar = $('broadcastPreviewAvatar');
  const message = $('broadcastPreviewMessage');
  const images = $('broadcastPreviewImages');
  const unsubscribeNote = $('broadcastPreviewUnsubscribe');
  const time = $('broadcastPreviewTime');

  const currentAccount = waAccounts.find(acc => acc.id === getSelectedBroadcastAccountID()) || getActiveAccount();
  const selectedGroupData = getSelectedContactGroup();
  const firstContact = selectedGroupData?.contacts?.[0] || null;
  const preferredName = firstContact?.nama || firstContact?.Nama || firstContact?.name || firstContact?.customer || firstContact?.Customer || '';
  const previewName = String(preferredName || $('scheduleName')?.value?.trim() || 'Nama Customer').trim();
  if (accountName) accountName.textContent = previewName;
  if (avatar) {
    const parts = previewName.split(/\s+/).filter(Boolean).slice(0, 2);
    const initials = parts.map(part => part[0]?.toUpperCase() || '').join('') || 'CU';
    avatar.textContent = initials;
  }

  const selectedGroup = $('groupSelect')?.value || '';
  const manualCount = countBroadcastNumbers($('numbers')?.value || '');
  const audienceLabel = selectedGroup
    ? `online • ${manualCount || 0} penerima`
    : 'online';
  if (audience) audience.textContent = audienceLabel;

  const messageText = $('message')?.value?.trim() || 'Tulis isi pesan untuk melihat preview di sini.';
  if (message) {
    message.innerHTML = formatBroadcastPreviewMessage(messageText, firstContact);
  }
  renderBroadcastPreviewSyntaxSummary(messageText);
  if (time) {
    time.textContent = new Date().toLocaleTimeString('id-ID', { hour: '2-digit', minute: '2-digit' });
  }

  if (images) {
    if (!imageUploads.length) {
      images.innerHTML = '';
    } else {
      images.innerHTML = imageUploads.map(item => `
        <div class="blast-phone-image-chip">${escapeHtml(formatMediaLabel(item))}</div>
      `).join('');
    }
  }

  const previewUnsubscribe = getCurrentUnsubscribeSettings();
  if (unsubscribeNote) {
    const instruction = String(previewUnsubscribe.instruction || '').trim();
    if (previewUnsubscribe.enabled && instruction && !previewTextContainsInstruction(messageText, instruction)) {
      unsubscribeNote.hidden = false;
      unsubscribeNote.innerHTML = escapeHtml(instruction).replace(/\n/g, '<br>');
    } else {
      unsubscribeNote.hidden = true;
      unsubscribeNote.innerHTML = '';
    }
  }
}

function getSelectedContactGroup() {
  const groupID = $('groupSelect')?.value || '';
  return contactLists.find(item => String(item.id) === String(groupID)) || null;
}

function getBroadcastContactVariables() {
  const group = getSelectedContactGroup();
  if (!group) return [];
  const ignored = new Set(['nomor', 'phone', 'no', 'whatsapp', 'wa', 'hp', 'telepon']);
  const savedVariables = (group.columns || [])
    .map(col => String(col || '').trim())
    .filter(col => col && !ignored.has(col.toLowerCase()));
  return Array.from(new Set(['nama_wa', 'wa_name', ...savedVariables]));
}

function getBroadcastVariableToken(name) {
  return `{${String(name || '').trim()}}`;
}

function renderBroadcastVariablePicker() {
  const select = $('broadcastVariableSelect');
  const chips = $('broadcastVariableChips');
  const hint = $('broadcastVariableHint');
  if (!select || !chips || !hint) return;

  const variables = getBroadcastContactVariables();
  if (!getSelectedContactGroup()) {
    select.innerHTML = '<option value="">Pilih variabel kontak...</option>';
    select.disabled = true;
    chips.innerHTML = '';
    hint.textContent = 'Pilih grup kontak terlebih dahulu untuk menampilkan variabel yang tersedia.';
    return;
  }

  if (!variables.length) {
    select.innerHTML = '<option value="">Tidak ada variabel tambahan</option>';
    select.disabled = true;
    chips.innerHTML = '';
    hint.textContent = 'Grup ini hanya punya kolom nomor. Tambahkan kolom lain di data kontak jika ingin memakai variabel.';
    return;
  }

  select.disabled = false;
  select.innerHTML = '<option value="">Pilih variabel kontak...</option>' + variables.map(name => {
    const token = getBroadcastVariableToken(name);
    return `<option value="${escapeHtml(token)}">${escapeHtml(name)} → ${escapeHtml(token)}</option>`;
  }).join('');
  chips.innerHTML = variables.map(name => {
    const token = getBroadcastVariableToken(name);
    return `<button class="btn btn-secondary btn-sm" type="button" onclick="insertBroadcastVariable('${jsString(token)}')">${escapeHtml(token)}</button>`;
  }).join('');
  hint.textContent = 'Klik chip atau pilih dari dropdown untuk memasukkan variabel ke isi pesan.';
}

function insertAtCursor(el, text) {
  if (!el) return;
  const start = el.selectionStart ?? el.value.length;
  const end = el.selectionEnd ?? el.value.length;
  const before = el.value.slice(0, start);
  const after = el.value.slice(end);
  const needsSpaceBefore = before && !/\s$/.test(before);
  const insertText = `${needsSpaceBefore ? ' ' : ''}${text}`;
  el.value = `${before}${insertText}${after}`;
  const cursorPos = before.length + insertText.length;
  el.focus();
  el.setSelectionRange(cursorPos, cursorPos);
}

function insertBroadcastVariable(token) {
  const message = $('message');
  if (!message || !token) return;
  insertAtCursor(message, token);
  renderBroadcastPreview();
}

function insertSelectedBroadcastVariable() {
  const token = $('broadcastVariableSelect')?.value || '';
  if (!token) {
    showToast('Pilih variabel kontak dulu', 'warning');
    return;
  }
  insertBroadcastVariable(token);
}

function setBroadcastAIButtonsLoading(loading, label) {
  const analyzeBtn = $('broadcastAnalyzeBtn');
  const spintaxBtn = $('broadcastSpintaxBtn');
  const undoBtn = $('broadcastSpintaxUndoBtn');
  [analyzeBtn, spintaxBtn, undoBtn].forEach((btn) => {
    if (!btn) return;
    btn.disabled = !!loading;
  });
  const status = $('broadcastAIStatus');
  if (status && loading) {
    status.textContent = label || 'AI sedang memproses pesan...';
    status.style.color = loading ? '#f59e0b' : '';
  }
}

function updateBroadcastSpintaxUndoButton() {
  const btn = $('broadcastSpintaxUndoBtn');
  if (!btn) return;
  const canRestore = !!String(broadcastSpintaxOriginalMessage || '').length;
  btn.hidden = !canRestore;
  btn.disabled = !canRestore;
}

function renderBroadcastAIResult(data) {
  const wrap = $('broadcastAIResult');
  if (!wrap) return;
  const risk = String(data?.risk_level || '').trim();
  const analysis = normalizeAIMessageFormatting(data?.analysis);
  const improved = normalizeAIMessageFormatting(data?.improved_message);
  broadcastAIImprovedDraft = improved;

  if (!analysis && !improved) {
    wrap.hidden = true;
    wrap.innerHTML = '';
    return;
  }

  wrap.hidden = false;
  wrap.innerHTML = `
    <div class="broadcast-ai-result-head">
      <strong>Hasil Analisa AI</strong>
      ${risk ? `<span class="broadcast-ai-risk">${escapeHtml(risk)}</span>` : ''}
    </div>
    ${analysis ? `<div class="broadcast-ai-copy">${escapeHtml(analysis).replace(/\n/g, '<br>')}</div>` : ''}
    ${improved ? `
      <div class="broadcast-ai-improved">
        <div class="broadcast-ai-label">Teks yang lebih aman</div>
        <div>${escapeHtml(improved).replace(/\n/g, '<br>')}</div>
      </div>
      <button class="btn btn-primary btn-sm" type="button" onclick="applyBroadcastAIImproved()">Pakai Teks Ini</button>
    ` : ''}
  `;
}

function applyBroadcastAIImproved() {
  const message = $('message');
  if (!message || !broadcastAIImprovedDraft) return;
  message.value = broadcastAIImprovedDraft;
  renderBroadcastPreview();
  showToast('Teks hasil perbaikan AI dipakai', 'success');
}

function restoreBroadcastSpintaxMessage() {
  const message = $('message');
  if (!message || !broadcastSpintaxOriginalMessage) return;
  message.value = broadcastSpintaxOriginalMessage;
  broadcastSpintaxOriginalMessage = '';
  updateBroadcastSpintaxUndoButton();
  renderBroadcastPreview();
  showToast('Teks sebelum spintax berhasil dikembalikan', 'success');
}

async function runBroadcastAIAssistant(mode) {
  const messageEl = $('message');
  const rawMessage = messageEl?.value || '';
  const message = rawMessage.trim();
  if (!message) {
    showToast('Isi pesan dulu sebelum memakai AI', 'warning');
    return;
  }

  const isSpintax = mode === 'spintax';
  setBroadcastAIButtonsLoading(true, isSpintax ? 'AI sedang membuat spintax berat...' : 'AI sedang menganalisa risiko spam...');
  try {
    const data = await api('/broadcast/ai-helper', {
      method: 'POST',
      body: JSON.stringify({
        mode,
        message,
        variables: getBroadcastContactVariables(),
      }),
    });

    if (isSpintax) {
      const nextMessage = normalizeAIMessageFormatting(data.spintax_message);
      if (!nextMessage) throw new Error('AI tidak mengembalikan teks spintax');
      broadcastSpintaxOriginalMessage = rawMessage;
      updateBroadcastSpintaxUndoButton();
      messageEl.value = nextMessage;
      renderBroadcastPreview();
      renderBroadcastAIResult({
        risk_level: 'spintax',
        analysis: 'Pesan sudah dimodifikasi memakai spintax berat. Silakan cek preview dan pastikan maknanya tetap sesuai.',
        improved_message: nextMessage,
      });
      if ($('broadcastAIStatus')) {
        $('broadcastAIStatus').textContent = 'Spintax AI selesai dibuat.';
        $('broadcastAIStatus').style.color = '#22c55e';
      }
      showToast('Spintax AI berhasil dibuat', 'success');
      return;
    }

    renderBroadcastAIResult(data);
    if ($('broadcastAIStatus')) {
      $('broadcastAIStatus').textContent = 'Analisa pesan selesai.';
      $('broadcastAIStatus').style.color = '#22c55e';
    }
    showToast('Analisa pesan selesai', 'success');
  } catch (e) {
    const status = $('broadcastAIStatus');
    if (status) {
      status.textContent = e.message;
      status.style.color = '#ef4444';
    }
    showToast(e.message, 'error');
  } finally {
    setBroadcastAIButtonsLoading(false);
  }
}

function analyzeBroadcastMessage() {
  runBroadcastAIAssistant('analyze');
}

function generateBroadcastSpintax() {
  runBroadcastAIAssistant('spintax');
}

function saveBroadcastDraft() {
  try {
    const payload = {
      account_id: getSelectedBroadcastAccountID(),
      group_name: $('groupSelect')?.value || '',
      numbers: $('numbers')?.value || '',
      title: $('scheduleName')?.value || '',
      message: $('message')?.value || '',
      template_name: $('templateName')?.value || '',
      use_spintax: $('useSpintax')?.checked || false,
      attach_image: $('attachImage')?.checked || false,
      schedule_enabled: $('broadcastScheduleToggle')?.checked || false,
      schedule_run_at: $('scheduleRunAt')?.value || '',
      images: imageUploads,
      timing: getBroadcastTimingConfig(),
    };
    localStorage.setItem('instablast_broadcast_draft', JSON.stringify(payload));
    showToast('Draf blast disimpan', 'success');
  } catch (e) {
    showToast('Gagal menyimpan draf: ' + e.message, 'error');
  }
}

function restoreBroadcastDraft() {
  let raw = '';
  try {
    raw = localStorage.getItem('instablast_broadcast_draft') || '';
  } catch (_) {
    return;
  }
  if (!raw) return;
  try {
    const draft = JSON.parse(raw);
    if ($('broadcastAccountSelect') && draft.account_id) $('broadcastAccountSelect').value = draft.account_id;
    if ($('groupSelect') && draft.group_name) $('groupSelect').value = draft.group_name;
    if ($('numbers') && draft.numbers && !$('numbers').value.trim()) $('numbers').value = draft.numbers;
    if ($('scheduleName') && draft.title && !$('scheduleName').value.trim()) $('scheduleName').value = draft.title;
    if ($('message') && draft.message && !$('message').value.trim()) $('message').value = draft.message;
    if ($('templateName') && draft.template_name && !$('templateName').value.trim()) $('templateName').value = draft.template_name;
    if ($('useSpintax')) $('useSpintax').checked = !!draft.use_spintax;
    if ($('attachImage')) $('attachImage').checked = !!draft.attach_image;
    if ($('broadcastScheduleToggle')) $('broadcastScheduleToggle').checked = !!draft.schedule_enabled;
    if ($('scheduleRunAt') && draft.schedule_run_at && !$('scheduleRunAt').value) $('scheduleRunAt').value = draft.schedule_run_at;
    if (draft.timing) {
      if ($('broadcastDelay')) $('broadcastDelay').value = draft.timing.delay || 3;
      if ($('broadcastRandomDelay')) $('broadcastRandomDelay').checked = !!draft.timing.randomDelay;
      if ($('broadcastDelayMin')) $('broadcastDelayMin').value = draft.timing.delayMin || 2;
      if ($('broadcastDelayMax')) $('broadcastDelayMax').value = draft.timing.delayMax || 5;
      if ($('broadcastBurstEvery')) $('broadcastBurstEvery').value = draft.timing.burstEvery || 0;
      if ($('broadcastBurstPauseSec')) $('broadcastBurstPauseSec').value = draft.timing.burstPause || 0;
      if ($('broadcastBreakEnabled')) $('broadcastBreakEnabled').checked = (draft.timing.burstEvery || 0) > 0 && (draft.timing.burstPause || 0) > 0;
    }
    imageUploads = Array.isArray(draft.images) ? draft.images : [];
    if ($('attachImage')) $('imageRow').style.display = $('attachImage').checked ? 'block' : 'none';
    if ($('broadcastScheduleBox')) $('broadcastScheduleBox').style.display = $('broadcastScheduleToggle')?.checked ? 'block' : 'none';
    if ($('broadcastRandomDelayRow')) $('broadcastRandomDelayRow').style.display = $('broadcastRandomDelay')?.checked ? 'block' : 'none';
    renderImagePreview('imagePreview', imageUploads);
    renderBroadcastPreview();
  } catch (_) {}
}

function submitBroadcastComposer() {
  if ($('broadcastScheduleToggle')?.checked) {
    scheduleBroadcast();
    return;
  }
  startBroadcast();
}

async function startBroadcast() {
  syncSelectedContactGroupToNumbers();
  const numbers = $('numbers').value;
  const message = $('message').value;
  const useSpintax = $('useSpintax').checked;
  const targetAccountID = getSelectedBroadcastAccountID();
  const selectedGroupID = $('groupSelect')?.value || '';
  const selectedGroupData = getSelectedContactGroup();
  const campaignName = $('scheduleName')?.value?.trim() || '';

  if (!selectedGroupID) { showToast('Pilih grup kontak dari menu Kontak terlebih dahulu'); return; }
  if (!campaignName) { showToast('Isi Judul Blast sebagai penanda broadcast terlebih dahulu'); return; }
  if (!numbers.trim()) { showToast('Grup kontak belum berisi nomor tujuan'); return; }
  if (!message.trim()) { showToast('Masukkan pesan!'); return; }
  if (!targetAccountID) { showToast('Pilih akun WhatsApp untuk broadcast!'); return; }

  const timing = getBroadcastTimingConfig();

  try {
    const body = withAccountBody({
      name: campaignName,
      contact_group_id: selectedGroupID,
      contact_rows: selectedGroupData?.contacts || [],
      numbers, message, use_spintax: useSpintax,
      delay_seconds: timing.delay,
      random_delay: timing.randomDelay,
      delay_min: timing.delayMin,
      delay_max: timing.delayMax,
      burst_every: timing.burstEvery,
      burst_pause: timing.burstPause,
    }, targetAccountID);

    if (imageUploads.length) {
      body.images = imageUploads.map(item => ({ data: item.data, mime: item.mime, name: item.name }));
    }

    const data = await api('/broadcast/start', { method: 'POST', body: JSON.stringify(body) });
    showToast(`Broadcast dimulai: ${data.total} nomor`, 'success');
    appendLog(`🚀 Broadcast dimulai: ${data.total} nomor`, 'success');

    // Start polling progress
    pollProgress();
  } catch (e) {
    showToast('Gagal mulai broadcast: ' + e.message, 'error');
  }
}

async function startBroadcastPersonal() {
  const message = $('messagePersonal').value;
  const useSpintax = $('useSpintaxPersonal')?.checked || false;
  const targetAccountID = getSelectedPersonalAccountID();

  if (!csvRawData) { showToast('Import CSV terlebih dahulu!'); return; }
  if (!message.trim()) { showToast('Masukkan template pesan!'); return; }
  if (!targetAccountID) { showToast('Pilih akun WhatsApp untuk personalisasi!'); return; }

  const delay = parseInt($('delay')?.value) || 3;
  const randomDelay = $('randomDelay')?.checked || false;
  const delayMin = parseInt($('delayMin')?.value) || 2;
  const delayMax = parseInt($('delayMax')?.value) || 5;
  const burstEvery = parseInt($('burstEvery')?.value) || 0;
  const burstPause = parseInt($('burstPauseSec')?.value) || 0;

  try {
    const body = withAccountBody({
      csv_data: csvRawData,
      message,
      use_spintax: useSpintax,
      delay_seconds: delay,
      random_delay: randomDelay,
      delay_min: delayMin,
      delay_max: delayMax,
      burst_every: burstEvery,
      burst_pause: burstPause,
    }, targetAccountID);

    if (imageUploadsPersonal.length) {
      body.images = imageUploadsPersonal.map(item => ({ data: item.data, mime: item.mime, name: item.name }));
    }

    const data = await api('/broadcast/personal', { method: 'POST', body: JSON.stringify(body) });
    showToast(`Broadcast personalisasi dimulai: ${data.total} kontak`, 'success');
    appendLog(`✨ Broadcast personalisasi dimulai: ${data.total} kontak`, 'success');
    pollProgress();
  } catch (e) {
    showToast('Gagal: ' + e.message, 'error');
  }
}

async function schedulePersonalBroadcast() {
  const targetAccountID = getSelectedPersonalAccountID();
  const message = $('messagePersonal')?.value || '';
  const runAtRaw = $('schedulePersonalRunAt')?.value || '';

  if (!targetAccountID) { showToast('Pilih akun WhatsApp untuk jadwal personalisasi!'); return; }
  if (!csvRawData) { showToast('Import CSV terlebih dahulu!'); return; }
  if (!message.trim()) { showToast('Masukkan template pesan personalisasi!'); return; }
  if (!runAtRaw) { showToast('Tentukan waktu kirim jadwal!'); return; }

  const runAt = new Date(runAtRaw);
  if (Number.isNaN(runAt.getTime())) {
    showToast('Format waktu jadwal tidak valid', 'error');
    return;
  }

  const body = withAccountBody({
    schedule_type: 'personalisasi',
    name: $('schedulePersonalName')?.value?.trim() || '',
    csv_data: csvRawData,
    message,
    use_spintax: $('useSpintaxPersonal')?.checked || false,
    delay_seconds: parseInt($('delay')?.value || '3', 10) || 3,
    random_delay: $('randomDelay')?.checked || false,
    delay_min: parseInt($('delayMin')?.value || '2', 10) || 2,
    delay_max: parseInt($('delayMax')?.value || '5', 10) || 5,
    burst_every: parseInt($('burstEvery')?.value || '0', 10) || 0,
    burst_pause: parseInt($('burstPauseSec')?.value || '0', 10) || 0,
    run_at: runAt.toISOString(),
  }, targetAccountID);

  if (imageUploadsPersonal.length) {
    body.images = imageUploadsPersonal.map(item => ({ data: item.data, mime: item.mime, name: item.name }));
  }

  try {
    const data = await api('/broadcast/schedules', {
      method: 'POST',
      body: JSON.stringify(body)
    });
    if ($('schedulePersonalStatus')) {
      $('schedulePersonalStatus').textContent = `Jadwal tersimpan untuk ${new Date(data.schedule.run_at).toLocaleString('id-ID')}`;
      $('schedulePersonalStatus').style.color = '#22c55e';
    }
    showToast('Jadwal personalisasi disimpan', 'success');
    loadPersonalSchedules();
  } catch (e) {
    if ($('schedulePersonalStatus')) {
      $('schedulePersonalStatus').textContent = 'Gagal menyimpan jadwal: ' + e.message;
      $('schedulePersonalStatus').style.color = '#ef4444';
    }
    showToast('Gagal simpan jadwal personalisasi: ' + e.message, 'error');
  }
}

async function pauseBroadcast() {
  try {
    await api('/broadcast/pause', { method: 'POST' });
    showToast('Broadcast dijeda', 'warning');
  } catch (_) {}
}

async function stopBroadcast() {
  try {
    await api('/broadcast/stop', { method: 'POST' });
    showToast('Broadcast dihentikan', 'warning');
  } catch (_) {}
}

async function resumeBroadcast() {
  try {
    await api('/broadcast/resume', { method: 'POST' });
    showToast('Broadcast dilanjutkan', 'success');
  } catch (e) {
    showToast('Gagal melanjutkan broadcast: ' + e.message, 'error');
  }
}

function pollProgress() {
  const interval = setInterval(async () => {
    try {
      const p = await api('/broadcast/progress');
      updateProgressUI(p);
      if (p.status === 'done' || p.status === 'idle') {
        clearInterval(interval);
      }
    } catch (_) {
      clearInterval(interval);
    }
  }, 1000);
}

async function scheduleBroadcast() {
  const targetAccountID = getSelectedBroadcastAccountID();
  syncSelectedContactGroupToNumbers();
  const numbers = $('numbers')?.value || '';
  const message = $('message')?.value || '';
  const runAtRaw = $('scheduleRunAt')?.value || '';
  const selectedGroupID = $('groupSelect')?.value || '';

  if (!targetAccountID) { showToast('Pilih akun WhatsApp untuk jadwal!'); return; }
  if (!selectedGroupID) { showToast('Pilih grup kontak dari menu Kontak terlebih dahulu'); return; }
  if (!numbers.trim()) { showToast('Grup kontak belum berisi nomor tujuan'); return; }
  if (!message.trim()) { showToast('Masukkan pesan broadcast!'); return; }
  if (!runAtRaw) { showToast('Tentukan waktu kirim jadwal!'); return; }

  const runAt = new Date(runAtRaw);
  if (Number.isNaN(runAt.getTime())) {
    showToast('Format waktu jadwal tidak valid', 'error');
    return;
  }

  const timing = getBroadcastTimingConfig();
  const body = withAccountBody({
    name: $('scheduleName')?.value?.trim() || '',
    numbers,
    message,
    use_spintax: $('useSpintax')?.checked || false,
    delay_seconds: timing.delay,
    random_delay: timing.randomDelay,
    delay_min: timing.delayMin,
    delay_max: timing.delayMax,
    burst_every: timing.burstEvery,
    burst_pause: timing.burstPause,
    run_at: runAt.toISOString(),
  }, targetAccountID);

  if (imageUploads.length) {
    body.images = imageUploads.map(item => ({ data: item.data, mime: item.mime, name: item.name }));
  }

  try {
    const data = await api('/broadcast/schedules', {
      method: 'POST',
      body: JSON.stringify(body)
    });
    if ($('scheduleStatus')) {
      $('scheduleStatus').textContent = `Jadwal tersimpan untuk ${new Date(data.schedule.run_at).toLocaleString('id-ID')}`;
      $('scheduleStatus').style.color = '#22c55e';
    }
    showToast('Jadwal broadcast disimpan', 'success');
    appendLog(`Jadwal broadcast disimpan untuk akun ${data.schedule.account_name || targetAccountID}`, 'success');
    loadBroadcastSchedules();
    closeBroadcastComposer();
  } catch (e) {
    if ($('scheduleStatus')) {
      $('scheduleStatus').textContent = 'Gagal menyimpan jadwal: ' + e.message;
      $('scheduleStatus').style.color = '#ef4444';
    }
    showToast('Gagal simpan jadwal: ' + e.message, 'error');
  }
}

function formatScheduleStatus(status) {
  const value = String(status || 'pending').toLowerCase();
  if (value === 'running') return 'Berjalan';
  if (value === 'done') return 'Selesai';
  if (value === 'failed') return 'Gagal';
  if (value === 'cancelled') return 'Dibatalkan';
  return 'Menunggu';
}

function normalizeHistoryRecords(data) {
  if (Array.isArray(data?.history)) return data.history;
  if (Array.isArray(data?.records)) return data.records;
  return [];
}

function summarizeHistoryRecords(records = []) {
  let totalSent = 0;
  let totalFailed = 0;
  records.forEach((item) => {
    totalSent += Number(item?.sent || 0);
    totalFailed += Number(item?.failed || 0);
  });
  const totalSessions = records.length;
  const totalAttempts = totalSent + totalFailed;
  const successRate = totalAttempts > 0 ? `${Math.round((totalSent / totalAttempts) * 100)}%` : '0%';
  return { totalSent, totalFailed, totalSessions, successRate };
}

function applyHistorySummary(summary) {
  if ($('histTotalSent')) $('histTotalSent').textContent = summary.totalSent || 0;
  if ($('histTotalFailed')) $('histTotalFailed').textContent = summary.totalFailed || 0;
  if ($('histTotalSessions')) $('histTotalSessions').textContent = summary.totalSessions || 0;
  if ($('histSuccessRate')) $('histSuccessRate').textContent = summary.successRate || '0%';
}

function applyDashboardSummary(summary, progress = latestBroadcastProgress) {
  const liveStatus = String(progress?.status || '').toLowerCase();
  const hasLiveSession = (liveStatus === 'running' || liveStatus === 'paused') && Number(progress?.total || 0) > 0;
  const totalBroadcast = (summary.totalSessions || 0) + (hasLiveSession ? 1 : 0);
  const totalSent = (summary.totalSent || 0) + (hasLiveSession ? Number(progress?.sent || 0) : 0);
  const totalFailed = (summary.totalFailed || 0) + (hasLiveSession ? Number(progress?.failed || 0) : 0);

  if ($('statTotalBC')) $('statTotalBC').textContent = totalBroadcast;
  if ($('statSent')) $('statSent').textContent = totalSent;
  if ($('statFailed')) $('statFailed').textContent = totalFailed;
}

async function refreshDashboardSummary() {
  try {
    const data = await api('/history');
    const records = normalizeHistoryRecords(data);
    const summary = summarizeHistoryRecords(records);
    applyDashboardSummary(summary);
    return summary;
  } catch (_) {
    return null;
  }
}

function updateBroadcastQuickStats() {
  const activeWrap = $('progressWrap');
  if (activeWrap) {
    activeWrap.classList.toggle('is-idle', ($('progressLabel')?.textContent || '0/0') === '0/0');
  }
}

function renderBroadcastCampaignTable() {
  const tbody = $('broadcastCampaignTableBody');
  if (!tbody) return;
  const term = ($('broadcastSearchInput')?.value || '').trim().toLowerCase();
  const rows = (broadcastSchedulesCache || []).filter(item => {
    if (!term) return true;
    const hay = [
      item.name || '',
      item.account_name || '',
      item.account_id || '',
      formatScheduleStatus(item.status),
    ].join(' ').toLowerCase();
    return hay.includes(term);
  });

  if (!rows.length) {
    tbody.innerHTML = '<tr><td colspan="7" class="empty-cell">Belum ada data blast yang cocok.</td></tr>';
    return;
  }

  tbody.innerHTML = rows.map(item => `
    <tr>
      <td>
        <div class="blast-title-cell">${escapeHtml(item.name || `Jadwal #${item.id}`)}</div>
        <div class="blast-sub-cell">${escapeHtml(item.schedule_type || 'broadcast')}</div>
      </td>
      <td>${escapeHtml(item.account_name || item.account_id || '-')}</td>
      <td>${item.run_at ? new Date(item.run_at).toLocaleString('id-ID') : '-'}</td>
      <td><span class="feature-badge blast-status-badge">${escapeHtml(formatScheduleStatus(item.status))}</span></td>
      <td style="text-align:center;">${item.total || 0}</td>
      <td>${item.sent || 0} sukses / ${item.failed || 0} gagal</td>
      <td><button class="btn btn-danger btn-sm" onclick="deleteBroadcastSchedule(${Number(item.id)})">Hapus</button></td>
    </tr>
  `).join('');
}

async function loadBroadcastMiniHistory() {
  const wrap = $('broadcastMiniHistory');
  if (!wrap) return;
  try {
    const data = await api('/history');
    broadcastHistoryCache = normalizeHistoryRecords(data)
      .filter(item => (item.type || 'broadcast') === 'broadcast')
      .slice(0, 5);
    if (!broadcastHistoryCache.length) {
      wrap.innerHTML = '<div class="empty-state-inline">Belum ada riwayat blast.</div>';
      return;
    }
    wrap.innerHTML = broadcastHistoryCache.map(item => `
      <div class="blast-mini-history-row">
        <div>
          <div class="blast-mini-history-title">${escapeHtml(item.account || 'Broadcast')}</div>
          <div class="blast-mini-history-sub">${escapeHtml(item.message || '').slice(0, 90) || 'Tanpa preview pesan'}</div>
        </div>
        <div class="blast-mini-history-meta">
          <strong>${Number(item.total || 0)}</strong>
          <span>${escapeHtml(item.date || '-')}</span>
        </div>
      </div>
    `).join('');
  } catch (e) {
    wrap.innerHTML = `<div class="empty-state-inline">Gagal memuat riwayat: ${escapeHtml(e.message)}</div>`;
  }
}

async function loadBroadcastSchedules() {
  try {
    const data = await api('/broadcast/schedules?type=broadcast');
    broadcastSchedulesCache = data.schedules || [];
    const legacyTbody = $('scheduleTableBody');
    if (legacyTbody) {
      if (!broadcastSchedulesCache.length) {
        legacyTbody.innerHTML = '<tr><td colspan="7" class="empty-cell">Belum ada jadwal broadcast</td></tr>';
      } else {
        legacyTbody.innerHTML = broadcastSchedulesCache.map(item => `
          <tr>
            <td>${escapeHtml(item.name || `Jadwal #${item.id}`)}</td>
            <td>${escapeHtml(item.account_name || item.account_id || '-')}</td>
            <td>${item.run_at ? new Date(item.run_at).toLocaleString('id-ID') : '-'}</td>
            <td><span class="feature-badge" style="margin:0;">${escapeHtml(formatScheduleStatus(item.status))}</span></td>
            <td style="text-align:center;">${item.total || 0}</td>
            <td>${item.sent || 0} sukses / ${item.failed || 0} gagal</td>
            <td><button class="btn btn-danger btn-sm" onclick="deleteBroadcastSchedule(${Number(item.id)})">Hapus</button></td>
          </tr>
        `).join('');
      }
    }
    renderBroadcastCampaignTable();
    loadBroadcastMiniHistory();
  } catch (e) {
    const tbody = $('broadcastCampaignTableBody');
    if (tbody) {
      tbody.innerHTML = `<tr><td colspan="7" class="empty-cell">Gagal memuat jadwal: ${escapeHtml(e.message)}</td></tr>`;
    }
    const legacyTbody = $('scheduleTableBody');
    if (legacyTbody) {
      legacyTbody.innerHTML = `<tr><td colspan="7" class="empty-cell">Gagal memuat jadwal: ${escapeHtml(e.message)}</td></tr>`;
    }
  }
}

async function deleteBroadcastSchedule(id) {
  if (!id) return;
  if (!confirm('Hapus jadwal broadcast ini?')) return;
  try {
    await api('/broadcast/schedules/' + encodeURIComponent(String(id)), { method: 'DELETE' });
    showToast('Jadwal broadcast dihapus', 'success');
    loadBroadcastSchedules();
  } catch (e) {
    showToast('Gagal hapus jadwal: ' + e.message, 'error');
  }
}

async function loadPersonalSchedules() {
  const tbody = $('schedulePersonalTableBody');
  if (!tbody) return;
  try {
    const data = await api('/broadcast/schedules?type=personalisasi');
    const rows = data.schedules || [];
    if (!rows.length) {
      tbody.innerHTML = '<tr><td colspan="7" class="empty-cell">Belum ada jadwal personalisasi</td></tr>';
      return;
    }
    tbody.innerHTML = rows.map(item => `
      <tr>
        <td>${escapeHtml(item.name || `Jadwal #${item.id}`)}</td>
        <td>${escapeHtml(item.account_name || item.account_id || '-')}</td>
        <td>${item.run_at ? new Date(item.run_at).toLocaleString('id-ID') : '-'}</td>
        <td><span class="feature-badge" style="margin:0;">${escapeHtml(formatScheduleStatus(item.status))}</span></td>
        <td style="text-align:center;">${item.total || 0}</td>
        <td>${item.sent || 0} sukses / ${item.failed || 0} gagal</td>
        <td><button class="btn btn-danger btn-sm" onclick="deletePersonalSchedule(${Number(item.id)})">Hapus</button></td>
      </tr>
    `).join('');
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="7" class="empty-cell">Gagal memuat jadwal: ${escapeHtml(e.message)}</td></tr>`;
  }
}

async function deletePersonalSchedule(id) {
  if (!id) return;
  if (!confirm('Hapus jadwal personalisasi ini?')) return;
  try {
    await api('/broadcast/schedules/' + encodeURIComponent(String(id)), { method: 'DELETE' });
    showToast('Jadwal personalisasi dihapus', 'success');
    loadPersonalSchedules();
  } catch (e) {
    showToast('Gagal hapus jadwal: ' + e.message, 'error');
  }
}

function updateProgressUILegacy(p) {
  latestBroadcastProgress = p || null;
  // Main broadcast progress
  const bar = $('progressBar');
  const label = $('progressLabel');
  const barP = $('progressBarPersonal');
  const labelP = $('progressLabelPersonal');
  const dashCard = $('dashProgressCard');
  const dashBar = $('dashProgressBar');
  const dashLabel = $('dashProgressLabel');
  const dashStatus = $('dashProgressStatus');

  const pct = p.total > 0 ? Math.round((p.current / p.total) * 100) : 0;

  if (bar) bar.style.width = pct + '%';
  if (label) label.textContent = `${p.current || 0}/${p.total || 0} (${p.sent || 0}✅ ${p.failed || 0}❌)`;
  if (barP) barP.style.width = pct + '%';
  if (labelP) labelP.textContent = `${p.current || 0}/${p.total || 0}`;

  // Dashboard cards
  if ($('statTotalBC')) $('statTotalBC').textContent = p.total || 0;
  if ($('statSent')) $('statSent').textContent = p.sent || 0;
  if ($('statFailed')) $('statFailed').textContent = p.failed || 0;

  if (dashCard && p.status === 'running') {
    dashCard.style.display = 'block';
    if (dashBar) dashBar.style.width = pct + '%';
    if (dashLabel) dashLabel.textContent = `${p.current}/${p.total}`;
    if (dashStatus) dashStatus.textContent = `Mengirim ke ${p.current_num || '...'}`;
  } else if (dashCard && p.status === 'done') {
    if (dashStatus) dashStatus.textContent = `✅ Selesai: ${p.sent} terkirim, ${p.failed} gagal`;
  }
}

function updateProgressUI(p) {
  p = p || { status: 'idle', current: 0, total: 0, sent: 0, failed: 0 };
  latestBroadcastProgress = p || null;
  const progressStatus = String(p.status || 'idle').toLowerCase();

  const bar = $('progressBar');
  const label = $('progressLabel');
  const currentText = $('progressCurrentText');
  const sentCount = $('progressSentCount');
  const failedCount = $('progressFailedCount');
  const barP = $('progressBarPersonal');
  const labelP = $('progressLabelPersonal');
  const dashCard = $('dashProgressCard');
  const dashBar = $('dashProgressBar');
  const dashLabel = $('dashProgressLabel');
  const dashStatus = $('dashProgressStatus');
  const pct = p.total > 0 ? Math.round((p.current / p.total) * 100) : 0;

  if (bar) bar.style.width = pct + '%';
  if (label) label.textContent = `${p.current || 0}/${p.total || 0}`;
  if (sentCount) sentCount.textContent = `${p.sent || 0} terkirim`;
  if (failedCount) failedCount.textContent = `${p.failed || 0} gagal`;
  if (currentText) {
    const campaignLabel = String(p.campaign_name || '').trim();
    const prefix = campaignLabel ? `${campaignLabel}: ` : '';
    if (p.status === 'running') {
      currentText.textContent = `${prefix}sedang mengirim ke ${p.current_num || 'nomor berikutnya'}`;
    } else if (p.status === 'paused') {
      currentText.textContent = `${prefix}pengiriman sedang pause.`;
    } else if (p.status === 'done') {
      currentText.textContent = `${prefix}selesai: ${p.sent || 0} terkirim, ${p.failed || 0} gagal.`;
    } else if (p.status === 'failed') {
      currentText.textContent = `${prefix}pengiriman berhenti karena terjadi error.`;
    } else {
      currentText.textContent = 'Belum ada proses berjalan.';
    }
  }
  if (barP) barP.style.width = pct + '%';
  if (labelP) labelP.textContent = `${p.current || 0}/${p.total || 0}`;

  if (progressStatus === 'running' || progressStatus === 'paused') {
    if ($('statTotalBC')) $('statTotalBC').textContent = p.total || 0;
    if ($('statSent')) $('statSent').textContent = p.sent || 0;
    if ($('statFailed')) $('statFailed').textContent = p.failed || 0;
  } else {
    refreshDashboardSummary();
  }

  if (dashCard && p.status === 'running') {
    dashCard.style.display = 'block';
    if (dashBar) dashBar.style.width = pct + '%';
    if (dashLabel) dashLabel.textContent = `${p.current}/${p.total}`;
    if (dashStatus) dashStatus.textContent = `Mengirim ke ${p.current_num || '...'}`;
  } else if (dashCard && p.status === 'done') {
    if (dashStatus) dashStatus.textContent = `Selesai: ${p.sent || 0} terkirim, ${p.failed || 0} gagal`;
  }
}

function formatDuration(startedAt) {
  if (!startedAt) return '-';
  const start = new Date(startedAt);
  const dur = Math.round((Date.now() - start.getTime()) / 1000);
  if (dur < 60) return dur + 's';
  if (dur < 3600) return Math.floor(dur/60) + 'm ' + (dur%60) + 's';
  return Math.floor(dur/3600) + 'h ' + Math.floor((dur%3600)/60) + 'm';
}

function formatQueueTypeLabel(type) {
  return type === 'personalisasi' ? 'Personalisasi' : 'Broadcast';
}

function formatQueueStatusLabel(status) {
  const value = String(status || '').toLowerCase();
  if (value === 'running') return 'Berjalan';
  if (value === 'paused') return 'Pause';
  if (value === 'pending') return 'Menunggu';
  if (value === 'failed') return 'Gagal';
  if (value === 'done') return 'Selesai';
  if (value === 'cancelled') return 'Dibatalkan';
  return value ? value.charAt(0).toUpperCase() + value.slice(1) : '-';
}

function renderHistoryQueue() {
  const tbody = $('historyQueueTableBody');
  if (!tbody) return;

  const progress = latestBroadcastProgress || { status: 'idle' };
  const scheduleRows = Array.isArray(historyQueueSchedulesCache) ? [...historyQueueSchedulesCache] : [];
  scheduleRows.sort((a, b) => new Date(a.run_at || 0).getTime() - new Date(b.run_at || 0).getTime());

  const runningCount = progress.status === 'running' || progress.status === 'paused' ? 1 : 0;
  const pendingCount = scheduleRows.filter(item => String(item.status || '').toLowerCase() === 'pending').length;
  const broadcastCount = scheduleRows.filter(item => (item.schedule_type || 'broadcast') === 'broadcast').length;
  const personalCount = scheduleRows.filter(item => (item.schedule_type || '') === 'personalisasi').length;

  if ($('queueRunningCount')) $('queueRunningCount').textContent = String(runningCount);
  if ($('queuePendingCount')) $('queuePendingCount').textContent = String(pendingCount);
  if ($('queueBroadcastCount')) $('queueBroadcastCount').textContent = String(broadcastCount);
  if ($('queuePersonalCount')) $('queuePersonalCount').textContent = String(personalCount);

  const rows = [];
  if (runningCount) {
    const actionButton = progress.status === 'paused'
      ? `<button class="btn btn-primary btn-sm" onclick="resumeBroadcast()">Lanjutkan</button>`
      : `<button class="btn btn-warning btn-sm" onclick="pauseBroadcast()">Pause</button>`;
    const activeCampaignName = String(progress.campaign_name || '').trim() || 'Blast sedang berjalan';
    rows.push(`
      <tr>
        <td><span class="feature-badge blast-status-badge" style="margin:0;">${escapeHtml(formatQueueStatusLabel(progress.status))}</span></td>
        <td><span class="feature-badge blast-status-badge" style="margin:0;">Aktif</span></td>
        <td>
          <strong>${escapeHtml(activeCampaignName)}</strong>
          <div class="blast-sub-cell">${escapeHtml(progress.current_num || 'Sedang memproses nomor berikutnya')}</div>
        </td>
        <td>${escapeHtml(getActiveAccount()?.name || '-')}</td>
        <td>${escapeHtml(`${progress.current || 0}/${progress.total || 0} nomor`)}</td>
        <td style="text-align:center;">${progress.total || 0}</td>
        <td>${progress.sent || 0} sukses / ${progress.failed || 0} gagal</td>
        <td>
          <div class="btn-row">
            ${actionButton}
            <button class="btn btn-danger btn-sm" onclick="stopBroadcast()">Stop</button>
          </div>
        </td>
      </tr>
    `);
  }

  scheduleRows.forEach(item => {
    rows.push(`
      <tr>
        <td><span class="feature-badge blast-status-badge" style="margin:0;">${escapeHtml(formatQueueStatusLabel(item.status))}</span></td>
        <td><span class="feature-badge blast-status-badge" style="margin:0;">${escapeHtml(formatQueueTypeLabel(item.schedule_type))}</span></td>
        <td>
          <strong>${escapeHtml(item.name || `Jadwal #${item.id}`)}</strong>
          <div class="blast-sub-cell">${escapeHtml((item.message || '').slice(0, 70) || 'Tanpa preview pesan')}</div>
        </td>
        <td>${escapeHtml(item.account_name || item.account_id || '-')}</td>
        <td>${item.run_at ? new Date(item.run_at).toLocaleString('id-ID') : '-'}</td>
        <td style="text-align:center;">${item.total || 0}</td>
        <td>${item.sent || 0} sukses / ${item.failed || 0} gagal</td>
        <td><button class="btn btn-danger btn-sm" onclick="deleteQueuedSchedule(${Number(item.id)})">Hapus</button></td>
      </tr>
    `);
  });

  if (!rows.length) {
    tbody.innerHTML = '<tr><td colspan="8" class="empty-cell">Tidak ada blast yang sedang berjalan atau terjadwal.</td></tr>';
    return;
  }

  tbody.innerHTML = rows.join('');
}

async function loadHistoryQueue() {
  const tbody = $('historyQueueTableBody');
  if (tbody) {
    tbody.innerHTML = '<tr><td colspan="8" class="empty-cell">Memuat queue broadcast...</td></tr>';
  }
  try {
    const [progress, broadcastData, personalData] = await Promise.all([
      api('/broadcast/progress').catch(() => ({ status: 'idle' })),
      api('/broadcast/schedules?type=broadcast').catch(() => ({ schedules: [] })),
      api('/broadcast/schedules?type=personalisasi').catch(() => ({ schedules: [] })),
    ]);
    latestBroadcastProgress = progress || { status: 'idle' };
    historyQueueSchedulesCache = [
      ...(broadcastData.schedules || []),
      ...(personalData.schedules || []),
    ].filter(item => {
      const status = String(item.status || '').toLowerCase();
      return status !== 'done' && status !== 'cancelled';
    });
    renderHistoryQueue();
  } catch (e) {
    if (tbody) {
      tbody.innerHTML = `<tr><td colspan="8" class="empty-cell">Gagal memuat queue: ${escapeHtml(e.message)}</td></tr>`;
    }
  }
}

async function deleteQueuedSchedule(id) {
  if (!id) return;
  if (!confirm('Hapus jadwal ini dari queue?')) return;
  try {
    await api('/broadcast/schedules/' + encodeURIComponent(String(id)), { method: 'DELETE' });
    showToast('Jadwal dihapus dari queue', 'success');
    loadHistoryQueue();
    loadBroadcastSchedules();
    loadPersonalSchedules();
  } catch (e) {
    showToast('Gagal hapus jadwal: ' + e.message, 'error');
  }
}

// ===== CSV Import =====
function handleCSVImport(event) {
  const file = event.target.files[0];
  if (!file) return;
  const reader = new FileReader();
  reader.onload = (e) => {
    csvRawData = e.target.result;
    parseCSVPreview(csvRawData);
    showToast('CSV berhasil diimport', 'success');
  };
  reader.readAsText(file);
}

function parseCSVPreview(raw) {
  const lines = raw.split(/\r?\n/).filter(Boolean);
  if (lines.length < 2) {
    $('csvImportStatus').textContent = '❌ CSV kosong atau tidak valid';
    return;
  }

  const headers = lines[0].split(/[,;\t]/).map(h => h.trim().toLowerCase());
  csvColumns = headers;

  // Extract numbers
  const nomorIdx = headers.indexOf('nomor');
  if (nomorIdx < 0) {
    $('csvImportStatus').textContent = '❌ Kolom "nomor" tidak ditemukan';
    return;
  }

  const numbers = [];
  for (let i = 1; i < lines.length; i++) {
    const cols = lines[i].split(/[,;\t]/);
    if (cols[nomorIdx]) numbers.push(cols[nomorIdx].trim());
  }

  $('numbersPersonal').value = numbers.join('\n');
  $('csvImportStatus').textContent = `✅ ${numbers.length} kontak dari ${headers.length} kolom: ${headers.join(', ')}`;
  $('csvImportStatus').style.color = '#22c55e';

  // Update placeholders
  const phEl = $('personalPlaceholders');
  if (phEl) {
    phEl.innerHTML = headers.map(h => `<code>{${h}}</code> - ${h}`).join('<br>');
  }

  appendLog(`CSV imported: ${numbers.length} kontak, kolom: ${headers.join(', ')}`, 'success');
}

function importCSV() {
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = '.csv,.txt';
  input.onchange = (e) => {
    const file = e.target.files[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = (ev) => {
      const text = ev.target.result;
      const nums = [];
      text.split(/\r?\n/).filter(Boolean).forEach(line => {
        const col = line.split(/[;,\t]/)[0]?.trim();
        if (col && /\d{5,}/.test(col)) nums.push(col);
      });
      $('numbers').value = nums.join('\n');
      renderBroadcastPreview();
      appendLog(`✅ ${nums.length} nomor diimport dari CSV`, 'success');
      showToast(`${nums.length} nomor diimport`, 'success');
    };
    reader.readAsText(file);
  };
  input.click();
}

function downloadSampleCSV() {
  const csv = 'nomor,nama,jenis_kendaraan,plat_nomor,alamat\n628123456789,Budi,Mobil,B 1234 CD,Jakarta\n628987654321,Ani,Motor,D 5678 EF,Bandung\n';
  const blob = new Blob([csv], { type: 'text/csv' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'sample_personalisasi.csv';
  a.click();
}

// ===== Image Upload =====
function handleImageUpload(event) {
  const file = event.target.files[0];
  if (!file) return;
  const reader = new FileReader();
  reader.onload = (e) => {
    const dataUrl = e.target.result;
    imageBase64 = dataUrl.split(',')[1];
    imageMime = file.type;
    $('imagePreview').innerHTML = `<span style="color:#22c55e;">✅ ${file.name}</span>`;
    appendLog('Gambar dimuat: ' + file.name, 'success');
  };
  reader.readAsDataURL(file);
}

function handleImageUploadPersonal(event) {
  const file = event.target.files[0];
  if (!file) return;
  const reader = new FileReader();
  reader.onload = (e) => {
    const dataUrl = e.target.result;
    imageBase64Personal = dataUrl.split(',')[1];
    imageMimePersonal = file.type;
    $('imagePreviewPersonal').innerHTML = `<span style="color:#22c55e;">✅ ${file.name}</span>`;
  };
  reader.readAsDataURL(file);
}

async function readMediaFiles(fileList) {
  const files = Array.from(fileList || []).filter(file => file && file.type);
  const items = await Promise.all(files.map(file => new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = (e) => {
      const dataUrl = String(e.target.result || '');
      const parts = dataUrl.split(',');
      if (parts.length < 2) {
        reject(new Error('Gagal membaca media'));
        return;
      }
      resolve({
        name: file.name,
        mime: file.type || 'application/octet-stream',
        data: parts[1],
      });
    };
    reader.onerror = () => reject(new Error('Gagal membaca media'));
    reader.readAsDataURL(file);
  })));
  return items;
}

function getMediaKind(mime) {
  const value = String(mime || '').toLowerCase();
  if (value.startsWith('image/')) return 'Gambar';
  if (value.startsWith('video/')) return 'Video';
  if (value.startsWith('audio/')) return 'Audio';
  if (value.includes('pdf')) return 'PDF';
  return 'Dokumen';
}

function formatMediaLabel(item) {
  const name = item?.name || '';
  return name ? `${getMediaKind(item?.mime)}: ${name}` : getMediaKind(item?.mime);
}

function renderImagePreview(previewID, items) {
  const preview = $(previewID);
  if (!preview) return;
  if (!items.length) {
    preview.innerHTML = '';
    return;
  }
  const summary = items.length === 1
    ? `1 ${getMediaKind(items[0].mime).toLowerCase()} siap dikirim dengan caption`
    : `${items.length} media siap dikirim, lalu pesan teks dikirim setelah semua media`;
  preview.innerHTML = `
    <div style="color:#22c55e; font-weight:600;">${escapeHtml(summary)}</div>
    <div>${items.map(item => `<div>${escapeHtml(formatMediaLabel(item))}</div>`).join('')}</div>
  `;
}

async function handleImageUpload(event) {
  try {
    imageUploads = await readMediaFiles(event.target.files);
    renderImagePreview('imagePreview', imageUploads);
    renderBroadcastPreview();
    if (imageUploads.length) {
      appendLog(`${imageUploads.length} media dimuat untuk broadcast`, 'success');
    }
  } catch (e) {
    imageUploads = [];
    renderImagePreview('imagePreview', imageUploads);
    renderBroadcastPreview();
    showToast(e.message, 'error');
  }
}

async function handleImageUploadPersonal(event) {
  try {
    imageUploadsPersonal = await readMediaFiles(event.target.files);
    renderImagePreview('imagePreviewPersonal', imageUploadsPersonal);
    if (imageUploadsPersonal.length) {
      appendLog(`${imageUploadsPersonal.length} media dimuat untuk personalisasi`, 'success');
    }
  } catch (e) {
    imageUploadsPersonal = [];
    renderImagePreview('imagePreviewPersonal', imageUploadsPersonal);
    showToast(e.message, 'error');
  }
}

// ===== Field Helpers =====
function clearField(id) { const el = $(id); if (el) el.value = ''; }

function escapeHtml(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

function getCurrentUnsubscribeSettings() {
  const settings = { ...unsubscribeSettingsState };
  if ($('unsubscribeEnabled')) settings.enabled = !!$('unsubscribeEnabled').checked;
  if ($('unsubscribeKeyword')) settings.keyword = String($('unsubscribeKeyword').value || settings.keyword || 'STOP').trim().toUpperCase() || 'STOP';
  if ($('unsubscribeInstruction')) settings.instruction = String($('unsubscribeInstruction').value || settings.instruction || '').trim();
  if ($('unsubscribeAutoReply')) settings.auto_reply = String($('unsubscribeAutoReply').value || settings.auto_reply || '').trim();
  return settings;
}

function switchContactsView(view = 'groups') {
  contactsView = view === 'unsubscribe' ? 'unsubscribe' : 'groups';
  $('contactsGroupsView')?.toggleAttribute('hidden', contactsView !== 'groups');
  $('contactsUnsubscribeView')?.toggleAttribute('hidden', contactsView !== 'unsubscribe');
  $('contactsViewGroupsBtn')?.classList.toggle('btn-primary', contactsView === 'groups');
  $('contactsViewGroupsBtn')?.classList.toggle('btn-secondary', contactsView !== 'groups');
  $('contactsViewUnsubscribeBtn')?.classList.toggle('btn-primary', contactsView === 'unsubscribe');
  $('contactsViewUnsubscribeBtn')?.classList.toggle('btn-secondary', contactsView !== 'unsubscribe');
}

async function loadUnsubscribeSettings() {
  try {
    const data = await api('/contacts/unsubscribe/settings');
    unsubscribeSettingsState = {
      enabled: !!data.enabled,
      keyword: String(data.keyword || 'STOP').trim().toUpperCase() || 'STOP',
      instruction: String(data.instruction || 'Ketik STOP untuk berhenti menerima pesan dari kami.').trim(),
      auto_reply: String(data.auto_reply || 'Baik, nomor Anda sudah kami masukkan ke daftar berhenti berlangganan. Kami tidak akan mengirimkan pesan broadcast lagi.').trim(),
    };
  } catch (_) {}
  if ($('unsubscribeEnabled')) $('unsubscribeEnabled').checked = !!unsubscribeSettingsState.enabled;
  if ($('unsubscribeKeyword')) $('unsubscribeKeyword').value = unsubscribeSettingsState.keyword || 'STOP';
  if ($('unsubscribeInstruction')) $('unsubscribeInstruction').value = unsubscribeSettingsState.instruction || '';
  if ($('unsubscribeAutoReply')) $('unsubscribeAutoReply').value = unsubscribeSettingsState.auto_reply || '';
  renderBroadcastPreview();
}

async function loadUnsubscribeList() {
  try {
    const data = await api('/contacts/unsubscribe');
    unsubscribeEntries = Array.isArray(data.items) ? data.items : [];
  } catch (_) {
    unsubscribeEntries = [];
  }
  renderUnsubscribeList();
}

async function loadUnsubscribeData() {
  await Promise.allSettled([loadUnsubscribeSettings(), loadUnsubscribeList()]);
}

function renderUnsubscribeList() {
  const wrap = $('unsubscribeListWrap');
  if (!wrap) return;
  if (!unsubscribeEntries.length) {
    wrap.innerHTML = '<div class="empty-state-inline">Belum ada pelanggan unsubscribe.</div>';
    return;
  }
  wrap.innerHTML = `
    <div class="hint">${unsubscribeEntries.length} pelanggan sudah memilih berhenti menerima pesan.</div>
    <div class="table-scroll">
      <table class="data-table">
        <thead>
          <tr>
            <th>Nomor</th>
            <th>Nama</th>
            <th>Kata Kunci</th>
            <th>Sumber</th>
            <th>Tanggal</th>
            <th>Aksi</th>
          </tr>
        </thead>
        <tbody>
          ${unsubscribeEntries.map(item => `
            <tr>
              <td>${escapeHtml(item.phone || '-')}</td>
              <td>${escapeHtml(item.name || '-')}</td>
              <td>${escapeHtml(item.keyword || 'STOP')}</td>
              <td>${escapeHtml(item.source_account_name || item.source_account_id || '-')}</td>
              <td>${item.updated_at ? new Date(item.updated_at).toLocaleString('id-ID') : '-'}</td>
              <td><button class="btn btn-danger btn-sm" type="button" onclick="deleteUnsubscribeEntry(${Number(item.id)})">Hapus</button></td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

async function saveUnsubscribeSettings() {
  const settings = getCurrentUnsubscribeSettings();
  try {
    const saved = await api('/contacts/unsubscribe/settings', {
      method: 'POST',
      body: JSON.stringify(settings),
    });
    unsubscribeSettingsState = {
      enabled: !!saved.enabled,
      keyword: String(saved.keyword || 'STOP').trim().toUpperCase() || 'STOP',
      instruction: String(saved.instruction || '').trim(),
      auto_reply: String(saved.auto_reply || '').trim(),
    };
    if ($('unsubscribeSettingsStatus')) {
      $('unsubscribeSettingsStatus').textContent = 'Pengaturan unsubscribe disimpan';
      $('unsubscribeSettingsStatus').style.color = '#16a34a';
    }
    showToast('Pengaturan unsubscribe disimpan', 'success');
    renderBroadcastPreview();
  } catch (e) {
    if ($('unsubscribeSettingsStatus')) {
      $('unsubscribeSettingsStatus').textContent = 'Gagal menyimpan pengaturan: ' + e.message;
      $('unsubscribeSettingsStatus').style.color = '#ef4444';
    }
    showToast('Gagal menyimpan unsubscribe: ' + e.message, 'error');
  }
}

async function deleteUnsubscribeEntry(id) {
  if (!id) return;
  if (!confirm('Hapus nomor ini dari daftar unsubscribe?')) return;
  try {
    await api('/contacts/unsubscribe/' + encodeURIComponent(String(id)), { method: 'DELETE' });
    showToast('Nomor dihapus dari daftar unsubscribe', 'success');
    loadUnsubscribeList();
  } catch (e) {
    showToast('Gagal hapus unsubscribe: ' + e.message, 'error');
  }
}

// ===== Contact Lists =====
function parseCSVRows(text) {
  const rows = [];
  let current = '';
  let row = [];
  let inQuotes = false;
  const source = String(text || '').replace(/\r\n/g, '\n').replace(/\r/g, '\n');
  const delimiter = (source.split('\n')[0] || '').includes(';') ? ';' : ',';
  for (let i = 0; i < source.length; i++) {
    const ch = source[i];
    const next = source[i + 1];
    if (ch === '"' && inQuotes && next === '"') {
      current += '"';
      i++;
      continue;
    }
    if (ch === '"') {
      inQuotes = !inQuotes;
      continue;
    }
    if (ch === delimiter && !inQuotes) {
      row.push(current.trim());
      current = '';
      continue;
    }
    if (ch === '\n' && !inQuotes) {
      row.push(current.trim());
      if (row.some(cell => cell !== '')) rows.push(row);
      row = [];
      current = '';
      continue;
    }
    current += ch;
  }
  row.push(current.trim());
  if (row.some(cell => cell !== '')) rows.push(row);
  return rows;
}

function findPhoneColumn(columns) {
  const accepted = ['nomor', 'phone', 'no', 'whatsapp', 'wa', 'hp', 'telepon'];
  return columns.find(col => accepted.includes(String(col || '').trim().toLowerCase())) || '';
}

function parseManualContactNumbers(raw) {
  return Array.from(new Set(
    String(raw || '')
      .replace(/\r\n/g, '\n')
      .replace(/\r/g, '\n')
      .split(/\n+/)
      .map(line => line.trim())
      .map(line => {
        const match = line.match(/\+?\d[\d\s\-().]{5,}\d|\d{6,}/);
        if (!match) return '';
        return match[0].replace(/[^\d+]/g, '');
      })
      .map(num => num.replace(/^\+/, ''))
      .filter(num => /\d{6,}/.test(num))
  ));
}

function getPreparedContactDataset() {
  const manualRaw = $('contactManualNumbers')?.value || '';
  const manualNumbers = parseManualContactNumbers(manualRaw);
  if (manualNumbers.length) {
    return {
      source: 'manual',
      columns: ['nomor'],
      rows: manualNumbers.map(number => ({ nomor: number })),
    };
  }
  if (contactCSVRows.length && contactCSVColumns.length) {
    return {
      source: 'csv',
      columns: [...contactCSVColumns],
      rows: [...contactCSVRows],
    };
  }
  return { source: '', columns: [], rows: [] };
}

function handleManualContactInput() {
  const dataset = getPreparedContactDataset();
  const manualCount = dataset.source === 'manual' ? dataset.rows.length : 0;
  if ($('contactImportStatus')) {
    if (manualCount) {
      $('contactImportStatus').textContent = `${manualCount} nomor manual siap disimpan`;
      $('contactImportStatus').style.color = '#16a34a';
    } else if (!contactCSVRows.length) {
      $('contactImportStatus').textContent = '';
    }
  }
  if (manualCount) {
    renderContactTablePreview(dataset.rows, dataset.columns);
  } else if (contactCSVRows.length) {
    renderContactTablePreview();
  } else {
    renderContactTablePreview([], []);
  }
}

async function handleContactCSVImport(event) {
  const file = event.target.files?.[0];
  if (!file) return;
  try {
    const text = await file.text();
    const rows = parseCSVRows(text);
    if (rows.length < 2) throw new Error('CSV minimal berisi header dan 1 baris kontak');
    contactCSVColumns = rows[0].map((col, index) => String(col || `kolom_${index + 1}`).trim() || `kolom_${index + 1}`);
    const phoneColumn = findPhoneColumn(contactCSVColumns);
    if (!phoneColumn) throw new Error('Tambahkan kolom nomor/phone/wa/whatsapp/no pada CSV');
    contactCSVRows = rows.slice(1).map(values => {
      const item = {};
      contactCSVColumns.forEach((col, index) => {
        item[col] = values[index] || '';
      });
      return item;
    }).filter(row => String(row[phoneColumn] || '').trim() !== '');
    if (!contactCSVRows.length) throw new Error('Tidak ada nomor valid di file CSV');
    if ($('contactImportStatus')) {
      $('contactImportStatus').textContent = `${contactCSVRows.length} kontak siap disimpan dari ${file.name}`;
      $('contactImportStatus').style.color = '#16a34a';
    }
    renderContactTablePreview();
  } catch (e) {
    contactCSVColumns = [];
    contactCSVRows = [];
    renderContactTablePreview();
    showToast(e.message, 'error');
  } finally {
    event.target.value = '';
  }
}

function renderContactTablePreview(rows = contactCSVRows, columns = contactCSVColumns) {
  const wrap = $('contactTablePreview');
  if (!wrap) return;
  if (!rows.length || !columns.length) {
    wrap.innerHTML = '<div class="empty-state-inline">Import CSV atau paste nomor untuk melihat preview kontak.</div>';
    return;
  }
  const previewRows = rows.slice(0, 50);
  wrap.innerHTML = `
    <div class="hint">${rows.length} kontak, ${columns.length} kolom variabel. Preview menampilkan maksimal 50 baris.</div>
    <div class="table-scroll">
      <table class="data-table">
        <thead><tr>${columns.map(col => `<th>${escapeHtml(col)}</th>`).join('')}</tr></thead>
        <tbody>
          ${previewRows.map(row => `<tr>${columns.map(col => `<td>${escapeHtml(row[col] || '')}</td>`).join('')}</tr>`).join('')}
        </tbody>
      </table>
    </div>
  `;
}

async function saveContactList() {
  const name = $('contactListName')?.value?.trim() || '';
  const dataset = getPreparedContactDataset();
  if (!name) { showToast('Isi nama grup kontak dulu'); return; }
  if (!dataset.rows.length) { showToast('Import CSV/TXT atau paste nomor HP dulu'); return; }
  try {
    await api('/contacts/lists', {
      method: 'POST',
      body: JSON.stringify({ name, columns: dataset.columns, contacts: dataset.rows })
    });
    showToast(`Grup kontak "${name}" disimpan`, 'success');
    clearContactImport(false);
    loadContactLists();
  } catch (e) {
    showToast('Gagal menyimpan kontak: ' + e.message, 'error');
  }
}

function clearContactImport(clearName = true) {
  contactCSVColumns = [];
  contactCSVRows = [];
  if (clearName && $('contactListName')) $('contactListName').value = '';
  if ($('contactManualNumbers')) $('contactManualNumbers').value = '';
  if ($('contactImportStatus')) $('contactImportStatus').textContent = '';
  renderContactTablePreview();
}

async function loadContactLists() {
  try {
    const data = await api('/contacts/lists');
    contactLists = data.groups || [];
    renderContactListCards();
    renderContactGroupSelect();
  } catch (_) {}
}

async function loadGroups() {
  return loadContactLists();
}

function renderContactListCards() {
  const box = $('contactListCards');
  if (!box) return;
  if (!contactLists.length) {
    box.innerHTML = '<div class="empty-state-inline">Belum ada grup kontak. Import CSV atau paste nomor di kiri.</div>';
    return;
  }
  box.innerHTML = contactLists.map(group => `
    <div class="contact-list-card">
      <div>
        <strong>${escapeHtml(group.name)}</strong>
        <div class="hint">${Number(group.count || 0)} kontak | ${(group.columns || []).length} variabel</div>
      </div>
      <div class="btn-row">
        <button class="btn btn-secondary btn-sm" type="button" onclick="previewSavedContactList(${Number(group.id)})">Lihat</button>
        <button class="btn btn-danger btn-sm" type="button" onclick="deleteContactList(${Number(group.id)})">Hapus</button>
      </div>
    </div>
  `).join('');
}

function previewSavedContactList(id) {
  const group = contactLists.find(item => Number(item.id) === Number(id));
  if (!group) return;
  renderContactTablePreview(group.contacts || [], group.columns || []);
  if ($('contactListName')) $('contactListName').value = group.name || '';
}

async function deleteContactList(id) {
  if (!id) return;
  try {
    await api('/contacts/lists/' + encodeURIComponent(id), { method: 'DELETE' });
    showToast('Grup kontak dihapus', 'success');
    loadContactLists();
  } catch (e) {
    showToast('Gagal hapus grup kontak: ' + e.message, 'error');
  }
}

function renderContactGroupSelect() {
  const sel = $('groupSelect');
  if (!sel) return;
  const current = sel.value;
  sel.innerHTML = '<option value="">Pilih grup kontak...</option>';
  contactLists.forEach(group => {
    const o = document.createElement('option');
    o.value = String(group.id);
    o.textContent = `${group.name} (${Number(group.count || 0)} kontak)`;
    sel.appendChild(o);
  });
  if (current) sel.value = current;
  sel.onchange = syncSelectedContactGroupToNumbers;
  syncSelectedContactGroupToNumbers();
}

function syncSelectedContactGroupToNumbers() {
  const group = getSelectedContactGroup();
  if ($('numbers')) $('numbers').value = group?.numbers || '';
  renderBroadcastVariablePicker();
  renderBroadcastPreview();
}

// ===== File Manager =====
async function loadMediaFiles() {
  try {
    const data = await api('/media/files');
    mediaFiles = data.files || [];
    renderMediaFileList();
    renderBroadcastMediaPicker();
  } catch (_) {}
}

async function uploadManagedMedia(event) {
  const file = event.target.files?.[0];
  if (!file) return;
  const form = new FormData();
  form.append('file', file);
  try {
    const res = await fetch(API + '/media/files', { method: 'POST', body: form });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Gagal upload file');
    if ($('mediaStatus')) {
      $('mediaStatus').textContent = `${file.name} berhasil diupload`;
      $('mediaStatus').style.color = '#16a34a';
    }
    showToast('Media berhasil diupload', 'success');
    loadMediaFiles();
  } catch (e) {
    showToast(e.message, 'error');
  } finally {
    event.target.value = '';
  }
}

function renderMediaFileList() {
  const box = $('mediaFileList');
  if (!box) return;
  if (!mediaFiles.length) {
    box.innerHTML = '<div class="empty-state-inline">Belum ada media. Upload gambar, video, atau dokumen dulu.</div>';
    return;
  }
  box.innerHTML = mediaFiles.map(file => `
    <div class="media-card">
      <div class="media-thumb">${renderMediaThumb(file)}</div>
      <div class="media-name">${escapeHtml(file.original_name || file.name)}</div>
      <div class="hint">${escapeHtml(file.mime || 'file')} | ${formatBytes(file.size || 0)}</div>
      <div class="btn-row">
        <a class="btn btn-secondary btn-sm" href="${escapeHtml(file.url || '#')}" target="_blank" rel="noopener">Lihat</a>
        <button class="btn btn-danger btn-sm" type="button" onclick="deleteManagedMedia(${Number(file.id)})">Hapus</button>
      </div>
    </div>
  `).join('');
}

function renderMediaThumb(file) {
  const mime = String(file.mime || '');
  if (mime.startsWith('image/') && file.url) {
    return `<img src="${escapeHtml(file.url)}" alt="${escapeHtml(file.original_name || file.name)}" />`;
  }
  if (mime.startsWith('video/')) return '<span>VIDEO</span>';
  if (mime.startsWith('audio/')) return '<span>AUDIO</span>';
  if (mime.includes('pdf')) return '<span>PDF</span>';
  return '<span>FILE</span>';
}

function formatBytes(size) {
  const n = Number(size || 0);
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

async function deleteManagedMedia(id) {
  try {
    await api('/media/files/' + encodeURIComponent(id), { method: 'DELETE' });
    selectedBroadcastMediaIDs = selectedBroadcastMediaIDs.filter(item => Number(item) !== Number(id));
    showToast('Media dihapus', 'success');
    loadMediaFiles();
  } catch (e) {
    showToast('Gagal hapus media: ' + e.message, 'error');
  }
}

function renderBroadcastMediaPicker() {
  const box = $('broadcastMediaPicker');
  if (!box) return;
  const availableFiles = mediaFiles.filter(file => String(file.mime || '').trim());
  if (!availableFiles.length) {
    box.innerHTML = '<div class="empty-state-inline">Belum ada media di Media Center.</div>';
    return;
  }
  box.innerHTML = availableFiles.map(file => {
    const checked = selectedBroadcastMediaIDs.includes(Number(file.id)) ? 'checked' : '';
    return `
      <label class="media-picker-item">
        <input type="checkbox" value="${Number(file.id)}" ${checked} onchange="syncBroadcastMediaSelection()" />
        <span class="media-picker-thumb">${renderMediaThumb(file)}</span>
        <span>${escapeHtml(getMediaKind(file.mime))}: ${escapeHtml(file.original_name || file.name)}</span>
      </label>
    `;
  }).join('');
}

async function syncBroadcastMediaSelection() {
  const checked = Array.from(document.querySelectorAll('#broadcastMediaPicker input[type="checkbox"]:checked'));
  selectedBroadcastMediaIDs = checked.map(input => Number(input.value)).filter(Boolean);
  const selectedFiles = selectedBroadcastMediaIDs
    .map(id => mediaFiles.find(file => Number(file.id) === Number(id)))
    .filter(Boolean);
  try {
    imageUploads = await Promise.all(selectedFiles.map(async file => {
      const res = await fetch(file.url);
      if (!res.ok) throw new Error(`Gagal membaca ${file.original_name || file.name}`);
      const blob = await res.blob();
      return blobToImagePayload(blob, file.original_name || file.name, file.mime || blob.type || 'application/octet-stream');
    }));
    renderImagePreview('imagePreview', imageUploads);
    renderBroadcastPreview();
  } catch (e) {
    showToast(e.message, 'error');
  }
}

function blobToImagePayload(blob, name, mime) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = String(reader.result || '');
      resolve({ name, mime, data: result.split(',')[1] || '' });
    };
    reader.onerror = () => reject(new Error('Gagal membaca media'));
    reader.readAsDataURL(blob);
  });
}

// ===== Templates =====
async function loadTemplates() {
  try {
    const data = await api('/templates/broadcast');
    const sel = $('templateSelect');
    if (!sel) return;
    sel.innerHTML = '<option value="">Pilih template...</option>';
    (data.templates || []).forEach(t => {
      const o = document.createElement('option');
      o.value = t.name;
      o.textContent = t.name;
      sel.appendChild(o);
    });
    sel.onchange = () => {
      const t = (data.templates || []).find(x => x.name === sel.value);
      if (t) $('message').value = t.message;
      renderBroadcastPreview();
    };
  } catch (_) {}
}

async function saveTemplate() {
  const name = $('templateName').value.trim();
  if (!name) { showToast('Masukkan nama template'); return; }
  try {
    await api('/templates', {
      method: 'POST',
      body: JSON.stringify({ name, message: $('message').value, type: 'broadcast' })
    });
    loadTemplates();
    showToast(`Template "${name}" disimpan`, 'success');
  } catch (_) {}
}

async function deleteTemplate() {
  const name = $('templateSelect').value || $('templateName').value;
  if (!name) return;
  try {
    await api('/templates/broadcast/' + encodeURIComponent(name), { method: 'DELETE' });
    loadTemplates();
  } catch (_) {}
}

async function loadTemplatesPersonal() {
  try {
    const data = await api('/templates/personalisasi');
    const sel = $('templatePersonalSelect');
    if (!sel) return;
    sel.innerHTML = '<option value="">Pilih template...</option>';
    (data.templates || []).forEach(t => {
      const o = document.createElement('option');
      o.value = t.name;
      o.textContent = t.name;
      sel.appendChild(o);
    });
    sel.onchange = () => {
      const t = (data.templates || []).find(x => x.name === sel.value);
      if (t) $('messagePersonal').value = t.message;
    };
  } catch (_) {}
}

async function saveTemplatePersonal() {
  const name = $('templatePersonalName').value.trim();
  if (!name) { showToast('Masukkan nama template'); return; }
  try {
    await api('/templates', {
      method: 'POST',
      body: JSON.stringify({ name, message: $('messagePersonal').value, type: 'personalisasi' })
    });
    loadTemplatesPersonal();
    showToast(`Template "${name}" disimpan`, 'success');
  } catch (_) {}
}

async function deleteTemplatePersonal() {
  const name = $('templatePersonalSelect').value || $('templatePersonalName').value;
  if (!name) return;
  try {
    await api('/templates/personalisasi/' + encodeURIComponent(name), { method: 'DELETE' });
    loadTemplatesPersonal();
  } catch (_) {}
}

// ===== Tools: Validate Numbers =====
async function runValidateNumbers() {
  const raw = $('validateInput')?.value || '';
  const nums = raw.split(/[\r\n,;]+/).map(l => l.trim().replace(/\D+/g, '')).filter(n => n.length >= 6).slice(0, 500);
  if (!nums.length) { $('validateStatus').textContent = 'Masukkan nomor terlebih dahulu.'; return; }
  const toolsAccountID = getSelectedToolsAccountID();
  if (!toolsAccountID) { $('validateStatus').textContent = 'Pilih akun WhatsApp tools terlebih dahulu.'; return; }

  $('validateStatus').textContent = `⏳ Memvalidasi ${nums.length} nomor...`;
  $('validateStatus').style.color = '#f59e0b';
  appendLog(`Validasi ${nums.length} nomor...`, 'info');

  try {
    // Validate in batches of 50
    lastValidateResult = [];
    for (let i = 0; i < nums.length; i += 50) {
      const batch = nums.slice(i, i + 50);
      $('validateStatus').textContent = `⏳ Batch ${Math.floor(i/50)+1}/${Math.ceil(nums.length/50)}...`;
      const data = await api('/validate', {
        method: 'POST',
        body: JSON.stringify(withAccountBody({ numbers: batch }, toolsAccountID))
      });
      if (data.results) {
        lastValidateResult.push(...data.results);
      }
    }

    const valid = lastValidateResult.filter(r => r.is_in);
    const invalid = lastValidateResult.filter(r => !r.is_in);

    $('validateStatus').textContent = `✅ ${valid.length} valid, ❌ ${invalid.length} tidak terdaftar`;
    $('validateStatus').style.color = '#22c55e';
    $('validateOutput').style.display = 'block';
    $('validateOutput').value = lastValidateResult.map(r =>
      `${r.query} → ${r.is_in ? '✅ VALID' : '❌ TIDAK TERDAFTAR'}`
    ).join('\n');

    appendLog(`Validasi selesai: ${valid.length} valid, ${invalid.length} invalid`, 'success');
  } catch (e) {
    $('validateStatus').textContent = '❌ ' + e.message;
    $('validateStatus').style.color = '#ef4444';
  }
}

function copyValidNumbers() {
  const valid = lastValidateResult.filter(r => r.is_in).map(r => r.query.replace(/^\+/, ''));
  if (!valid.length) { showToast('Tidak ada nomor valid'); return; }
  navigator.clipboard.writeText(valid.join('\n'));
  showToast(`${valid.length} nomor valid disalin!`, 'success');
}

function clearValidation() {
  $('validateInput').value = '';
  $('validateOutput').value = '';
  $('validateOutput').style.display = 'none';
  $('validateStatus').textContent = '';
  lastValidateResult = [];
}

// ===== Tools: WA Groups =====
async function loadWAGroups() {
  const sel = $('waGroupList');
  if (!sel) return;
  const toolsAccountID = getSelectedToolsAccountID();
  if (!toolsAccountID) {
    sel.innerHTML = '<option value="">Pilih akun tools dulu...</option>';
    return;
  }
  sel.innerHTML = '<option value="">Memuat...</option>';
  try {
    const data = await api(appendAccountQuery('/groups', toolsAccountID));
    sel.innerHTML = '<option value="">Pilih grup WA...</option>';
    (data.groups || []).forEach(g => {
      const o = document.createElement('option');
      o.value = g.id;
      o.textContent = `${g.name} (${g.members} anggota)`;
      sel.appendChild(o);
    });
    appendLog(`${(data.groups||[]).length} grup ditemukan`, 'success');
  } catch (e) {
    sel.innerHTML = '<option value="">❌ Gagal memuat</option>';
    appendLog('Gagal memuat grup: ' + e.message, 'error');
  }
}

async function scrapeGroupMembers() {
  const groupId = $('waGroupList')?.value;
  if (!groupId) { showToast('Pilih grup terlebih dahulu'); return; }
  const toolsAccountID = getSelectedToolsAccountID();
  if (!toolsAccountID) { showToast('Pilih akun WhatsApp tools terlebih dahulu'); return; }

  $('groupScrapeStatus').textContent = '⏳ Mengambil anggota...';
  $('groupScrapeStatus').style.color = '#f59e0b';
  groupMembersData = [];
  if ($('copyGroupBtn')) $('copyGroupBtn').disabled = true;

  try {
    const data = await api(appendAccountQuery('/groups/' + encodeURIComponent(groupId) + '/members', toolsAccountID));
    groupMembersData = (data.members || []).filter(m => m.phone && m.phone.startsWith('62'));
    $('groupScrapeStatus').textContent = `OK ${groupMembersData.length} anggota dari "${data.group_name}"${data.skipped_hidden ? ` (${data.skipped_hidden} hidden dilewati)` : ''}`;
    $('groupScrapeStatus').style.color = '#22c55e';
    $('groupMembersOutput').style.display = 'block';
    $('groupMembersOutput').value = groupMembersData.map(m => m.phone).join('\n');
    if ($('copyGroupBtn')) $('copyGroupBtn').disabled = !groupMembersData.length;
    appendLog(`Scrape grup: ${groupMembersData.length} anggota`, 'success');
  } catch (e) {
    $('groupScrapeStatus').textContent = '❌ ' + e.message;
    $('groupScrapeStatus').style.color = '#ef4444';
  }
}

function copyGroupMembers() {
  if (!groupMembersData.length) { showToast('Tidak ada data'); return; }
  navigator.clipboard.writeText(groupMembersData.map(m => m.phone).join('\n'));
  showToast(`${groupMembersData.length} nomor disalin!`, 'success');
}

async function loadChatHistory() {
  const toolsAccountID = getSelectedToolsAccountID();
  if (!toolsAccountID) {
    if ($('historyScrapeStatus')) {
      $('historyScrapeStatus').textContent = 'Pilih akun WhatsApp tools terlebih dahulu.';
      $('historyScrapeStatus').style.color = '#ef4444';
    }
    return;
  }
  if ($('historyScrapeStatus')) {
    $('historyScrapeStatus').textContent = 'Memuat riwayat chat...';
    $('historyScrapeStatus').style.color = '#f59e0b';
  }
  if ($('copyHistoryBtn')) $('copyHistoryBtn').disabled = true;
  try {
    const data = await api(appendAccountQuery('/history/chats', toolsAccountID));
    chatHistoryData = (data.history || []).filter(item => (item.phone || '').startsWith('62'));
    if ($('historyOutput')) {
      $('historyOutput').style.display = 'block';
      $('historyOutput').value = chatHistoryData.map(item => item.phone).join('\n');
    }
    if ($('historyScrapeStatus')) {
      $('historyScrapeStatus').textContent = `${chatHistoryData.length} chat personal ditemukan`;
      $('historyScrapeStatus').style.color = '#22c55e';
    }
    if ($('copyHistoryBtn')) $('copyHistoryBtn').disabled = !chatHistoryData.length;
    appendLog(`Scrape riwayat: ${chatHistoryData.length} chat`, 'success');
  } catch (e) {
    if ($('historyScrapeStatus')) {
      $('historyScrapeStatus').textContent = 'Gagal memuat riwayat: ' + e.message;
      $('historyScrapeStatus').style.color = '#ef4444';
    }
  }
}

function copyChatHistory() {
  if (!chatHistoryData.length) { showToast('Tidak ada data'); return; }
  navigator.clipboard.writeText(chatHistoryData.map(item => item.phone).join('\n'));
  showToast(`${chatHistoryData.length} nomor disalin!`, 'success');
}

async function clearChatHistory() {
  if (!confirm('Bersihkan hasil scrape riwayat chat akun aktif?')) return;
  const toolsAccountID = getSelectedToolsAccountID();
  if (!toolsAccountID) { showToast('Pilih akun WhatsApp tools terlebih dahulu'); return; }
  try {
    await api(appendAccountQuery('/history/chats', toolsAccountID), { method: 'DELETE' });
    chatHistoryData = [];
    if ($('historyOutput')) $('historyOutput').value = '';
    if ($('historyScrapeStatus')) {
      $('historyScrapeStatus').textContent = 'Riwayat chat dibersihkan';
      $('historyScrapeStatus').style.color = '#22c55e';
    }
    if ($('copyHistoryBtn')) $('copyHistoryBtn').disabled = true;
  } catch (e) {
    showToast('Gagal membersihkan riwayat: ' + e.message, 'error');
  }
}

// ===== InstaBlast AI =====
async function loadAISettings() {
  try {
    const data = await api('/ai/settings');
    aiSelectedAccountIDs = Array.isArray(data.account_ids) ? data.account_ids : [];
    aiKnowledgeProducts = normalizeAIKnowledgeProducts(data.products);
    aiAccountProductIDs = normalizeAIAccountProductMap(data.account_product_ids);
    if ($('aiEnabled')) $('aiEnabled').checked = !!data.enabled;
    if ($('aiOcrEnabled')) $('aiOcrEnabled').checked = data.vision_enabled !== false;
    if ($('aiRajaOngkirEnabled')) $('aiRajaOngkirEnabled').checked = !!data.rajaongkir_enabled;
    if ($('aiRajaOngkirApiKey')) $('aiRajaOngkirApiKey').value = data.rajaongkir_api_key || '';
    if ($('aiRajaOngkirOrigin')) $('aiRajaOngkirOrigin').value = data.rajaongkir_origin || '';
    if ($('aiInstruction')) $('aiInstruction').value = data.instruction || '';
    if ($('aiProductInfo')) $('aiProductInfo').value = data.product_info || '';
    if ($('aiDelayMs')) $('aiDelayMs').value = data.delay_ms || 1200;
    if ($('aiMaxHistory')) $('aiMaxHistory').value = data.max_history || 15;
    if ($('aiBatchWindowMs')) $('aiBatchWindowMs').value = data.batch_window_ms || 4500;
    syncRajaOngkirFields();
    renderAIAccountPicker();
    renderAIKnowledgeList();
    renderAIKnowledgeImagePreview();
    ['aiEnabled', 'aiOcrEnabled', 'aiRajaOngkirEnabled', 'aiRajaOngkirApiKey', 'aiRajaOngkirOrigin', 'aiInstruction', 'aiProductInfo', 'aiDelayMs', 'aiMaxHistory', 'aiBatchWindowMs', 'aiKnowledgeName', 'aiKnowledgeContent', 'aiKnowledgeImageInput', 'aiKnowledgeSelect', 'aiKnowledgeAccountSelect']
      .forEach(id => { if ($(id)) $(id).disabled = !!data.locked; });
    document.querySelectorAll('#tab-ai .btn').forEach(btn => btn.disabled = !!data.locked);
    if (data.locked && $('aiEnabled')) $('aiEnabled').checked = false;
    if ($('aiStatus')) {
      $('aiStatus').textContent = data.locked ? 'Fitur InstaBlast AI dikunci untuk user ini' : (data.enabled ? 'AI auto-reply aktif' : 'AI auto-reply nonaktif');
      $('aiStatus').style.color = data.locked ? '#ef4444' : (data.enabled ? '#22c55e' : 'var(--muted)');
    }
    if ($('aiLastError')) $('aiLastError').textContent = '';
  } catch (e) {
    if ($('aiStatus')) {
      $('aiStatus').textContent = 'Gagal memuat setting AI: ' + e.message;
      $('aiStatus').style.color = '#ef4444';
    }
  }
}

async function saveAISettings() {
  const enabled = $('aiEnabled')?.checked || false;
  const selectedAccountIDs = getSelectedAIAccountIDs();
  if (enabled && !selectedAccountIDs.length) {
    showToast('Pilih minimal satu akun WhatsApp untuk AI', 'error');
    return;
  }

  const body = {
    enabled,
    vision_enabled: $('aiOcrEnabled')?.checked !== false,
    rajaongkir_enabled: $('aiRajaOngkirEnabled')?.checked || false,
    rajaongkir_api_key: $('aiRajaOngkirApiKey')?.value?.trim() || '',
    rajaongkir_origin: $('aiRajaOngkirOrigin')?.value?.trim() || '',
    instruction: $('aiInstruction')?.value || '',
    product_info: $('aiProductInfo')?.value || '',
    products: aiKnowledgeProducts.map(item => ({
      id: item.id,
      name: item.name,
      content: item.content,
      image_path: item.image_path || '',
      image_url: item.image_url || '',
    })),
    account_product_ids: aiAccountProductIDs,
    delay_ms: parseInt($('aiDelayMs')?.value || '1200', 10) || 0,
    max_history: parseInt($('aiMaxHistory')?.value || '15', 10) || 15,
    batch_window_ms: parseInt($('aiBatchWindowMs')?.value || '4500', 10) || 4500,
    account_ids: selectedAccountIDs,
  };

  try {
    const data = await api('/ai/settings', {
      method: 'POST',
      body: JSON.stringify(body)
    });

    if ($('aiEnabled')) $('aiEnabled').checked = !!data.enabled;
    if ($('aiRajaOngkirEnabled')) $('aiRajaOngkirEnabled').checked = !!data.rajaongkir_enabled;
    if ($('aiRajaOngkirApiKey')) $('aiRajaOngkirApiKey').value = data.rajaongkir_api_key || body.rajaongkir_api_key;
    if ($('aiRajaOngkirOrigin')) $('aiRajaOngkirOrigin').value = data.rajaongkir_origin || body.rajaongkir_origin;
    aiSelectedAccountIDs = Array.isArray(data.account_ids) ? data.account_ids : selectedAccountIDs;
    aiKnowledgeProducts = normalizeAIKnowledgeProducts(data.products);
    aiAccountProductIDs = normalizeAIAccountProductMap(data.account_product_ids);
    syncRajaOngkirFields();
    renderAIAccountPicker();
    renderAIKnowledgeList();
    renderAIKnowledgeImagePreview();
    if ($('aiStatus')) {
      $('aiStatus').textContent = data.enabled ? 'AI auto-reply aktif' : 'AI auto-reply nonaktif';
      $('aiStatus').style.color = data.enabled ? '#22c55e' : 'var(--muted)';
    }
    if ($('aiLastError')) $('aiLastError').textContent = '';
    showToast('Pengaturan AI disimpan', 'success');
    appendLog('Pengaturan InstaBlast AI disimpan', 'success');
    refreshAIStats();
  } catch (e) {
    if ($('aiStatus')) {
      $('aiStatus').textContent = 'Gagal simpan AI: ' + e.message;
      $('aiStatus').style.color = '#ef4444';
    }
    showToast('Gagal simpan AI: ' + e.message, 'error');
  }
}

async function testAI() {
  try {
    if ($('aiStatus')) {
      $('aiStatus').textContent = 'Testing AI...';
      $('aiStatus').style.color = '#f59e0b';
    }
    const data = await api('/ai/test', {
      method: 'POST',
      body: JSON.stringify({
        prompt: 'Halo, balas singkat dalam 1 kalimat bahasa Indonesia.'
      })
    });

    if ($('aiStatus')) {
      $('aiStatus').textContent = 'OK: ' + (data.reply || '').slice(0, 120);
      $('aiStatus').style.color = '#22c55e';
    }
    if ($('aiLastError')) $('aiLastError').textContent = '';
    showToast('AI test berhasil', 'success');
  } catch (e) {
    if ($('aiStatus')) {
      $('aiStatus').textContent = 'Test AI gagal';
      $('aiStatus').style.color = '#ef4444';
    }
    if ($('aiLastError')) $('aiLastError').textContent = e.message;
    showToast('Test AI gagal: ' + e.message, 'error');
  }
}

async function refreshAIStats() {
  try {
    const stats = await api('/ai/stats');
    if ($('aiStatReceived')) $('aiStatReceived').textContent = stats.received || 0;
    if ($('aiStatReplied')) $('aiStatReplied').textContent = stats.replied || 0;
    if ($('aiStatFailed')) $('aiStatFailed').textContent = stats.failed || 0;
    if ($('aiLastError')) $('aiLastError').textContent = stats.last_error || '';
  } catch (_) {}
}

function startAIStatsPolling() {
  if (aiStatsTimer) return;
  aiStatsTimer = setInterval(refreshAIStats, 5000);
}

// ===== History =====
async function loadHistory() {
  try {
    await loadHistoryQueue();
    const data = await api('/history');
    const records = normalizeHistoryRecords(data);
    const tbody = $('historyTableBody');
    const summary = summarizeHistoryRecords(records);

    applyHistorySummary(summary);
    applyDashboardSummary(summary);

    if (!records.length) {
      tbody.innerHTML = '<tr><td colspan="8" class="empty-cell">Belum ada riwayat broadcast</td></tr>';
      return;
    }

    tbody.innerHTML = records.map((r, i) => `
      <tr>
        <td>${i + 1}</td>
        <td>${new Date(r.date).toLocaleString('id-ID')}</td>
        <td style="text-align:center;">${r.total}</td>
        <td style="text-align:center;color:#22c55e;">${r.sent}</td>
        <td style="text-align:center;color:#ef4444;">${r.failed}</td>
        <td>${escapeHtml((r.message || '').substring(0, 50))}</td>
        <td>${r.duration || '-'}</td>
        <td><span class="feature-badge" style="margin:0;font-size:10px;">${r.type || 'broadcast'}</span></td>
      </tr>
    `).join('');
  } catch (_) {}
}

async function clearHistory() {
  if (!confirm('Hapus semua riwayat broadcast?')) return;
  try {
    await api('/history', { method: 'DELETE' });
    loadHistory();
    showToast('Riwayat dihapus', 'success');
  } catch (_) {}
}

async function loadAdminUsers() {
  const tbody = $('adminUsersTableBody');
  if (!tbody || !currentUser?.is_admin) return;
  try {
    const data = await api('/admin/users');
    const users = data.users || [];
    if (!users.length) {
      tbody.innerHTML = '<tr><td colspan="7" class="empty-cell">Belum ada user</td></tr>';
      return;
    }
    tbody.innerHTML = users.map(user => `
      <tr>
        <td>
          <div style="display:flex; flex-direction:column; gap:6px;">
            <span>${escapeHtml(user.email || '-')}</span>
            ${user.is_trial ? '<span class="feature-badge" style="margin:0; width:fit-content;">TRIAL</span>' : ''}
          </div>
        </td>
        <td>${user.is_admin ? 'Admin' : (user.is_trial ? 'User Trial' : 'User')}</td>
        <td>${user.is_active ? 'Aktif' : 'Nonaktif'}</td>
        <td>${user.can_use_ai ? 'Aktif' : 'Terkunci'}</td>
        <td style="text-align:center;">${user.max_devices || 0}</td>
        <td>${user.expires_at ? new Date(user.expires_at).toLocaleDateString('id-ID') : '-'}</td>
        <td>${user.is_admin ? '-' : `
          <div style="display:flex; gap:8px; flex-wrap:wrap;">
            <button class="btn btn-secondary btn-sm" onclick="startManagedUserEdit('${escapeHtml(user.id)}')">Edit</button>
            <button class="btn btn-danger btn-sm" onclick="deleteManagedUser('${escapeHtml(user.id)}','${escapeHtml(user.email)}')">Hapus</button>
          </div>
        `}</td>
      </tr>
    `).join('');
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="7" class="empty-cell">Gagal memuat user: ${escapeHtml(e.message)}</td></tr>`;
  }
}

async function createManagedUser() {
  if (!currentUser?.is_admin) return;
  const body = {
    email: $('adminUserEmail')?.value?.trim() || '',
    password: $('adminUserPassword')?.value || '',
    max_devices: parseInt($('adminUserMaxDevices')?.value || '1', 10) || 1,
    active_days: parseInt($('adminUserActiveDays')?.value || '30', 10) || 30,
    can_use_ai: $('adminUserCanAI')?.checked || false,
  };
  try {
    const data = await api('/admin/users', {
      method: 'POST',
      body: JSON.stringify(body)
    });
    if ($('adminUserStatus')) {
      $('adminUserStatus').textContent = `User ${data.user.email} berhasil ditambahkan`;
      $('adminUserStatus').style.color = '#22c55e';
    }
    ['adminUserEmail', 'adminUserPassword'].forEach(id => { if ($(id)) $(id).value = ''; });
    loadAdminUsers();
  } catch (e) {
    if ($('adminUserStatus')) {
      $('adminUserStatus').textContent = e.message;
      $('adminUserStatus').style.color = '#ef4444';
    }
  }
}

function getManagedUserActiveDays(user) {
  if (!user?.expires_at) return 1;
  const diffMs = new Date(user.expires_at).getTime() - Date.now();
  const days = Math.ceil(diffMs / (24 * 60 * 60 * 1000));
  return days > 0 ? days : 1;
}

async function startManagedUserEdit(id) {
  if (!id || !currentUser?.is_admin) return;
  try {
    const data = await api('/admin/users');
    const users = data.users || [];
    const user = users.find(item => item.id === id);
    if (!user) throw new Error('User tidak ditemukan');
    editingManagedUser = user;
    if ($('adminEditUserCard')) $('adminEditUserCard').style.display = 'block';
    if ($('adminEditUserEmail')) $('adminEditUserEmail').value = user.email || '';
    if ($('adminEditUserPassword')) $('adminEditUserPassword').value = '';
    if ($('adminEditUserMaxDevices')) $('adminEditUserMaxDevices').value = String(user.max_devices || 1);
    if ($('adminEditUserActiveDays')) $('adminEditUserActiveDays').value = String(getManagedUserActiveDays(user));
    if ($('adminEditUserCanAI')) $('adminEditUserCanAI').checked = !!user.can_use_ai;
    if ($('adminEditUserIsActive')) $('adminEditUserIsActive').checked = !!user.is_active;
    if ($('adminEditUserIsTrial')) $('adminEditUserIsTrial').checked = !!user.is_trial;
    if ($('adminEditUserStatus')) {
      $('adminEditUserStatus').textContent = `Sedang mengedit ${user.email}`;
      $('adminEditUserStatus').style.color = '#667085';
    }
    $('adminEditUserCard')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  } catch (e) {
    if ($('adminUserStatus')) {
      $('adminUserStatus').textContent = e.message;
      $('adminUserStatus').style.color = '#ef4444';
    }
  }
}

function cancelManagedUserEdit() {
  editingManagedUser = null;
  if ($('adminEditUserCard')) $('adminEditUserCard').style.display = 'none';
  if ($('adminEditUserStatus')) $('adminEditUserStatus').textContent = '';
  ['adminEditUserEmail', 'adminEditUserPassword'].forEach(id => { if ($(id)) $(id).value = ''; });
}

async function saveManagedUserEdit() {
  if (!currentUser?.is_admin || !editingManagedUser?.id) return;
  const body = {
    email: $('adminEditUserEmail')?.value?.trim() || '',
    password: $('adminEditUserPassword')?.value || '',
    max_devices: parseInt($('adminEditUserMaxDevices')?.value || '1', 10) || 1,
    active_days: parseInt($('adminEditUserActiveDays')?.value || '1', 10) || 1,
    can_use_ai: $('adminEditUserCanAI')?.checked || false,
    is_active: $('adminEditUserIsActive')?.checked || false,
    is_trial: $('adminEditUserIsTrial')?.checked || false,
    is_admin: false,
  };
  try {
    const data = await api('/admin/users/' + encodeURIComponent(editingManagedUser.id), {
      method: 'PATCH',
      body: JSON.stringify(body)
    });
    if ($('adminEditUserStatus')) {
      $('adminEditUserStatus').textContent = `User ${data.user.email} berhasil diperbarui`;
      $('adminEditUserStatus').style.color = '#22c55e';
    }
    if (currentUser?.id === data.user.id) {
      currentUser = data.user;
      renderCurrentUser();
    }
    await loadAdminUsers();
    setTimeout(() => {
      cancelManagedUserEdit();
    }, 600);
  } catch (e) {
    if ($('adminEditUserStatus')) {
      $('adminEditUserStatus').textContent = e.message;
      $('adminEditUserStatus').style.color = '#ef4444';
    }
  }
}

async function deleteManagedUser(id, email) {
  if (!id) return;
  if (!confirm(`Hapus user ${email}?`)) return;
  try {
    await api('/admin/users/' + encodeURIComponent(id), { method: 'DELETE' });
    loadAdminUsers();
  } catch (e) {
    if ($('adminUserStatus')) {
      $('adminUserStatus').textContent = e.message;
      $('adminUserStatus').style.color = '#ef4444';
    }
  }
}

async function loadAdminAIConfig() {
  if (!currentUser?.is_admin) return;
  try {
    const data = await api('/admin/ai-config');
    if ($('adminGlobalApiKey')) $('adminGlobalApiKey').value = data.global_api_key || '';
  } catch (e) {
    if ($('adminAIStatus')) {
      $('adminAIStatus').textContent = e.message;
      $('adminAIStatus').style.color = '#ef4444';
    }
  }
}

async function loadAdminMetaConfig() {
  if (!currentUser?.is_admin) return;
  try {
    const data = await api('/admin/meta-config');
    if ($('adminMetaAppId')) $('adminMetaAppId').value = data.app_id || '';
    if ($('adminMetaAppSecret')) $('adminMetaAppSecret').value = data.app_secret || '';
    if ($('adminMetaConfigId')) $('adminMetaConfigId').value = data.config_id || '';
    if ($('adminMetaRedirectUri')) $('adminMetaRedirectUri').value = data.redirect_uri || '';
    if ($('adminMetaVerifyToken')) $('adminMetaVerifyToken').value = data.verify_token || '';
  } catch (e) {
    if ($('adminMetaStatus')) {
      $('adminMetaStatus').textContent = e.message;
      $('adminMetaStatus').style.color = '#ef4444';
    }
  }
}

function setAdminTrialOTPText(id, message, color) {
  if (!$(id)) return;
  $(id).textContent = message || '';
  $(id).style.color = color || '#667085';
}

function renderAdminTrialOTPAccount(data = {}) {
  const account = data.account || {};
  const connected = !!data.connected;
  if ($('adminTrialOTPEnabled')) $('adminTrialOTPEnabled').checked = !!data.enabled;
  if ($('adminTrialOTPTTL') && data.ttl_minutes) $('adminTrialOTPTTL').value = String(data.ttl_minutes);
  if ($('adminTrialOTPMessageTemplate') && typeof data.message_template === 'string') $('adminTrialOTPMessageTemplate').value = data.message_template;
  if ($('adminTrialOTPSuccessTemplate') && typeof data.success_template === 'string') $('adminTrialOTPSuccessTemplate').value = data.success_template;
  if ($('adminTrialOTPAccountName')) $('adminTrialOTPAccountName').value = account.name || 'Verifier OTP Trial';
  if ($('adminTrialOTPConnection')) $('adminTrialOTPConnection').value = connected ? 'Online' : (account.status || 'Belum login');
  if ($('adminTrialOTPJID')) $('adminTrialOTPJID').value = data.jid || account.phone || account.jid || '-';
  if (data.qr && $('adminTrialOTPQRImage') && $('adminTrialOTPQRWrap')) {
    $('adminTrialOTPQRImage').src = data.qr;
    $('adminTrialOTPQRWrap').style.display = 'block';
  } else if (connected && $('adminTrialOTPQRWrap')) {
    $('adminTrialOTPQRWrap').style.display = 'none';
    if ($('adminTrialOTPQRImage')) $('adminTrialOTPQRImage').src = '';
  }
}

async function loadAdminTrialOTPConfig() {
  if (!currentUser?.is_admin) return;
  try {
    const data = await api('/admin/trial-otp-config');
    renderAdminTrialOTPAccount(data || {});
    if (data?.account_error) {
      setAdminTrialOTPText('adminTrialOTPDeviceStatus', data.account_error, '#ef4444');
    }
  } catch (e) {
    setAdminTrialOTPText('adminTrialOTPStatus', e.message, '#ef4444');
    setAdminTrialOTPText('adminTrialOTPDeviceStatus', e.message, '#ef4444');
  }
}

async function loadAdminTrialOTPStatus() {
  if (!currentUser?.is_admin) return;
  try {
    const data = await api('/admin/trial-otp-status');
    renderAdminTrialOTPAccount(data || {});
    setAdminTrialOTPText('adminTrialOTPDeviceStatus', data.connected ? 'WhatsApp verifier OTP sedang online.' : 'WhatsApp verifier OTP belum login. Scan QR untuk menghubungkan.', data.connected ? '#22c55e' : '#f59e0b');
  } catch (e) {
    setAdminTrialOTPText('adminTrialOTPDeviceStatus', e.message, '#ef4444');
  }
}

async function saveAdminTrialOTPConfig() {
  if (!currentUser?.is_admin) return;
  try {
    await api('/admin/trial-otp-config', {
      method: 'POST',
      body: JSON.stringify({
        enabled: $('adminTrialOTPEnabled')?.checked || false,
        ttl_minutes: parseInt($('adminTrialOTPTTL')?.value || '5', 10) || 5,
        message_template: $('adminTrialOTPMessageTemplate')?.value || '',
        success_template: $('adminTrialOTPSuccessTemplate')?.value || ''
      })
    });
    setAdminTrialOTPText('adminTrialOTPStatus', 'Konfigurasi OTP trial berhasil disimpan', '#22c55e');
    await loadAdminTrialOTPConfig();
  } catch (e) {
    setAdminTrialOTPText('adminTrialOTPStatus', e.message, '#ef4444');
  }
}

async function requestAdminTrialOTPQR() {
  if (!currentUser?.is_admin) return;
  try {
    setAdminTrialOTPText('adminTrialOTPDeviceStatus', 'Meminta QR verifier OTP...', '#f59e0b');
    const data = await api('/admin/trial-otp/qr');
    renderAdminTrialOTPAccount(data || {});
    if (data.status === 'already_logged_in' || data.connected) {
      setAdminTrialOTPText('adminTrialOTPDeviceStatus', 'Verifier OTP sudah login dan online.', '#22c55e');
      return;
    }
    if (data.status === 'qr') {
      setAdminTrialOTPText('adminTrialOTPDeviceStatus', 'QR verifier OTP tampil. Silakan scan dengan WhatsApp admin.', '#22c55e');
      let attempts = 0;
      const interval = setInterval(async () => {
        attempts += 1;
        if (attempts > 60) {
          clearInterval(interval);
          return;
        }
        try {
          const status = await api('/admin/trial-otp-status');
          renderAdminTrialOTPAccount(status || {});
          if (status.connected) {
            clearInterval(interval);
            setAdminTrialOTPText('adminTrialOTPDeviceStatus', 'WhatsApp verifier OTP berhasil terhubung.', '#22c55e');
          }
        } catch (_) {}
      }, 3000);
    }
  } catch (e) {
    setAdminTrialOTPText('adminTrialOTPDeviceStatus', e.message, '#ef4444');
  }
}

async function reconnectAdminTrialOTP() {
  if (!currentUser?.is_admin) return;
  try {
    await api('/admin/trial-otp/reconnect', { method: 'POST' });
    setAdminTrialOTPText('adminTrialOTPDeviceStatus', 'Meminta reconnect verifier OTP...', '#f59e0b');
    setTimeout(loadAdminTrialOTPStatus, 2500);
  } catch (e) {
    setAdminTrialOTPText('adminTrialOTPDeviceStatus', e.message, '#ef4444');
  }
}

async function logoutAdminTrialOTP() {
  if (!currentUser?.is_admin) return;
  if (!confirm('Logout akun WhatsApp verifier OTP?')) return;
  try {
    await api('/admin/trial-otp/logout', { method: 'POST' });
    if ($('adminTrialOTPQRWrap')) $('adminTrialOTPQRWrap').style.display = 'none';
    if ($('adminTrialOTPQRImage')) $('adminTrialOTPQRImage').src = '';
    setAdminTrialOTPText('adminTrialOTPDeviceStatus', 'Verifier OTP berhasil logout.', '#22c55e');
    await loadAdminTrialOTPStatus();
  } catch (e) {
    setAdminTrialOTPText('adminTrialOTPDeviceStatus', e.message, '#ef4444');
  }
}

async function saveAdminAIConfig() {
  if (!currentUser?.is_admin) return;
  try {
    await api('/admin/ai-config', {
      method: 'POST',
      body: JSON.stringify({
        global_api_key: $('adminGlobalApiKey')?.value?.trim() || ''
      })
    });
    if ($('adminAIStatus')) {
      $('adminAIStatus').textContent = 'API key global berhasil disimpan';
      $('adminAIStatus').style.color = '#22c55e';
    }
  } catch (e) {
    if ($('adminAIStatus')) {
      $('adminAIStatus').textContent = e.message;
      $('adminAIStatus').style.color = '#ef4444';
    }
  }
}

async function saveAdminMetaConfig() {
  if (!currentUser?.is_admin) return;
  try {
    await api('/admin/meta-config', {
      method: 'POST',
      body: JSON.stringify({
        app_id: $('adminMetaAppId')?.value?.trim() || '',
        app_secret: $('adminMetaAppSecret')?.value?.trim() || '',
        config_id: $('adminMetaConfigId')?.value?.trim() || '',
        redirect_uri: $('adminMetaRedirectUri')?.value?.trim() || '',
        verify_token: $('adminMetaVerifyToken')?.value?.trim() || '',
      })
    });
    if ($('adminMetaStatus')) {
      $('adminMetaStatus').textContent = 'Konfigurasi Meta berhasil disimpan';
      $('adminMetaStatus').style.color = '#22c55e';
    }
  } catch (e) {
    if ($('adminMetaStatus')) {
      $('adminMetaStatus').textContent = e.message;
      $('adminMetaStatus').style.color = '#ef4444';
    }
  }
}

function openMetaSignupModal() {
  if ($('metaSignupModal')) $('metaSignupModal').classList.add('open');
  resetMetaSignupRuntime();
  resetMetaSignupChecklist();
  syncMetaSignupLaunchState();
  if ($('metaModalStatus')) {
    $('metaModalStatus').textContent = 'Menyiapkan Meta Embedded Signup...';
    $('metaModalStatus').style.color = '#f59e0b';
  }
  prepareMetaSignup();
}

function closeMetaSignupModal() {
  if ($('metaSignupModal')) $('metaSignupModal').classList.remove('open');
}

function resetMetaSignupRuntime() {
  metaSignupCode = '';
  metaSignupSessionInfo = null;
  metaSignupCompleting = false;
  metaSignupState = '';
  metaSignupConfig = null;
  metaSignupReadyAt = 0;
  metaSignupSDKReady = false;
  if (metaSignupFallbackTimer) {
    clearTimeout(metaSignupFallbackTimer);
    metaSignupFallbackTimer = null;
  }
}

function setMetaLaunchButtonDisabled(disabled) {
  if ($('metaLaunchButton')) $('metaLaunchButton').disabled = !!disabled;
}

function resetMetaSignupChecklist() {
  document.querySelectorAll('#metaSignupModal input[data-meta-check]').forEach((el) => {
    el.checked = false;
  });
}

function isMetaSignupChecklistComplete() {
  const checks = Array.from(document.querySelectorAll('#metaSignupModal input[data-meta-check]'));
  return checks.length > 0 && checks.every((el) => el.checked);
}

function syncMetaSignupLaunchState() {
  const canLaunch = metaSignupSDKReady && isMetaSignupChecklistComplete();
  setMetaLaunchButtonDisabled(!canLaunch);
  return canLaunch;
}

function isTrustedMetaOrigin(origin) {
  try {
    const { hostname } = new URL(origin);
    return hostname === 'facebook.com' || hostname.endsWith('.facebook.com');
  } catch (_) {
    return false;
  }
}

async function ensureMetaFacebookSDK(appId, graphVersion) {
  if (!appId) throw new Error('Meta App ID belum diisi');

  const initSDK = () => {
    if (!window.FB || typeof window.FB.init !== 'function') {
      throw new Error('Facebook SDK belum siap');
    }
    window.FB.init({
      appId,
      cookie: true,
      xfbml: false,
      version: graphVersion || 'v22.0'
    });
    return window.FB;
  };

  if (window.FB && typeof window.FB.init === 'function') {
    return initSDK();
  }

  if (!metaSDKReadyPromise) {
    metaSDKReadyPromise = new Promise((resolve, reject) => {
      const existing = document.getElementById('facebook-jssdk');
      window.fbAsyncInit = () => {
        try {
          resolve(initSDK());
        } catch (err) {
          reject(err);
        }
      };
      if (existing) return;
      const script = document.createElement('script');
      script.id = 'facebook-jssdk';
      script.async = true;
      script.defer = true;
      script.src = 'https://connect.facebook.net/en_US/sdk.js';
      script.onerror = () => reject(new Error('Gagal memuat Facebook SDK'));
      document.body.appendChild(script);
    });
  }

  return metaSDKReadyPromise.then(() => initSDK());
}

async function prepareMetaSignup() {
  if (metaSignupPreparing) return metaSignupPreparing;
  metaSignupPreparing = (async () => {
    try {
      const data = await api('/meta/signup/session');
      metaSignupConfig = data || {};
      metaSignupState = data.state || '';
      metaSignupReadyAt = Date.now();
      metaSignupSDKReady = !!(data && data.launch_url && data.state);
      syncMetaSignupLaunchState();
      if ($('metaModalStatus')) {
        $('metaModalStatus').textContent = isMetaSignupChecklistComplete()
          ? 'Meta siap. Lanjutkan ke Facebook untuk mulai daftar.'
          : 'Meta siap. Centang checklist wajib di bawah sebelum lanjut.';
        $('metaModalStatus').style.color = isMetaSignupChecklistComplete() ? '#22c55e' : '#f59e0b';
      }
    } catch (e) {
      metaSignupSDKReady = false;
      syncMetaSignupLaunchState();
      if ($('metaModalStatus')) {
        $('metaModalStatus').textContent = e.message;
        $('metaModalStatus').style.color = '#ef4444';
      }
      if ($('metaSignupStatus')) {
        $('metaSignupStatus').textContent = e.message;
        $('metaSignupStatus').style.color = '#ef4444';
      }
      throw e;
    } finally {
      metaSignupPreparing = null;
    }
  })();
  return metaSignupPreparing;
}

function handleMetaFBLoginResponse(response) {
  if (response?.authResponse?.code) {
    metaSignupCode = response.authResponse.code;
    if ($('metaSignupStatus')) {
      $('metaSignupStatus').textContent = 'Kode otorisasi Meta diterima. Menunggu detail WABA...';
      $('metaSignupStatus').style.color = '#f59e0b';
    }
    scheduleMetaSignupCompletion();
    return;
  }

  const message = response?.status === 'unknown'
    ? 'Login Facebook dibatalkan atau popup tertutup sebelum selesai.'
    : 'Meta tidak mengembalikan authorization code.';
  if ($('metaSignupStatus')) {
    $('metaSignupStatus').textContent = message;
    $('metaSignupStatus').style.color = '#ef4444';
  }
}

function scheduleMetaSignupCompletion(force = false) {
  if (!metaSignupCode || metaSignupCompleting) return;
  if (metaSignupFallbackTimer) clearTimeout(metaSignupFallbackTimer);
  if (!force && !metaSignupSessionInfo) {
    metaSignupFallbackTimer = setTimeout(() => scheduleMetaSignupCompletion(true), 3000);
    return;
  }
  completeMetaSignup();
}

async function launchMetaSignup() {
  try {
    if (!metaSignupConfig || !metaSignupState || (Date.now() - metaSignupReadyAt) > (10 * 60 * 1000)) {
      throw new Error('Sesi Meta belum siap. Tutup modal lalu buka lagi.');
    }
    if (!metaSignupConfig.launch_url) {
      throw new Error('URL signup Meta belum tersedia');
    }
    if (!isMetaSignupChecklistComplete()) {
      throw new Error('Centang dulu checklist penting sebelum lanjut ke Facebook.');
    }
    if ($('metaModalStatus')) {
      $('metaModalStatus').textContent = 'Popup resmi Facebook sedang dibuka...';
      $('metaModalStatus').style.color = '#f59e0b';
    }
    if ($('metaSignupStatus')) {
      $('metaSignupStatus').textContent = 'Selesaikan semua langkah di popup Facebook sampai tombol Finish.';
      $('metaSignupStatus').style.color = '#f59e0b';
    }
    const popup = window.open(
      metaSignupConfig.launch_url,
      'MetaEmbeddedSignup',
      'width=1200,height=800,scrollbars=yes,resizable=yes'
    );
    if (!popup) {
      throw new Error('Popup Facebook diblokir browser. Izinkan popup lalu coba lagi.');
    }
  } catch (e) {
    if ($('metaModalStatus')) {
      $('metaModalStatus').textContent = e.message;
      $('metaModalStatus').style.color = '#ef4444';
    }
    if ($('metaSignupStatus')) {
      $('metaSignupStatus').textContent = e.message;
      $('metaSignupStatus').style.color = '#ef4444';
    }
  }
}

async function completeMetaSignupFromCallback(payload) {
  if (!payload?.state || payload.state !== metaSignupState) return;
  if (payload.error) {
    if ($('metaSignupStatus')) {
      $('metaSignupStatus').textContent = payload.error_description || payload.error || 'Embedded Signup dibatalkan';
      $('metaSignupStatus').style.color = '#ef4444';
    }
    closeMetaSignupModal();
    return;
  }
  metaSignupCode = payload.code || '';
  if ($('metaSignupStatus')) {
    $('metaSignupStatus').textContent = 'Kode callback Meta diterima. Menyelesaikan sinkronisasi akun...';
    $('metaSignupStatus').style.color = '#f59e0b';
  }
  scheduleMetaSignupCompletion(true);
}

function handleMetaSignupMessageEvent(event) {
  if (!isTrustedMetaOrigin(event.origin)) return;

  let data = event.data;
  if (typeof data === 'string') {
    try {
      data = JSON.parse(data);
    } catch (_) {
      return;
    }
  }

  if (!data || data.type !== 'WA_EMBEDDED_SIGNUP') return;

  if (String(data.event || '').toUpperCase().includes('FINISH')) {
    metaSignupSessionInfo = data.data || {};
    if ($('metaSignupStatus')) {
      $('metaSignupStatus').textContent = 'Detail WABA diterima dari Meta. Menyimpan akun...';
      $('metaSignupStatus').style.color = '#f59e0b';
    }
    scheduleMetaSignupCompletion();
    return;
  }

  if (String(data.event || '').toUpperCase() === 'CANCEL') {
    const errorMessage = data?.data?.error_message || '';
    const currentStep = data?.data?.current_step || '';
    const message = errorMessage
      ? `Flow Meta dibatalkan: ${errorMessage}`
      : `Flow Meta dibatalkan${currentStep ? ' pada langkah ' + currentStep : ''}.`;
    if ($('metaSignupStatus')) {
      $('metaSignupStatus').textContent = message;
      $('metaSignupStatus').style.color = '#ef4444';
    }
    if ($('metaModalStatus')) {
      $('metaModalStatus').textContent = message;
      $('metaModalStatus').style.color = '#ef4444';
    }
  }
}

async function completeMetaSignup() {
  if (metaSignupCompleting || !metaSignupState || !metaSignupCode) return;
  metaSignupCompleting = true;
  try {
    const payload = {
      state: metaSignupState,
      code: metaSignupCode,
      business_id: metaSignupSessionInfo?.business_id || '',
      waba_id: metaSignupSessionInfo?.waba_id || '',
      phone_number_id: metaSignupSessionInfo?.phone_number_id || '',
      display_phone_number: metaSignupSessionInfo?.display_phone_number || '',
      name: metaSignupSessionInfo?.business_name || ''
    };
    const data = await api('/meta/signup/complete', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
    const warnings = Array.isArray(data.warnings) ? data.warnings.filter(Boolean) : [];
    if ($('metaSignupStatus')) {
      $('metaSignupStatus').textContent = warnings.length
        ? 'WABA berhasil tersimpan, tetapi ada catatan lanjutan yang perlu dicek.'
        : 'WABA berhasil ditambahkan dan siap dilanjutkan ke setup berikutnya.';
      $('metaSignupStatus').style.color = warnings.length ? '#f59e0b' : '#22c55e';
    }
    closeMetaSignupModal();
    await loadMetaAccounts();
    showToast(warnings.length ? 'WABA tersimpan dengan catatan' : 'WABA berhasil ditambahkan', warnings.length ? 'warning' : 'success');
    if (warnings.length) {
      appendLog('Meta WABA warnings: ' + warnings.join(' | '), 'warning');
    }
  } catch (e) {
    if ($('metaSignupStatus')) {
      $('metaSignupStatus').textContent = 'Gagal menyelesaikan signup Meta: ' + e.message;
      $('metaSignupStatus').style.color = '#ef4444';
    }
    if ($('metaModalStatus')) {
      $('metaModalStatus').textContent = e.message;
      $('metaModalStatus').style.color = '#ef4444';
    }
  } finally {
    metaSignupCompleting = false;
    metaSignupState = '';
    metaSignupCode = '';
    metaSignupSessionInfo = null;
    if (metaSignupFallbackTimer) {
      clearTimeout(metaSignupFallbackTimer);
      metaSignupFallbackTimer = null;
    }
  }
}

async function connectMetaManual() {
  const accessToken = $('metaManualAccessToken')?.value?.trim() || '';
  const wabaId = $('metaManualWabaId')?.value?.trim() || '';
  const name = $('metaManualName')?.value?.trim() || '';
  if (!accessToken) {
    if ($('metaManualStatus')) {
      $('metaManualStatus').textContent = 'Access token wajib diisi';
      $('metaManualStatus').style.color = '#ef4444';
    }
    return;
  }
  if (!wabaId) {
    if ($('metaManualStatus')) {
      $('metaManualStatus').textContent = 'WABA ID wajib diisi';
      $('metaManualStatus').style.color = '#ef4444';
    }
    return;
  }
  try {
    if ($('metaManualStatus')) {
      $('metaManualStatus').textContent = 'Memvalidasi token dan mengambil data WABA dari Meta...';
      $('metaManualStatus').style.color = '#f59e0b';
    }
    const data = await api('/meta/manual/connect', {
      method: 'POST',
      body: JSON.stringify({
        access_token: accessToken,
        waba_id: wabaId,
        name
      })
    });
    const warnings = Array.isArray(data.warnings) ? data.warnings.filter(Boolean) : [];
    if ($('metaManualStatus')) {
      $('metaManualStatus').textContent = warnings.length
        ? 'Akun WABA tersimpan, tetapi masih ada catatan lanjutan yang perlu dicek.'
        : 'Akun WABA berhasil dihubungkan secara manual.';
      $('metaManualStatus').style.color = warnings.length ? '#f59e0b' : '#22c55e';
    }
    await loadMetaAccounts();
    showToast(warnings.length ? 'WABA manual tersimpan dengan catatan' : 'WABA manual berhasil terhubung', warnings.length ? 'warning' : 'success');
    if (warnings.length) {
      appendLog('Meta WABA manual warnings: ' + warnings.join(' | '), 'warning');
    }
  } catch (e) {
    if ($('metaManualStatus')) {
      $('metaManualStatus').textContent = e.message;
      $('metaManualStatus').style.color = '#ef4444';
    }
  }
}

async function loadMetaAccounts() {
  const tbody = $('metaAccountsTableBody');
  if (!tbody) return;
  try {
    const data = await api('/meta/accounts');
    const accounts = data.accounts || [];
    if ($('metaStatTotal')) $('metaStatTotal').textContent = accounts.length;
    if ($('metaStatReady')) $('metaStatReady').textContent = accounts.filter(acc => (acc.status || '') === 'active').length;
    if ($('metaStatPending')) $('metaStatPending').textContent = accounts.filter(acc => (acc.status || '') !== 'active').length;
    if (!accounts.length) {
      tbody.innerHTML = '<tr><td colspan="7" class="empty-cell">Belum ada akun WABA Cloud</td></tr>';
      return;
    }
    tbody.innerHTML = accounts.map(acc => `
      <tr>
        <td>${escapeHtml(acc.name || 'WABA Cloud')}</td>
        <td>${escapeHtml(acc.waba_id || '-')}</td>
        <td>${escapeHtml(acc.phone_number_id || '-')}</td>
        <td>${escapeHtml(acc.display_phone_number || '-')}</td>
        <td><span class="feature-badge">${escapeHtml(acc.status || '-')}</span></td>
        <td>${escapeHtml(acc.onboarding_status || '-')}</td>
        <td>${acc.updated_at ? new Date(acc.updated_at).toLocaleString('id-ID') : '-'}</td>
      </tr>
    `).join('');
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="7" class="empty-cell">Gagal memuat akun WABA: ${escapeHtml(e.message)}</td></tr>`;
  }
}

// ===== Settings =====
async function loadSettings() {
  try {
    const s = await api('/settings');
    if ($('delay')) $('delay').value = s.delay_seconds || 3;
    if ($('randomDelay')) {
      $('randomDelay').checked = s.random_delay || false;
      $('randomDelayRow').style.display = s.random_delay ? 'block' : 'none';
    }
    if ($('delayMin')) $('delayMin').value = s.delay_min || 2;
    if ($('delayMax')) $('delayMax').value = s.delay_max || 5;
    if ($('burstEvery')) $('burstEvery').value = s.burst_every || 0;
    if ($('burstPauseSec')) $('burstPauseSec').value = s.burst_pause || 0;
    syncInlineBroadcastTimingFromSettings();
  } catch (_) {}
}

async function saveSettings() {
  try {
    await api('/settings', {
      method: 'POST',
      body: JSON.stringify({
        delay_seconds: $('delay').value,
        random_delay: String($('randomDelay').checked),
        delay_min: $('delayMin').value,
        delay_max: $('delayMax').value,
        burst_every: $('burstEvery').value,
        burst_pause: $('burstPauseSec').value,
      })
    });
    showToast('Pengaturan disimpan', 'success');
    $('settingsStatus').textContent = '✅ Pengaturan disimpan';
    $('settingsStatus').style.color = '#22c55e';
  } catch (e) {
    showToast('Gagal simpan: ' + e.message, 'error');
  }
}

// ===== Init =====
document.addEventListener('DOMContentLoaded', () => {
  normalizeLegacyLayout();
  // Toggle handlers
  $('randomDelay')?.addEventListener('change', () => {
    $('randomDelayRow').style.display = $('randomDelay').checked ? 'block' : 'none';
  });
  $('attachImage')?.addEventListener('change', () => {
    $('imageRow').style.display = $('attachImage').checked ? 'block' : 'none';
    if ($('attachImage').checked) {
      loadMediaFiles();
    }
    if (!$('attachImage').checked) {
      imageUploads = [];
      selectedBroadcastMediaIDs = [];
      renderImagePreview('imagePreview', imageUploads);
      renderBroadcastMediaPicker();
    }
    renderBroadcastPreview();
  });
  $('attachImagePersonal')?.addEventListener('change', () => {
    $('imageRowPersonal').style.display = $('attachImagePersonal').checked ? 'block' : 'none';
    if (!$('attachImagePersonal').checked) {
      imageUploadsPersonal = [];
      if ($('imageInputPersonal')) $('imageInputPersonal').value = '';
      renderImagePreview('imagePreviewPersonal', imageUploadsPersonal);
    }
  });
  $('aiRajaOngkirEnabled')?.addEventListener('change', syncRajaOngkirFields);
  $('broadcastRandomDelay')?.addEventListener('change', () => {
    $('broadcastRandomDelayRow').style.display = $('broadcastRandomDelay').checked ? 'block' : 'none';
  });
  $('broadcastScheduleToggle')?.addEventListener('change', () => {
    $('broadcastScheduleBox').style.display = $('broadcastScheduleToggle').checked ? 'block' : 'none';
  });
  switchContactsView(contactsView);
  ['broadcastAccountSelect', 'numbers', 'message', 'scheduleName', 'groupSelect'].forEach(id => {
    $(id)?.addEventListener('input', renderBroadcastPreview);
    $(id)?.addEventListener('change', renderBroadcastPreview);
  });
  document.querySelectorAll('#metaSignupModal input[data-meta-check]').forEach((el) => {
    el.addEventListener('change', () => {
      const canLaunch = syncMetaSignupLaunchState();
      if ($('metaModalStatus') && metaSignupSDKReady) {
        $('metaModalStatus').textContent = canLaunch
          ? 'Checklist lengkap. Anda bisa lanjut ke portal Facebook sekarang.'
          : 'Centang semua checklist penting sebelum lanjut ke Facebook.';
        $('metaModalStatus').style.color = canLaunch ? '#22c55e' : '#f59e0b';
      }
    });
  });

  // Connect WebSocket
  connectWS();

  if ($('copyGroupBtn')) $('copyGroupBtn').disabled = true;
  if ($('copyHistoryBtn')) $('copyHistoryBtn').disabled = true;

  Promise.allSettled([loadCurrentUser(), loadAccounts()]).finally(() => {
    renderAPIDocsMeta();
    setTimeout(() => {
      checkConnection();
      refreshDashboardSummary();
    }, 500);
  });

  window.addEventListener('resize', () => {
    if (!isMobileViewport()) {
      closeSidebar();
    }
  });

  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') {
      closeSidebar();
      closeMetaSignupModal();
    }
  });

  window.addEventListener('message', (event) => {
    if (event.origin === location.origin) {
      if (!event.data || event.data.type !== 'meta_embedded_signup_callback') return;
      completeMetaSignupFromCallback(event.data);
      return;
    }
    handleMetaSignupMessageEvent(event);
  });

  // Periodic status check
  setInterval(checkConnection, 10000);

  // Load settings
  loadSettings();
  loadUnsubscribeSettings();
  loadAISettings();
  refreshAIStats();
  startAIStatsPolling();

  appendLog('InstaBlast Pro siap', 'success');
});


