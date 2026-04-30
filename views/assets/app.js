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
let groupMembersData = [];
let chatHistoryData = [];
let lastValidateResult = [];
let aiStatsTimer = null;
let waAccounts = [];
let activeWAAccountId = '';
let aiSelectedAccountIDs = [];
let currentUser = null;
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

function applyAccounts(accounts = [], activeID = '') {
  waAccounts = Array.isArray(accounts) ? accounts : [];
  activeWAAccountId = activeID || (waAccounts[0]?.id || '');
  renderAccountSwitcher();
  renderAccountCards();
  renderActiveAccountSummary();
  renderBroadcastAccountSelect();
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
}

function getSelectedBroadcastAccountID() {
  return $('broadcastAccountSelect')?.value || activeWAAccountId || '';
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
}

function renderAIAccountPicker() {
  const wrap = $('aiAccountPicker');
  if (!wrap) return;
  if (!waAccounts.length) {
    wrap.innerHTML = '<div class="empty-state-inline">Belum ada akun WhatsApp.</div>';
    aiSelectedAccountIDs = [];
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
}

function getSelectedAIAccountIDs() {
  syncAISelectedAccountsFromDOM();
  return [...aiSelectedAccountIDs];
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
    return `
      <div class="account-card${active}">
        <div class="account-card-head">
          <div>
            <div class="account-card-title">${escapeHtml(acc.name || 'Akun WA')}</div>
            <div class="account-card-subtitle">${escapeHtml(phone)}</div>
          </div>
          <div class="account-chip-row">
            <span class="account-chip">${escapeHtml(acc.status || 'Offline')}</span>
            ${pending}
          </div>
        </div>
        <div class="account-card-actions">
          <button class="btn btn-secondary btn-sm" onclick="handleAccountSwitch('${escapeHtml(acc.id)}')">Aktifkan</button>
          <button class="btn btn-secondary btn-sm" onclick="renameAccount('${escapeHtml(acc.id)}')">Rename</button>
          <button class="btn btn-danger btn-sm" onclick="deleteAccount('${escapeHtml(acc.id)}')">Hapus</button>
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
  const role = currentUser?.is_admin ? 'Admin' : 'User';
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
  document.querySelectorAll('.nav-item').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
  const btn = document.querySelector(`[data-tab="${tab}"]`);
  if (btn) btn.classList.add('active');
  const pane = $('tab-' + tab);
  if (pane) pane.classList.add('active');
  if (tab === 'accounts') loadAccounts();
  if (tab === 'history') loadHistory();
  if (tab === 'tools') {
    loadWAGroups();
    loadChatHistory();
  }
  if (tab === 'settings') loadSettings();
  if (tab === 'ai') {
    loadAISettings();
    refreshAIStats();
    startAIStatsPolling();
  }
  if (tab === 'broadcast') {
    loadGroups();
    loadTemplates();
    loadBroadcastSchedules();
  }
  if (tab === 'personalisasi') {
    loadTemplatesPersonal();
    loadPersonalSchedules();
  }
  if (tab === 'admin') {
    loadAdminUsers();
    loadAdminAIConfig();
    loadAdminMetaConfig();
  }
  if (tab === 'waba') {
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
    dot.classList.add('connected');
    text.textContent = 'Terhubung';
    if (stat) stat.textContent = 'Online';
    if (btnScan) btnScan.style.display = 'none';
    if (qrContainer) qrContainer.style.display = 'none';
    if (connectedInfo) connectedInfo.style.display = 'block';
    if (connJID) connJID.textContent = jid || '';
    if (connAccountMeta) connAccountMeta.textContent = active ? `${active.name} • ${active.status || 'Online'}` : '';
  } else {
    dot.classList.remove('connected');
    text.textContent = 'Tidak Terhubung';
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
async function startBroadcast() {
  const numbers = $('numbers').value;
  const message = $('message').value;
  const useSpintax = $('useSpintax').checked;
  const targetAccountID = getSelectedBroadcastAccountID();

  if (!numbers.trim()) { showToast('Masukkan nomor tujuan!'); return; }
  if (!message.trim()) { showToast('Masukkan pesan!'); return; }
  if (!targetAccountID) { showToast('Pilih akun WhatsApp untuk broadcast!'); return; }

  const delay = parseInt($('delay')?.value) || 3;
  const randomDelay = $('randomDelay')?.checked || false;
  const delayMin = parseInt($('delayMin')?.value) || 2;
  const delayMax = parseInt($('delayMax')?.value) || 5;
  const burstEvery = parseInt($('burstEvery')?.value) || 0;
  const burstPause = parseInt($('burstPauseSec')?.value) || 0;

  try {
    const body = withAccountBody({
      numbers, message, use_spintax: useSpintax,
      delay_seconds: delay,
      random_delay: randomDelay,
      delay_min: delayMin,
      delay_max: delayMax,
      burst_every: burstEvery,
      burst_pause: burstPause,
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
  const numbers = $('numbers')?.value || '';
  const message = $('message')?.value || '';
  const runAtRaw = $('scheduleRunAt')?.value || '';

  if (!targetAccountID) { showToast('Pilih akun WhatsApp untuk jadwal!'); return; }
  if (!numbers.trim()) { showToast('Masukkan nomor tujuan!'); return; }
  if (!message.trim()) { showToast('Masukkan pesan broadcast!'); return; }
  if (!runAtRaw) { showToast('Tentukan waktu kirim jadwal!'); return; }

  const runAt = new Date(runAtRaw);
  if (Number.isNaN(runAt.getTime())) {
    showToast('Format waktu jadwal tidak valid', 'error');
    return;
  }

  const body = withAccountBody({
    name: $('scheduleName')?.value?.trim() || '',
    numbers,
    message,
    use_spintax: $('useSpintax')?.checked || false,
    delay_seconds: parseInt($('delay')?.value || '3', 10) || 3,
    random_delay: $('randomDelay')?.checked || false,
    delay_min: parseInt($('delayMin')?.value || '2', 10) || 2,
    delay_max: parseInt($('delayMax')?.value || '5', 10) || 5,
    burst_every: parseInt($('burstEvery')?.value || '0', 10) || 0,
    burst_pause: parseInt($('burstPauseSec')?.value || '0', 10) || 0,
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

async function loadBroadcastSchedules() {
  const tbody = $('scheduleTableBody');
  if (!tbody) return;
  try {
    const data = await api('/broadcast/schedules?type=broadcast');
    const rows = data.schedules || [];
    if (!rows.length) {
      tbody.innerHTML = '<tr><td colspan="7" class="empty-cell">Belum ada jadwal broadcast</td></tr>';
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
        <td><button class="btn btn-danger btn-sm" onclick="deleteBroadcastSchedule(${Number(item.id)})">Hapus</button></td>
      </tr>
    `).join('');
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="7" class="empty-cell">Gagal memuat jadwal: ${escapeHtml(e.message)}</td></tr>`;
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

function updateProgressUI(p) {
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

function formatDuration(startedAt) {
  if (!startedAt) return '-';
  const start = new Date(startedAt);
  const dur = Math.round((Date.now() - start.getTime()) / 1000);
  if (dur < 60) return dur + 's';
  if (dur < 3600) return Math.floor(dur/60) + 'm ' + (dur%60) + 's';
  return Math.floor(dur/3600) + 'h ' + Math.floor((dur%3600)/60) + 'm';
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

async function readImageFiles(fileList) {
  const files = Array.from(fileList || []).filter(file => file && file.type && file.type.startsWith('image/'));
  const items = await Promise.all(files.map(file => new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = (e) => {
      const dataUrl = String(e.target.result || '');
      const parts = dataUrl.split(',');
      if (parts.length < 2) {
        reject(new Error('Gagal membaca gambar'));
        return;
      }
      resolve({
        name: file.name,
        mime: file.type || 'image/jpeg',
        data: parts[1],
      });
    };
    reader.onerror = () => reject(new Error('Gagal membaca gambar'));
    reader.readAsDataURL(file);
  })));
  return items;
}

function renderImagePreview(previewID, items) {
  const preview = $(previewID);
  if (!preview) return;
  if (!items.length) {
    preview.innerHTML = '';
    return;
  }
  const summary = items.length === 1
    ? '1 gambar siap dikirim dengan caption'
    : `${items.length} gambar siap dikirim, lalu pesan teks dikirim setelah semua gambar`;
  preview.innerHTML = `
    <div style="color:#22c55e; font-weight:600;">${escapeHtml(summary)}</div>
    <div>${items.map(item => `<div>${escapeHtml(item.name)}</div>`).join('')}</div>
  `;
}

async function handleImageUpload(event) {
  try {
    imageUploads = await readImageFiles(event.target.files);
    renderImagePreview('imagePreview', imageUploads);
    if (imageUploads.length) {
      appendLog(`${imageUploads.length} gambar dimuat untuk broadcast`, 'success');
    }
  } catch (e) {
    imageUploads = [];
    renderImagePreview('imagePreview', imageUploads);
    showToast(e.message, 'error');
  }
}

async function handleImageUploadPersonal(event) {
  try {
    imageUploadsPersonal = await readImageFiles(event.target.files);
    renderImagePreview('imagePreviewPersonal', imageUploadsPersonal);
    if (imageUploadsPersonal.length) {
      appendLog(`${imageUploadsPersonal.length} gambar dimuat untuk personalisasi`, 'success');
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

// ===== Contact Groups =====
async function loadGroups() {
  try {
    const data = await api('/groups/contacts');
    const sel = $('groupSelect');
    if (!sel) return;
    sel.innerHTML = '<option value="">Pilih grup...</option>';
    (data.groups || []).forEach(g => {
      const o = document.createElement('option');
      o.value = g.name;
      o.textContent = g.name;
      sel.appendChild(o);
    });
    sel.onchange = () => {
      const name = sel.value;
      if (!name) return;
      const g = (data.groups || []).find(x => x.name === name);
      if (g) { $('numbers').value = g.numbers || ''; $('groupName').value = g.name; }
    };
  } catch (_) {}
}

async function saveGroup() {
  const name = $('groupName').value.trim();
  if (!name) { showToast('Masukkan nama grup'); return; }
  try {
    await api('/groups/contacts', {
      method: 'POST',
      body: JSON.stringify({ name, numbers: $('numbers').value })
    });
    loadGroups();
    showToast(`Grup "${name}" disimpan`, 'success');
  } catch (e) {
    showToast('Gagal simpan: ' + e.message, 'error');
  }
}

function loadGroup() {
  const sel = $('groupSelect');
  if (sel && sel.onchange) sel.onchange();
}

async function deleteGroup() {
  const name = $('groupSelect').value || $('groupName').value;
  if (!name) return;
  try {
    await api('/groups/contacts/' + encodeURIComponent(name), { method: 'DELETE' });
    loadGroups();
    showToast('Grup dihapus');
  } catch (_) {}
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
        body: JSON.stringify(withAccountBody({ numbers: batch }))
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
  sel.innerHTML = '<option value="">Memuat...</option>';
  try {
    const data = await api(appendAccountQuery('/groups'));
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

  $('groupScrapeStatus').textContent = '⏳ Mengambil anggota...';
  $('groupScrapeStatus').style.color = '#f59e0b';
  groupMembersData = [];
  if ($('copyGroupBtn')) $('copyGroupBtn').disabled = true;

  try {
    const data = await api(appendAccountQuery('/groups/' + encodeURIComponent(groupId) + '/members'));
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
  if ($('historyScrapeStatus')) {
    $('historyScrapeStatus').textContent = 'Memuat riwayat chat...';
    $('historyScrapeStatus').style.color = '#f59e0b';
  }
  if ($('copyHistoryBtn')) $('copyHistoryBtn').disabled = true;
  try {
    const data = await api(appendAccountQuery('/history/chats'));
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
  try {
    await api(appendAccountQuery('/history/chats'), { method: 'DELETE' });
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
    ['aiEnabled', 'aiOcrEnabled', 'aiRajaOngkirEnabled', 'aiRajaOngkirApiKey', 'aiRajaOngkirOrigin', 'aiInstruction', 'aiProductInfo', 'aiDelayMs', 'aiMaxHistory', 'aiBatchWindowMs']
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
    syncRajaOngkirFields();
    renderAIAccountPicker();
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
    const data = await api('/history');
    const records = data.history || [];
    const tbody = $('historyTableBody');

    let totalSent = 0, totalFailed = 0;
    records.forEach(r => { totalSent += r.sent; totalFailed += r.failed; });

    $('histTotalSent').textContent = totalSent;
    $('histTotalFailed').textContent = totalFailed;
    $('histTotalSessions').textContent = records.length;
    $('histSuccessRate').textContent = (totalSent + totalFailed > 0)
      ? Math.round((totalSent / (totalSent + totalFailed)) * 100) + '%'
      : '0%';

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
      tbody.innerHTML = '<tr><td colspan="5" class="empty-cell">Belum ada user</td></tr>';
      return;
    }
    tbody.innerHTML = users.map(user => `
      <tr>
        <td>${escapeHtml(user.email || '-')}</td>
        <td>${user.is_admin ? 'Admin' : 'User'}</td>
        <td>${user.can_use_ai ? 'Aktif' : 'Terkunci'}</td>
        <td style="text-align:center;">${user.max_devices || 0}</td>
        <td>${user.expires_at ? new Date(user.expires_at).toLocaleDateString('id-ID') : '-'}</td>
        <td>${user.is_admin ? '-' : `<button class="btn btn-danger btn-sm" onclick="deleteManagedUser('${escapeHtml(user.id)}','${escapeHtml(user.email)}')">Hapus</button>`}</td>
      </tr>
    `).join('');
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty-cell">Gagal memuat user: ${escapeHtml(e.message)}</td></tr>`;
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
  setMetaLaunchButtonDisabled(true);
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
  if (metaSignupFallbackTimer) {
    clearTimeout(metaSignupFallbackTimer);
    metaSignupFallbackTimer = null;
  }
}

