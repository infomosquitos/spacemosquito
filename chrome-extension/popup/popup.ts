// @ts-nocheck
import { ApiClient } from '../lib/api';
import { CrawlJob, CrawlSpace, CronSpaceConfig, ExtensionSettings, SpaceInfo } from '../lib/types';

let api: ApiClient;
let healthPollInterval: number | null = null;
let crawlPollInterval: number | null = null;
let currentSpaceInfo: SpaceInfo | null = null;
let sessionValid = false;
/** Running/pending crawl jobs keyed by space_key */
const runningBySpace = new Map<string, CrawlJob>();
/** Latest finished (completed/failed) crawl job per space_key, to report failures. */
const lastEndedBySpace = new Map<string, CrawlJob>();

type BackendState = 'unknown' | 'up' | 'down';
let backendState: BackendState = 'unknown';

const GATED_CONTROL_IDS = [
  'btn-refresh-page',
  'btn-add-space',
  'btn-crawl-all',
];

let cronReloadTimer: number | null = null;
let yamlCronDefaults: {
  full_interval: string;
  incr_interval: string;
  detection: string;
} = { full_interval: '24h', incr_interval: '2h', detection: 'dom' };

function formatBackendHost(url: string): string {
  try {
    return new URL(url).host || url;
  } catch {
    return url;
  }
}

function formatShortDate(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}