function setMetaLaunchButtonDisabled(disabled) {
  if ($('metaLaunchButton')) $('metaLaunchButton').disabled = !!disabled;
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
      await ensureMetaFacebookSDK(data.app_id, data.graph_version);
      metaSignupReadyAt = Date.now();
      setMetaLaunchButtonDisabled(false);
      if ($('metaModalStatus')) {
        $('metaModalStatus').textContent = 'Meta siap. Lanjutkan dengan Facebook untuk mulai daftar.';
        $('metaModalStatus').style.color = '#22c55e';
      }
    } catch (e) {
      setMetaLaunchButtonDisabled(true);
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
    if (!window.FB || typeof window.FB.login !== 'function') {
      throw new Error('Facebook SDK belum siap');
    }
    if ($('metaModalStatus')) {
      $('metaModalStatus').textContent = 'Popup resmi Facebook sedang dibuka...';
      $('metaModalStatus').style.color = '#f59e0b';
    }
    if ($('metaSignupStatus')) {
      $('metaSignupStatus').textContent = 'Selesaikan semua langkah di popup Facebook sampai tombol Finish.';
      $('metaSignupStatus').style.color = '#f59e0b';
    }
    window.FB.login(handleMetaFBLoginResponse, {
      config_id: metaSignupConfig.config_id,
      response_type: 'code',
      override_default_response_type: true,
      extras: {
        setup: {}
      }
    });
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

async function loadMetaAccounts() {
  const tbody = $('metaAccountsTableBody');
  if (!tbody) return;
  try {
    const data = await api('/meta/accounts');
    const accounts = data.accounts || [];
    if ($('metaStatTotal')) $('metaStatTotal').textContent = accounts.length;
    if ($('metaStatReady')) $('metaStatReady').textContent = accounts.filter(acc => (acc.status || '') === 'active').length;
    if ($('metaStatPending')) $('metaStatPending').textContent = accounts.filter(acc => (acc.status || '').includes('pending')).length;
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
  // Toggle handlers
  $('randomDelay')?.addEventListener('change', () => {
    $('randomDelayRow').style.display = $('randomDelay').checked ? 'block' : 'none';
  });
  $('attachImage')?.addEventListener('change', () => {
    $('imageRow').style.display = $('attachImage').checked ? 'block' : 'none';
    if (!$('attachImage').checked) {
      imageUploads = [];
      if ($('imageInput')) $('imageInput').value = '';
      renderImagePreview('imagePreview', imageUploads);
    }
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

  // Connect WebSocket
  connectWS();

  if ($('copyGroupBtn')) $('copyGroupBtn').disabled = true;
  if ($('copyHistoryBtn')) $('copyHistoryBtn').disabled = true;

  Promise.allSettled([loadCurrentUser(), loadAccounts()]).finally(() => {
    renderAPIDocsMeta();
    setTimeout(checkConnection, 500);
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
  loadAISettings();
  refreshAIStats();
  startAIStatsPolling();

  appendLog('InstaBlast Pro siap', 'success');
});