function spaceKeyFromUrl(url: string): string {
  if (!url) return '';
  try {
    const parsed = new URL(url);
    const match = url.match(/\/(?:wiki\/)?spaces\/([^/?#]+)/) || url.match(/\/display\/([^/?#]+)/);
    return match ? match[1] : (parsed.searchParams.get('spaceKey') || '');
  } catch {
    return '';
  }
}

function isBackendDown(): boolean {
  return backendState === 'down';
}

function actionsAllowed(): boolean {
  return backendState === 'up' && sessionValid;
}

/** Space crawl/cron/stop controls need only the backend: a crawl renews an
 * expired session itself (ensureSession), and stop/cron need none. */
function spaceActionsAllowed(): boolean {
  return backendState === 'up';
}

function isPageTitlePlaceholder(text: string | null): boolean {
  const t = (text || '').trim();
  return !t || t === '—' || t === 'Not on a Confluence page' || t === 'Could not read tab' || t === 'Confluence';
}

/** Page-tab version line next to the refresh button. */
function formatPageVersionMeta(liveVersion?: number | null, storedVersion?: number | null): string {
  const hasLive = liveVersion != null && Number.isFinite(liveVersion);
  const hasStored = storedVersion != null && Number.isFinite(storedVersion);
  if (hasLive && hasStored) {
    if (liveVersion === storedVersion) return `v${liveVersion} (Up to date)`;
    return `v${liveVersion} (Stored: v${storedVersion})`;
  }
  if (hasLive) return `v${liveVersion} (Stored: —)`;
  if (hasStored) return `— (Stored: v${storedVersion})`;
  return '—';
}

const BODY_FORMAT_LABELS: Record<string, { label: string; title: string }> = {
  storage: {
    label: 'CSF',
    title: 'Confluence Storage Format from REST API (body.storage)',
  },
  rendered: {
    label: 'HTML',
    title: 'HTML from browser scrape (fallback parser)',
  },
};

function setPageEngine(bodyFormat?: string | null): void {
  const el = document.getElementById('page-engine');
  if (!el) return;
  const meta = bodyFormat ? BODY_FORMAT_LABELS[bodyFormat] : undefined;
  if (meta) {
    el.textContent = meta.label;
    el.title = meta.title;
    el.classList.remove('hidden');
  } else {
    el.textContent = '';
    el.removeAttribute('title');
    el.classList.add('hidden');
  }
}

function setControlEnabled(el: HTMLButtonElement | HTMLSelectElement | null, enabled: boolean): void {
  if (!el) return;
  el.disabled = !enabled;
}

function applyGating(): void {
  const banner = document.getElementById('backend-banner');
  const urlEl = document.getElementById('backend-banner-url');
  const down = isBackendDown();
  const allow = actionsAllowed();

  if (banner) {
    if (down) {
      banner.classList.remove('hidden');
      if (urlEl) urlEl.textContent = `at ${formatBackendHost(api.getBackendUrl())}`;
    } else {
      banner.classList.add('hidden');
    }
  }

  const hint = document.getElementById('page-session-hint');
  if (hint) {
    if (!down && !sessionValid && currentSpaceInfo?.tabUrl) {
      hint.classList.remove('hidden');
    } else {
      hint.classList.add('hidden');
    }
  }

  // Session chip stays clickable when backend up (even if session invalid)
  const chip = document.getElementById('session-chip') as HTMLButtonElement | null;
  if (chip && !chip.classList.contains('busy')) {
    chip.disabled = down;
  }

  for (const id of GATED_CONTROL_IDS) {
    const el = document.getElementById(id) as HTMLButtonElement | null;
    if (!el) continue;
    if (id === 'btn-refresh-page') {
      el.disabled = !allow || !currentSpaceInfo?.pageId;
    } else if (id === 'btn-crawl-all') {
      el.disabled = !spaceActionsAllowed();
    } else {
      el.disabled = !allow;
    }
  }

  document.querySelectorAll<HTMLButtonElement | HTMLSelectElement | HTMLInputElement>(
    '#spaces-list .btn-icon:not(.btn-browse):not(.btn-delete), #spaces-list select, #spaces-list .space-cron-panel input[type="checkbox"]'
  ).forEach((el) => {
    el.disabled = !spaceActionsAllowed();
  });
}

/**
 * Crawls need a valid Confluence session. The backend expires a captured
 * session an hour after it was last validated, so first renew it from this
 * browser's cookies for the space's site; only if that fails ask to log in.
 */
async function ensureSession(spaceUrl: string): Promise<boolean> {
  if (sessionValid) return true;
  setSessionDisc('checking', 'Renewing session…');
  try {
    const result: any = await chrome.runtime.sendMessage({ type: 'capture-and-validate', url: spaceUrl });
    if (result?.success && result?.valid) {
      setSessionDisc('valid', 'Session is valid');
      return true;
    }
  } catch { /* reported below */ }
  setSessionDisc('invalid', 'Session is invalid or missing');
  alert('Your Confluence session has expired. Log in to Confluence in this browser, then try again.');
  return false;
}

function crawlResultMessage(job: CrawlJob): string {
  const at = job.total_pages > 0 ? ` at ${job.completed} / ${job.total_pages}` : '';
  if (job.status === 'failed') return `Last crawl failed${at}: ${job.error || 'unknown error'}`;
  if (job.status === 'completed' && job.failed > 0) {
    return `Last crawl: ${job.failed} page${job.failed === 1 ? '' : 's'} failed (details in serve.log)`;
  }
  return '';
}

async function probeBackend(): Promise<void> {
  const prev = backendState;
  const ok = await api.checkHealth();
  backendState = ok ? 'up' : 'down';
  applyGating();

  if (prev !== 'up' && backendState === 'up') {
    await Promise.allSettled([
      loadSessionStatus(),
      loadPageContext(),
      loadSpaces(),
      syncRunningCrawls(),
    ]);
  }
}

function startBackendPolling(): void {
  if (healthPollInterval) clearInterval(healthPollInterval);
  void probeBackend();
  healthPollInterval = window.setInterval(() => {
    void probeBackend();
  }, 2000);
}

async function init() {
  const settings = await getSettings();
  api = new ApiClient(settings.backend_url);

  const urlInput = document.getElementById('backend-url') as HTMLInputElement | null;
  if (urlInput) urlInput.value = settings.backend_url;

  const autoRenew = document.getElementById('auto-renew') as HTMLInputElement | null;
  if (autoRenew) autoRenew.checked = !!settings.auto_renew;

  await setupTabs();
  startBackendPolling();

  // Session before page meta (compare needs valid session)
  await loadSessionStatus();
  await Promise.allSettled([
    loadPageContext(),
    detectCurrentSpace(),
    loadSpaces(),
    syncRunningCrawls(),
  ]);
}

const POPUP_TABS = new Set(['page', 'spaces', 'settings']);

function activatePopupTab(tab: string) {
  if (!POPUP_TABS.has(tab)) tab = 'page';
  document.querySelectorAll('.tab-btn').forEach((t) => {
    t.classList.toggle('active', (t as HTMLElement).dataset.tab === tab);
  });
  document.querySelectorAll('.tab-content').forEach((c) => {
    c.classList.toggle('active', c.id === `tab-${tab}`);
  });
}

async function setupTabs() {
  try {
    const data: any = await chrome.storage.local.get('popup_last_tab');
    if (typeof data.popup_last_tab === 'string' && POPUP_TABS.has(data.popup_last_tab)) {
      activatePopupTab(data.popup_last_tab);
    }
  } catch { /* keep HTML default (Page) */ }

  document.querySelectorAll('.tab-btn').forEach((btn: HTMLElement) => {
    btn.addEventListener('click', () => {
      const tab = btn.dataset.tab || 'page';
      activatePopupTab(tab);
      void chrome.storage.local.set({ popup_last_tab: tab });
    });
  });
}

async function getSettings(): Promise<ExtensionSettings & { backend_url: string; auto_renew: boolean }> {
  const data: any = await chrome.storage.local.get(['backend_url', 'auto_renew']);
  return {
    backend_url: data.backend_url || 'http://localhost:56403',
    crawl_depth: 'all',
    auto_renew: !!data.auto_renew,
  };
}

function setSessionDisc(state: 'valid' | 'invalid' | 'checking', title?: string) {
  const dot = document.getElementById('session-dot');
  const chip = document.getElementById('session-chip') as HTMLButtonElement | null;
  dot?.classList.remove('connected', 'disconnected', 'checking');
  if (state === 'valid') {
    sessionValid = true;
    dot?.classList.add('connected');
  } else if (state === 'invalid') {
    sessionValid = false;
    dot?.classList.add('disconnected');
  } else {
    // Keep prior sessionValid while checking so Page hint / gates don't flicker.
    dot?.classList.add('checking');
  }
  if (chip) {
    chip.title = title || (state === 'valid'
      ? 'Session is valid'
      : state === 'invalid'
        ? 'Session is invalid or missing'
        : 'Checking session…');
  }
  applyGating();
}

async function loadSessionStatus() {
  setSessionDisc('checking', 'Checking session…');
  try {
    let tabUrl = '';
    try {
      const tabs: any[] = await chrome.tabs.query({ active: true, currentWindow: true });
      tabUrl = tabs[0]?.url || '';
    } catch { /* ignore */ }
    const status = await api.getSessionStatus(tabUrl || undefined);
    if (status.exists && status.valid) {
      setSessionDisc('valid', 'Session is valid');
    } else {
      setSessionDisc('invalid', 'Session is invalid or missing');
    }
  } catch {
    if (isBackendDown()) {
      setSessionDisc('checking', 'Waiting for backend…');
    } else {
      setSessionDisc('invalid', 'Session is invalid or missing');
    }
  }
}

async function captureAndValidate(): Promise<void> {
  if (isBackendDown()) return;
  const chip = document.getElementById('session-chip') as HTMLButtonElement | null;
  const hintBtn = document.getElementById('btn-refresh-session') as HTMLButtonElement | null;
  chip?.classList.add('busy');
  if (chip) chip.disabled = true;
  if (hintBtn) hintBtn.disabled = true;
  setSessionDisc('checking', 'Capturing & validating…');

  try {
    const result: any = await chrome.runtime.sendMessage({ type: 'capture-and-validate' });
    if (result?.success && result?.valid) {
      setSessionDisc('valid', 'Session is valid');
      await loadPageContext();
    } else if (result?.success && result?.valid === false) {
      setSessionDisc('invalid', 'Session is invalid or missing');
    } else {
      setSessionDisc('invalid', 'Session is invalid or missing');
      alert(result?.error || 'Capture & validate failed');
    }
  } catch (error) {
    setSessionDisc('invalid', 'Session is invalid or missing');
    alert('Capture failed: ' + (error as Error).message);
  } finally {
    chip?.classList.remove('busy');
    if (hintBtn) hintBtn.disabled = false;
    applyGating();
  }
}

(document.getElementById('session-chip') as HTMLButtonElement)?.addEventListener('click', () => {
  void captureAndValidate();
});

(document.getElementById('btn-refresh-session') as HTMLButtonElement)?.addEventListener('click', () => {
  void captureAndValidate();
});

async function detectCurrentSpace() {
  try {
    const info: any = await chrome.runtime.sendMessage({ type: 'get-space-info' });
    if (info?.spaceKey) {
      currentSpaceInfo = { ...currentSpaceInfo, ...info };
    }
  } catch {
    /* ignore */
  }
  applyGating();
}

async function loadPageContext() {
  const hostEl = document.getElementById('page-host') as HTMLParagraphElement;
  const titleEl = document.getElementById('page-title') as HTMLHeadingElement;
  const metaEl = document.getElementById('page-meta') as HTMLSpanElement;

  try {
    const info: any = await chrome.runtime.sendMessage({ type: 'get-space-info' });
    currentSpaceInfo = info?.spaceKey || info?.pageId ? info : null;

    if (!info?.tabUrl && !info?.host) {
      hostEl.textContent = '—';
      titleEl.textContent = 'Not on a Confluence page';
      metaEl.textContent = '—';
      setPageEngine(null);
      applyGating();
      return;
    }

    hostEl.textContent = info.host || '—';

    const tabTitle = info.pageTitle || (info.spaceKey ? `Space ${info.spaceKey}` : 'Confluence');
    const willFetchTitle = !!info.pageId && backendState === 'up';
    // Avoid tab-title flash (e.g. "… - RADAR-base") when we already show the API title
    // and are about to refresh compare/stored.
    if (!willFetchTitle || isPageTitlePlaceholder(titleEl.textContent)) {
      titleEl.textContent = tabTitle;
    }

    if (!info.pageId) {
      metaEl.textContent = '—';
      setPageEngine(null);
      applyGating();
      return;
    }

    if (isBackendDown() || !sessionValid) {
      // Still try stored-only if backend up
      if (backendState === 'up') {
        try {
          const stored = await api.getPage(info.pageId, info.spaceKey || undefined);
          metaEl.textContent = formatPageVersionMeta(null, stored.version);
          setPageEngine(stored.body_format);
          if (stored.title) titleEl.textContent = stored.title;
        } catch {
          metaEl.textContent = '—';
          setPageEngine(null);
          if (isPageTitlePlaceholder(titleEl.textContent)) titleEl.textContent = tabTitle;
        }
      } else {
        metaEl.textContent = '—';
        setPageEngine(null);
        if (isPageTitlePlaceholder(titleEl.textContent)) titleEl.textContent = tabTitle;
      }
      applyGating();
      return;
    }

    try {
      const cmp = await api.comparePage(info.pageId, info.spaceKey || undefined);
      metaEl.textContent = formatPageVersionMeta(cmp.live?.version, cmp.stored?.version);
      setPageEngine(cmp.stored?.body_format);
      if (cmp.live?.title) titleEl.textContent = cmp.live.title;
      else if (cmp.stored?.title) titleEl.textContent = cmp.stored.title;
      else if (isPageTitlePlaceholder(titleEl.textContent)) titleEl.textContent = tabTitle;
    } catch {
      try {
        const stored = await api.getPage(info.pageId, info.spaceKey || undefined);
        metaEl.textContent = formatPageVersionMeta(null, stored.version);
        setPageEngine(stored.body_format);
        if (stored.title) titleEl.textContent = stored.title;
      } catch {
        metaEl.textContent = '—';
        setPageEngine(null);
        if (isPageTitlePlaceholder(titleEl.textContent)) titleEl.textContent = tabTitle;
      }
    }
  } catch {
    hostEl.textContent = '—';
    titleEl.textContent = 'Could not read tab';
    metaEl.textContent = '—';
    setPageEngine(null);
  }
  applyGating();
}

(document.getElementById('btn-refresh-page') as HTMLButtonElement)?.addEventListener('click', async () => {
  if (!actionsAllowed() || !currentSpaceInfo?.pageId) return;
  const btn = document.getElementById('btn-refresh-page') as HTMLButtonElement;
  setControlEnabled(btn, false);
  try {
    await api.refreshPage(currentSpaceInfo.pageId, currentSpaceInfo.spaceKey || undefined);
    await loadPageContext();
  } catch (error) {
    alert('Refresh failed: ' + (error as Error).message);
  } finally {
    applyGating();
  }
});

function ensureCrawlPolling(): void {
  if (crawlPollInterval) return;
  crawlPollInterval = window.setInterval(() => {
    void syncRunningCrawls();
  }, 2000);
}

function stopCrawlPollingIfIdle(): void {
  if (runningBySpace.size > 0) return;
  if (crawlPollInterval) {
    clearInterval(crawlPollInterval);
    crawlPollInterval = null;
  }
}

function applyJobToRow(spaceKey: string, job: CrawlJob | null): void {
  const row = Array.from(document.querySelectorAll<HTMLElement>('.space-row[data-key]'))
    .find((r) => r.dataset.key === spaceKey) || null;
  if (!row) return;

  const playBtn = row.querySelector('.btn-play') as HTMLButtonElement | null;
  const panel = row.querySelector('.space-crawl-panel') as HTMLElement | null;
  const fill = row.querySelector('.space-progress-fill') as HTMLElement | null;
  const label = row.querySelector('.space-progress-label') as HTMLElement | null;
  const counts = row.querySelector('.space-row-counts') as HTMLElement | null;
  const result = row.querySelector('.space-crawl-result') as HTMLElement | null;

  const active = job && (job.status === 'running' || job.status === 'pending');
  const ended = active ? null : lastEndedBySpace.get(spaceKey) || null;
  const resultText = ended ? crawlResultMessage(ended) : '';
  if (result) {
    result.textContent = resultText;
    result.title = ended?.error || '';
    result.classList.toggle('hidden', !resultText);
  }
  if (active && job) {
    const percent = job.total_pages > 0
      ? Math.round((job.completed / job.total_pages) * 100)
      : (job.progress || 0);
    panel?.classList.remove('hidden');
    if (fill) fill.style.width = `${percent}%`;
    if (label) {
      label.textContent = job.total_pages > 0
        ? `Crawling… ${job.completed} / ${job.total_pages} · ${percent}%`
        : `Crawling… ${percent}%`;
    }
    if (counts && job.total_pages > 0) {
      counts.textContent = `${job.completed} / ${job.total_pages}`;
    }
    if (playBtn) {
      playBtn.textContent = '⏹';
      playBtn.title = 'Stop crawl';
      playBtn.classList.add('btn-stop');
      playBtn.dataset.jobId = job.id;
    }
  } else {
    panel?.classList.add('hidden');
    if (fill) fill.style.width = '0%';
    if (playBtn) {
      playBtn.textContent = '▶';
      playBtn.title = 'Crawl';
      playBtn.classList.remove('btn-stop');
      delete playBtn.dataset.jobId;
    }
  }
}

function renderAllCrawlRows(): void {
  document.querySelectorAll<HTMLElement>('.space-row[data-key]').forEach((row) => {
    const key = row.dataset.key || '';
    applyJobToRow(key, runningBySpace.get(key) || null);
  });
  applyGating();
}

async function syncRunningCrawls(): Promise<void> {
  if (isBackendDown()) {
    stopCrawlPollingIfIdle();
    return;
  }
  try {
    const snapshot = await api.listCrawls();
    const prevKeys = new Set(runningBySpace.keys());
    runningBySpace.clear();
    lastEndedBySpace.clear();

    const endedAt = (j: CrawlJob) => Date.parse(j.completed_at || j.updated_at || '') || 0;
    for (const job of snapshot.jobs || []) {
      const key = spaceKeyFromUrl(job.space_url);
      if (!key) continue;
      if (job.status === 'running' || job.status === 'pending') {
        runningBySpace.set(key, job);
      } else if (job.status === 'failed' || job.status === 'completed') {
        const prev = lastEndedBySpace.get(key);
        if (!prev || endedAt(job) > endedAt(prev)) lastEndedBySpace.set(key, job);
      }
    }

    const finished = [...prevKeys].filter((k) => !runningBySpace.has(k));
    renderAllCrawlRows();
    if (finished.length === 0) await revealBrowseLinks();

    if (finished.length > 0) {
      await loadSpaces();
      renderAllCrawlRows();
    }

    if (runningBySpace.size > 0) ensureCrawlPolling();
    else stopCrawlPollingIfIdle();
  } catch (error) {
    console.error('[spacemosquito] crawl sync error:', error);
  }
}

// A crawl writes the space's page tree early; show 📖 for running spaces as
// soon as it exists, without re-rendering the list (open panels stay open).
async function revealBrowseLinks(): Promise<void> {
  const pending = Array.from(document.querySelectorAll<HTMLElement>('.space-row[data-key]'))
    .filter((r) => !r.dataset.browse && runningBySpace.has(r.dataset.key || ''));
  if (pending.length === 0) return;
  const paths = new Map((await api.listSpaces()).map((s) => [s.space_key, s.browse_path || '']));
  for (const row of pending) {
    const path = paths.get(row.dataset.key || '');
    if (!path) continue;
    row.dataset.browse = path;
    row.querySelector('.btn-browse')?.classList.remove('invisible');
  }
}

async function startSpaceCrawl(spaceUrl: string, spaceKey?: string): Promise<void> {
  const key = spaceKey || spaceKeyFromUrl(spaceUrl);
  if (key && runningBySpace.has(key)) return;

  const result: any = await chrome.runtime.sendMessage({ type: 'start-crawl', spaceUrl });
  if (!result?.success) {
    alert(result?.error || 'Crawl failed');
    return;
  }

  const jobId = result.jobId || result.job_id;
  const optimistic: CrawlJob = {
    id: jobId,
    space_url: spaceUrl,
    status: 'running',
    progress: 0,
    total_pages: 0,
    completed: 0,
    failed: 0,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  };
  if (key) {
    runningBySpace.set(key, optimistic);
    applyJobToRow(key, optimistic);
  }
  ensureCrawlPolling();
  // Immediate sync so totals appear quickly
  void syncRunningCrawls();
}

async function stopSpaceCrawl(spaceKey: string): Promise<void> {
  const job = runningBySpace.get(spaceKey);
  if (!job) return;
  try {
    const result: any = await chrome.runtime.sendMessage({ type: 'cancel-crawl', jobId: job.id });
    if (!result?.success) {
      // Fallback: cancel via API client
      try {
        await api.cancelCrawl(job.id);
      } catch {
        alert('Cancel failed: ' + (result?.error || 'unknown error'));
        return;
      }
    }
    runningBySpace.delete(spaceKey);
    applyJobToRow(spaceKey, null);
    stopCrawlPollingIfIdle();
    await loadSpaces();
  } catch (error) {
    alert('Cancel failed: ' + (error as Error).message);
  }
}

(document.getElementById('btn-save-url') as HTMLButtonElement)?.addEventListener('click', async () => {
  const input = document.getElementById('backend-url') as HTMLInputElement;
  const url = input.value.trim();
  if (!url) return;
  try {
    api.setBackendUrl(url);
    await chrome.storage.local.set({ backend_url: url });
    await probeBackend();
    alert('Backend URL saved');
  } catch (error) {
    alert('Save failed: ' + (error as Error).message);
  }
});

(document.getElementById('auto-renew') as HTMLInputElement)?.addEventListener('change', async (e) => {
  const checked = (e.target as HTMLInputElement).checked;
  await chrome.storage.local.set({ auto_renew: checked });
  await chrome.runtime.sendMessage({ type: 'auto-renew-changed', enabled: checked });
});

(document.getElementById('btn-add-space') as HTMLButtonElement)?.addEventListener('click', async () => {
  if (!actionsAllowed()) return;
  const input = document.getElementById('add-space-url') as HTMLInputElement;
  const url = input.value.trim();
  if (!url) return;
  try {
    await api.addSpace(url);
    input.value = '';
    await loadSpaces();
  } catch (error) {
    alert('Add failed: ' + (error as Error).message);
  }
});

function formatCounts(space: CrawlSpace): string {
  const crawled = space.pages_crawled ?? 0;
  const total = space.pages_total ?? 0;
  if (total > 0) return `${crawled} / ${total}`;
  if (crawled > 0) return `${crawled} / —`;
  return '— / —';
}

function formatBodyFormatTooltip(space: CrawlSpace): string {
  const crawled = space.pages_crawled ?? 0;
  if (crawled <= 0) return '';
  const csf = space.pages_storage ?? 0;
  const html = space.pages_rendered ?? 0;
  return `Stored: ${csf} CSF · ${html} HTML`;
}

function scheduleCronReload(): void {
  if (cronReloadTimer) clearTimeout(cronReloadTimer);
  cronReloadTimer = window.setTimeout(async () => {
    try {
      await api.reloadCron();
    } catch (err) {
      console.error('[spacemosquito] cron reload failed:', err);
    }
  }, 600);
}

function optionList(values: string[], selected: string): string {
  return values.map((v) => `<option value="${v}" ${v === selected ? 'selected' : ''}>${v}</option>`).join('');
}

async function resolveCronDefaults(): Promise<void> {
  try {
    const cfg = await api.getCronConfig();
    if (cfg.yaml_full_crawl?.interval) yamlCronDefaults.full_interval = cfg.yaml_full_crawl.interval;
    if (cfg.yaml_incremental?.interval) yamlCronDefaults.incr_interval = cfg.yaml_incremental.interval;
    if (cfg.yaml_incremental?.detection) yamlCronDefaults.detection = cfg.yaml_incremental.detection;
  } catch {
    /* keep defaults */
  }
}

function cronEnabledFromOverride(ov: Partial<CronSpaceConfig> | undefined): boolean {
  return !!(ov?.full_crawl_enabled || ov?.incr_crawl_enabled);
}

async function loadCronEnabledKeys(): Promise<Set<string>> {
  const keys = new Set<string>();
  try {
    const cfg = await api.getCronConfig();
    for (const ov of cfg.per_space_overrides || []) {
      if (cronEnabledFromOverride(ov) && ov.space_key) keys.add(ov.space_key);
    }
  } catch {
    /* ignore — icons stay disabled styling */
  }
  return keys;
}

function setCronButtonState(btn: Element | null, enabled: boolean): void {
  if (!btn) return;
  btn.classList.toggle('cron-on', enabled);
  btn.setAttribute('title', enabled ? 'Cron enabled' : 'Cron disabled');
}

/** API errors arrive as `HTTP 409: {"error":"..."}`; show just the message. */
function apiErrorMessage(error: unknown): string {
  const text = (error as Error)?.message || String(error);
  const json = text.indexOf('{');
  if (json >= 0) {
    try {
      const body = JSON.parse(text.slice(json));
      if (typeof body?.error === 'string') return body.error;
    } catch { /* not JSON */ }
  }
  return text;
}

// Inline confirmation (extension popups cannot rely on window.confirm).
// Built with textContent only: space keys come from the backend.
function toggleDeletePanel(row: HTMLElement, space: CrawlSpace): void {
  const panel = row.querySelector('.space-delete-panel') as HTMLElement | null;
  if (!panel) return;
  const wasOpen = !panel.classList.contains('hidden');
  document.querySelectorAll('.space-delete-panel').forEach((p) => p.classList.add('hidden'));
  if (wasOpen) return;

  panel.textContent = '';
  const msg = document.createElement('div');
  msg.textContent = `Remove ${space.space_key}? ${space.pages_crawled} page(s) leave search and MCP; its crawl schedule is removed.`;
  const label = document.createElement('label');
  label.className = 'delete-files';
  const files = document.createElement('input');
  files.type = 'checkbox';
  label.append(files, ` Also delete saved files (saved/${space.space_key})`);
  const actions = document.createElement('div');
  actions.className = 'delete-actions';
  const remove = document.createElement('button');
  remove.type = 'button';
  remove.className = 'btn btn-danger btn-small';
  remove.textContent = 'Remove';
  const cancel = document.createElement('button');
  cancel.type = 'button';
  cancel.className = 'btn btn-small';
  cancel.textContent = 'Cancel';
  actions.append(remove, cancel);
  const status = document.createElement('div');
  status.className = 'crawl-error';

  cancel.addEventListener('click', () => panel.classList.add('hidden'));
  remove.addEventListener('click', async () => {
    remove.disabled = true;
    cancel.disabled = true;
    status.textContent = '';
    try {
      const result = await api.deleteSpace(space.space_key, files.checked);
      if (result.warning) alert(`Removed ${space.space_key}, but: ${result.warning}`);
      await loadSpaces();
    } catch (error) {
      status.textContent = 'Remove failed: ' + apiErrorMessage(error);
      remove.disabled = false;
      cancel.disabled = false;
    }
  });

  panel.append(msg, label, actions, status);
  panel.classList.remove('hidden');
}

async function toggleCronPanel(row: HTMLElement, space: CrawlSpace): Promise<void> {
  const panel = row.querySelector('.space-cron-panel') as HTMLElement | null;
  if (!panel) return;
  const opening = panel.classList.contains('hidden');
  document.querySelectorAll('.space-cron-panel').forEach((p) => p.classList.add('hidden'));
  if (!opening) return;

  await resolveCronDefaults();
  let ov: Partial<CronSpaceConfig> = {};
  try {
    ov = await api.getSpaceCronConfig(space.space_key);
  } catch {
    ov = {};
  }

  const fullInterval = ov.full_crawl_interval || yamlCronDefaults.full_interval;
  const incrInterval = ov.incr_crawl_interval || yamlCronDefaults.incr_interval;
  const detection = ov.detection || yamlCronDefaults.detection;
  const enabled = cronEnabledFromOverride(ov);

  panel.innerHTML = `
    <div class="space-cron-row">
      <label>Full crawl</label>
      <select data-field="full_crawl_interval">${optionList(['1h','6h','12h','24h','48h','7d'], fullInterval)}</select>
    </div>
    <div class="space-cron-row">
      <label>Incremental</label>
      <select data-field="incr_crawl_interval">${optionList(['30m','1h','2h','4h','6h','12h'], incrInterval)}</select>
    </div>
    <div class="space-cron-row">
      <label>Detection</label>
      <select data-field="detection">${optionList(['api','dom'], detection)}</select>
    </div>
    <div class="space-cron-row">
      <label>Enabled</label>
      <input type="checkbox" data-field="enabled" ${enabled ? 'checked' : ''}>
    </div>
  `;
  panel.classList.remove('hidden');
  applyGating();

  const cronBtn = row.querySelector('.btn-cron');
  setCronButtonState(cronBtn, enabled);

  const persist = async () => {
    if (!actionsAllowed()) return;
    const fullSel = panel.querySelector('select[data-field="full_crawl_interval"]') as HTMLSelectElement;
    const incrSel = panel.querySelector('select[data-field="incr_crawl_interval"]') as HTMLSelectElement;
    const detSel = panel.querySelector('select[data-field="detection"]') as HTMLSelectElement;
    const en = panel.querySelector('input[data-field="enabled"]') as HTMLInputElement;
    setCronButtonState(cronBtn, en.checked);
    const payload: Partial<CronSpaceConfig> = {
      space_key: space.space_key,
      space_url: space.space_url,
      full_crawl_interval: fullSel.value,
      incr_crawl_interval: incrSel.value,
      detection: detSel.value,
      full_crawl_enabled: en.checked,
      incr_crawl_enabled: en.checked,
    };
    try {
      await api.updateSpaceCron(space.space_key, payload);
      scheduleCronReload();
    } catch (error) {
      alert('Cron save failed: ' + (error as Error).message);
    }
  };

  panel.querySelectorAll('select, input').forEach((el) => {
    el.addEventListener('change', () => { void persist(); });
  });
}

function sortSpacesForDisplay(spaces: CrawlSpace[], currentKey: string): CrawlSpace[] {
  const rest = spaces
    .filter((s) => s.space_key !== currentKey)
    .sort((a, b) => (a.space_key || '').localeCompare(b.space_key || '', undefined, { sensitivity: 'base' }));
  const current = spaces.find((s) => s.space_key === currentKey);
  return current ? [current, ...rest] : rest;
}

async function loadSpaces() {
  const list = document.getElementById('spaces-list');
  const crawlAllBtn = document.getElementById('btn-crawl-all');
  if (!list || !crawlAllBtn) return;

  try {
    if (!currentSpaceInfo?.spaceKey) {
      try {
        const info: any = await chrome.runtime.sendMessage({ type: 'get-space-info' });
        if (info?.spaceKey) currentSpaceInfo = { ...currentSpaceInfo, ...info };
      } catch { /* ignore */ }
    }

    let spaces = await api.listSpaces();
    const cronEnabled = await loadCronEnabledKeys();
    const currentKey = currentSpaceInfo?.spaceKey || '';

    if (currentKey && !spaces.some((s) => s.space_key === currentKey)) {
      spaces = [
        {
          space_key: currentKey,
          space_name: currentSpaceInfo?.spaceName || currentKey,
          space_url: currentSpaceInfo?.spaceURL || '',
          pages_crawled: 0,
          pages_total: 0,
        },
        ...spaces,
      ];
    }

    const ordered = sortSpacesForDisplay(spaces, currentKey);
    list.innerHTML = '';

    if (ordered.length === 0) {
      crawlAllBtn.classList.add('hidden');
      list.innerHTML = '<p class="info-text">No spaces yet. Add a space URL below.</p>';
      applyGating();
      return;
    }

    crawlAllBtn.classList.remove('hidden');

    ordered.forEach((space) => {
      const isCurrent = space.space_key === currentKey;
      const neverCrawled = !space.last_crawled && !(space.pages_crawled > 0) && !runningBySpace.has(space.space_key);
      const dateLabel = space.last_crawled ? formatShortDate(space.last_crawled) : '—';
      const dateTitle = space.last_crawled ? new Date(space.last_crawled).toLocaleString() : 'Never crawled';
      const running = runningBySpace.get(space.space_key);

      const countsTitle = formatBodyFormatTooltip(space);
      const row = document.createElement('div');
      row.className = 'space-row' + (isCurrent ? ' current' : '');
      row.dataset.key = space.space_key;
      row.dataset.url = space.space_url || '';
      row.dataset.browse = space.browse_path || '';
      row.innerHTML = `
        <div class="space-row-main">
          <span class="space-row-name" title="${space.space_url || ''}">${space.space_key}</span>
          <span class="space-row-counts"${countsTitle ? ` title="${countsTitle}"` : ''}>${formatCounts(space)}</span>
          <span class="space-row-date" title="${dateTitle}">${dateLabel}</span>
          <button type="button" class="btn-icon btn-browse${space.browse_path ? '' : ' invisible'}" title="Browse saved pages">📖</button>
          <button type="button" class="btn-icon btn-play${running ? ' btn-stop' : ''}" title="${running ? 'Stop crawl' : 'Crawl'}" data-url="${space.space_url}">${running ? '⏹' : '▶'}</button>
          <button type="button" class="btn-icon btn-cron${cronEnabled.has(space.space_key) ? ' cron-on' : ''}" title="${cronEnabled.has(space.space_key) ? 'Cron enabled' : 'Cron disabled'}">⏱</button>
          ${space.created_at ? '<button type="button" class="btn-icon btn-delete" title="Remove space">🗑</button>' : '<span></span>'}
        </div>
        ${neverCrawled && isCurrent ? '<div class="space-row-sub">Not crawled yet</div>' : ''}
        <div class="space-crawl-panel${running ? '' : ' hidden'}">
          <div class="space-progress-track"><div class="space-progress-fill" style="width:0%"></div></div>
          <div class="space-progress-label">Crawling…</div>
        </div>
        <div class="space-crawl-result crawl-error hidden"></div>
        <div class="space-cron-panel hidden"></div>
        <div class="space-delete-panel hidden"></div>
      `;
      list.appendChild(row);

      applyJobToRow(space.space_key, running || null);

      row.querySelector('.btn-play')?.addEventListener('click', async (e) => {
        e.stopPropagation();
        if (!spaceActionsAllowed()) return;
        const btn = e.currentTarget as HTMLElement;
        if (btn.classList.contains('btn-stop') || runningBySpace.has(space.space_key)) {
          await stopSpaceCrawl(space.space_key);
          return;
        }
        const url = btn.dataset.url;
        if (!url) {
          alert('Space has no URL');
          return;
        }
        if (!(await ensureSession(url))) return;
        try {
          await startSpaceCrawl(url, space.space_key);
        } catch (error) {
          alert('Crawl failed: ' + (error as Error).message);
        }
      });

      row.querySelector('.btn-browse')?.addEventListener('click', async (e) => {
        e.stopPropagation();
        // Served by the backend: browsers do not let extensions open file:// pages.
        const path = row.dataset.browse;
        if (!path) return;
        await chrome.tabs.create({ url: api.getBackendUrl() + path });
        window.close();
      });

      row.querySelector('.btn-delete')?.addEventListener('click', (e) => {
        e.stopPropagation();
        toggleDeletePanel(row, space);
      });

      row.querySelector('.btn-cron')?.addEventListener('click', async (e) => {
        e.stopPropagation();
        if (!spaceActionsAllowed()) return;
        await toggleCronPanel(row, space);
      });
    });

    applyGating();
    if (runningBySpace.size > 0) ensureCrawlPolling();
  } catch (error) {
    list.innerHTML = isBackendDown()
      ? '<p class="info-text">Waiting for backend…</p>'
      : `<p class="crawl-error">Failed to load: ${(error as Error).message}</p>`;
  }
}

(document.getElementById('btn-crawl-all') as HTMLButtonElement)?.addEventListener('click', async () => {
  if (!spaceActionsAllowed()) return;
  try {
    const spaces = await api.listSpaces();
    const first = spaces.find((s) => s.space_url && !runningBySpace.has(s.space_key));
    if (first && !(await ensureSession(first.space_url))) return;
    for (const space of spaces) {
      if (!space.space_url) continue;
      if (runningBySpace.has(space.space_key)) continue;
      await startSpaceCrawl(space.space_url, space.space_key);
      await new Promise((r) => setTimeout(r, 400));
    }
  } catch (error) {
    alert('Crawl all failed: ' + (error as Error).message);
  }
});

init();
