// @ts-nocheck
import { ApiClient } from './lib/api';
import { captureAndSave } from './lib/session';
import { checkAuthStatus, AuthStatus } from './lib/auth';
import { SpaceInfo, JobSnapshot } from './lib/types';

const DEFAULT_BACKEND_URL = 'http://localhost:56403';

function isConfluenceUrl(url: string): boolean {
  if (!url) return false;
  const lowerUrl = url.toLowerCase();
  return (
    lowerUrl.includes('atlassian.net') ||
    lowerUrl.includes('/wiki/spaces/') ||
    lowerUrl.includes('/spaces/') ||
    lowerUrl.includes('/display/') ||
    lowerUrl.includes('/pages/viewpage.action') ||
    lowerUrl.includes('/pages/viewspace.action')
  );
}

const ACTION_ICON_ACTIVE = {
  16: 'assets/icon-active-16.png',
  32: 'assets/icon-active-32.png',
};
const ACTION_ICON_INACTIVE = {
  16: 'assets/icon-inactive-16.png',
  32: 'assets/icon-inactive-32.png',
};

const CRAWL_ICON_FRAMES = [0, 1, 2, 3, 4, 5].map((i) => ({
  16: `assets/icon-crawl-f${i}-16.png`,
  32: `assets/icon-crawl-f${i}-32.png`,
}));
const CRAWL_ICON_FRAME_MS = 160;

let crawlIconActive = false;
let crawlFrameIndex = 0;
let crawlAnimTimer: ReturnType<typeof setInterval> | null = null;

function crawlFramePath(): { 16: string; 32: string } {
  return CRAWL_ICON_FRAMES[crawlFrameIndex % CRAWL_ICON_FRAMES.length];
}

async function paintCrawlIconFrame(): Promise<void> {
  const path = crawlFramePath();
  crawlFrameIndex = (crawlFrameIndex + 1) % CRAWL_ICON_FRAMES.length;
  try {
    await chrome.action.setIcon({ path });
  } catch {
    /* ignore */
  }
  try {
    const tabs: any[] = await chrome.tabs.query({});
    await Promise.all(
      tabs.map(async (t) => {
        if (t.id == null || t.id < 0) return;
        try {
          await chrome.action.setIcon({ tabId: t.id, path });
        } catch {
          /* ignore */
        }
      })
    );
  } catch {
    /* ignore */
  }
}

function startCrawlIconAnimation(): void {
  if (crawlAnimTimer != null) return;
  crawlIconActive = true;
  crawlFrameIndex = 0;
  void paintCrawlIconFrame();
  crawlAnimTimer = setInterval(() => {
    void paintCrawlIconFrame();
  }, CRAWL_ICON_FRAME_MS);
}

async function stopCrawlIconAnimation(): Promise<void> {
  if (crawlAnimTimer != null) {
    clearInterval(crawlAnimTimer);
    crawlAnimTimer = null;
  }
  const wasActive = crawlIconActive;
  crawlIconActive = false;
  if (!wasActive) return;
  try {
    const tabs: any[] = await chrome.tabs.query({});
    for (const t of tabs) {
      if (t.id != null) await updateActionIconForTab(t.id, t.url);
    }
  } catch {
    await refreshActionIconForActiveTab();
  }
}

/** Any running/pending crawl → animate; backend down or idle → green/gray. */
async function syncCrawlIconFromBackend(): Promise<void> {
  try {
    const settings = await getSettings();
    const api = new ApiClient(settings.backendUrl);
    const snapshot = await api.listCrawls();
    const active = (snapshot.jobs || []).some(
      (j) => j.status === 'running' || j.status === 'pending'
    );
    if (active) startCrawlIconAnimation();
    else await stopCrawlIconAnimation();
  } catch {
    await stopCrawlIconAnimation();
  }
}

async function updateActionIconForTab(tabId: number, url?: string): Promise<void> {
  if (tabId < 0) return;
  if (crawlIconActive) {
    try {
      await chrome.action.setIcon({ tabId, path: crawlFramePath() });
    } catch (err) {
      console.error('[spacemosquito] setIcon failed:', err);
    }
    return;
  }
  let tabUrl = url || '';
  if (!tabUrl) {
    try {
      const tab = await chrome.tabs.get(tabId);
      tabUrl = tab?.url || '';
    } catch {
      tabUrl = '';
    }
  }
  const path = isConfluenceUrl(tabUrl) ? ACTION_ICON_ACTIVE : ACTION_ICON_INACTIVE;
  try {
    await chrome.action.setIcon({ tabId, path });
  } catch (err) {
    console.error('[spacemosquito] setIcon failed:', err);
  }
}

async function refreshActionIconForActiveTab(): Promise<void> {
  try {
    const tabs: any[] = await chrome.tabs.query({ active: true, currentWindow: true });
    const tab = tabs[0];
    if (tab?.id != null) await updateActionIconForTab(tab.id, tab.url);
  } catch {
    /* ignore */
  }
}

// Load settings from storage
async function getSettings(): Promise<{ backendUrl: string }> {
  const data: any = await chrome.storage.local.get('backend_url');
  return {
    backendUrl: data.backend_url || DEFAULT_BACKEND_URL,
  };
}

// Handle session capture flow
async function handleCaptureSession(tabUrl: string, cookieStoreId?: string) {
  console.log('[spacemosquito] handleCaptureSession(tabUrl):', tabUrl, 'cookieStoreId:', cookieStoreId);
  try {
    const settings = await getSettings();
    console.log('[spacemosquito] settings:', settings);
    const api = new ApiClient(settings.backendUrl);

    if (!tabUrl) {
      return { success: false, error: 'Empty tab URL' };
    }
    if (!isConfluenceUrl(tabUrl)) {
      return { success: false, error: 'Not on a Confluence page' };
    }

    // Capture cookies and save (cookieStoreId = Multi-Account Container jar)
    const result = await captureAndSave(tabUrl, api, cookieStoreId);

    // Update session status
    if (result.success) {
      await chrome.storage.local.set({
        session_captured: true,
        session_captured_at: Date.now(),
        session_cookie_count: result.cookieCount,
      });
    }

    return result;
  } catch (error) {
    console.error('[spacemosquito] Session capture failed:', error);
    return { success: false, error: (error as Error).message };
  }
}

// Handle crawl start
async function handleStartCrawl(spaceUrl: string) {
  try {
    const settings = await getSettings();
    const api = new ApiClient(settings.backendUrl);

    const result = await api.startCrawl(spaceUrl);

    await chrome.storage.local.set({
      active_crawl: {
        jobId: result.job_id,
        spaceUrl,
        startedAt: Date.now(),
      },
    });
    startCrawlIconAnimation();

    return { success: true, jobId: result.job_id };
  } catch (error) {
    console.error('[spacemosquito] Crawl start failed:', error);
    return { success: false, error: (error as Error).message };
  }
}

// Handle crawl cancel
async function handleCancelCrawl(jobId: string) {
  try {
    const settings = await getSettings();
    const api = new ApiClient(settings.backendUrl);
    await api.cancelCrawl(jobId);
    await chrome.storage.local.remove('active_crawl');
    return { success: true };
  } catch (error) {
    console.error('[spacemosquito] Crawl cancel failed:', error);
    return { success: false, error: (error as Error).message };
  }
}

// Poll session status periodically
async function pollSessionStatus() {
  try {
    const settings = await getSettings();
    const api = new ApiClient(settings.backendUrl);
    let tabUrl = '';
    try {
      const tabs: any[] = await chrome.tabs.query({ active: true, currentWindow: true });
      tabUrl = tabs[0]?.url || '';
    } catch { /* ignore */ }
    const prev: any = await chrome.storage.local.get('session_status');
    const status = await api.getSessionStatus(isConfluenceUrl(tabUrl) ? tabUrl : undefined);
    await chrome.storage.local.set({ session_status: status });
    const wasValid = !!prev?.session_status?.valid;
    if (wasValid !== !!status.valid) {
      console.info('[spacemosquito] session status changed', {
        from: wasValid,
        to: !!status.valid,
        message: status.message,
      });
    }
    return status;
  } catch {
    return null;
  }
}

// Poll active crawl status
async function pollCrawlStatus() {
  try {
    const data: any = await chrome.storage.local.get('active_crawl');
    if (!data.active_crawl?.jobId) return null;

    const settings = await getSettings();
    const api = new ApiClient(settings.backendUrl);
    const job = await api.getCrawlStatus(data.active_crawl.jobId);

    if (job.status === 'completed' || job.status === 'failed' || job.status === 'cancelled') {
      await chrome.storage.local.set({ active_crawl_progress: job });
      await chrome.storage.local.remove('active_crawl');
    } else {
      await chrome.storage.local.set({ active_crawl_progress: job });
    }

    return job;
  } catch {
    return null;
  }
}

// Listen for messages from popup and content scripts
chrome.runtime.onMessage.addListener((msg: any, sender: any, sendResponse: (response: any) => void) => {
  switch (msg.type) {
    case 'capture-session':
      console.log('[spacemosquito] capture-session received');
      return new Promise(resolve => {
        (async () => {
          let tabUrl: string | undefined;
          let cookieStoreId: string | undefined;
          console.log('[spacemosquito] sender.tab:', sender?.tab);
          if (sender?.tab?.url) {
            tabUrl = sender.tab.url;
            cookieStoreId = sender.tab.cookieStoreId;
          } else {
            console.log('[spacemosquito] No sender.tab, querying windows...');
            try {
              const tabs: any[] = await chrome.tabs.query({ active: true, currentWindow: true });
              const tab = tabs[0];
              if (tab?.url) {
                tabUrl = tab.url;
                cookieStoreId = tab.cookieStoreId;
                console.log('[spacemosquito] Found active tab:', tabUrl, 'store:', cookieStoreId);
              } else {
                const windows: any[] = await chrome.windows.getAll({ populate: true });
                console.log('[spacemosquito] Found', windows.length, 'windows');
                for (const w of windows) {
                  for (const t of w.tabs || []) {
                    if (t.active && t.url) {
                      tabUrl = t.url;
                      cookieStoreId = t.cookieStoreId;
                      console.log('[spacemosquito] Found active tab:', tabUrl, 'store:', cookieStoreId);
                      break;
                    }
                  }
                  if (tabUrl) break;
                }
              }
            } catch (err) {
              console.error('[spacemosquito] tab lookup failed:', err);
            }
          }
          if (!tabUrl) {
            console.error('[spacemosquito] No active tab URL found');
            resolve({ success: false, error: 'No active tab' });
            return;
          }
          const result = await handleCaptureSession(tabUrl, cookieStoreId);
          console.log('[spacemosquito] capture result:', result);
          resolve(result);
        })().catch(err => {
          console.error('[spacemosquito] capture error:', err);
          resolve({ success: false, error: err.message });
        });
      });

    case 'start-crawl':
      console.log('[spacemosquito] start-crawl received, spaceUrl:', msg.spaceUrl);
      return new Promise(resolve => {
        handleStartCrawl(msg.spaceUrl).then(result => {
          console.log('[spacemosquito] start-crawl result:', result);
          resolve(result);
        }).catch(err => {
          console.error('[spacemosquito] start-crawl error:', err);
          resolve({ success: false, error: err.message });
        });
      });

    case 'cancel-crawl':
      console.log('[spacemosquito] cancel-crawl received, jobId:', msg.jobId);
      return new Promise(resolve => {
        handleCancelCrawl(msg.jobId).then(result => {
          console.log('[spacemosquito] cancel-crawl result:', result);
          resolve(result);
        }).catch(err => {
          console.error('[spacemosquito] cancel-crawl error:', err);
          resolve({ success: false, error: err.message });
        });
      });

    case 'get-space-info':
      console.log('[spacemosquito] get-space-info received');
      return new Promise(resolve => {
        (async () => {
          let tab: any;
          if (sender?.tab?.url) {
            tab = sender.tab;
          } else {
            try {
              const tabs: any[] = await chrome.tabs.query({ active: true, currentWindow: true });
              tab = tabs[0];
              if (!tab?.url) {
                const windows: any[] = await chrome.windows.getAll({ populate: true });
                for (const w of windows) {
                  for (const t of w.tabs || []) {
                    if (t.active && t.url) {
                      tab = t;
                      break;
                    }
                  }
                  if (tab?.url) break;
                }
              }
            } catch (err) {
              console.error('[spacemosquito] get-space-info windows.getAll failed:', err);
            }
          }
          const tabUrl = tab?.url || '';
          if (!tabUrl || !isConfluenceUrl(tabUrl)) {
            resolve({ spaceKey: '', spaceName: '', spaceURL: '', pageTitle: '', pageId: 0, host: '', tabUrl: '' });
            return;
          }
          const parsed = new URL(tabUrl);
          const hostname = parsed.hostname;
          const protocol = parsed.protocol;

          const match = tabUrl.match(/\/(?:wiki\/)?spaces\/([^/?#]+)/) || tabUrl.match(/\/display\/([^/?#]+)/);
          const spaceKey = match ? match[1] : (parsed.searchParams.get('spaceKey') || '');
          const spaceName = spaceKey || 'Unknown';

          let pageId = 0;
          const pageMatch = tabUrl.match(/\/pages\/(\d+)/);
          if (pageMatch) {
            pageId = parseInt(pageMatch[1], 10);
          } else {
            // Space overview uses homepageId; classic URLs use pageId.
            const q = parsed.searchParams.get('pageId') || parsed.searchParams.get('homepageId');
            if (q) pageId = parseInt(q, 10) || 0;
          }

          let spaceURL = tabUrl;
          if (spaceKey) {
            if (tabUrl.includes('/wiki/spaces/')) {
              spaceURL = `${protocol}//${hostname}/wiki/spaces/${spaceKey}/overview`;
            } else if (tabUrl.includes('/spaces/')) {
              spaceURL = `${protocol}//${hostname}/spaces/${spaceKey}/overview`;
            } else if (tabUrl.includes('/display/')) {
              spaceURL = `${protocol}//${hostname}/display/${spaceKey}`;
            } else if (/\/pages\/view(?:page|space)\.action/i.test(tabUrl)) {
              const path = parsed.pathname;
              const i = path.toLowerCase().indexOf('/pages/');
              const ctx = i >= 0 ? path.slice(0, i) : '';
              spaceURL = `${protocol}//${hostname}${ctx}/display/${spaceKey}`;
            }
          }

          let pageTitle = tab?.title || '';
          // Strip common Confluence title suffixes
          pageTitle = pageTitle.replace(/\s*[-–|]\s*Confluence.*$/i, '').trim();

          const info = {
            spaceKey,
            spaceName,
            spaceURL,
            pageTitle,
            pageId,
            host: hostname,
            tabUrl,
          };
          console.log('[spacemosquito] get-space-info:', info);
          resolve(info);
        })().catch(err => {
          console.error('[spacemosquito] get-space-info error:', err);
          resolve({ spaceKey: '', spaceName: '', spaceURL: '', pageTitle: '', pageId: 0, host: '', tabUrl: '' });
        });
      });

    case 'capture-and-validate':
      return new Promise(resolve => {
        (async () => {
          // msg.url: renew for that Confluence site (popup crawl buttons);
          // otherwise for the active tab (session chip).
          let tabUrl: string | undefined = msg.url;
          let cookieStoreId: string | undefined;
          try {
            if (tabUrl) {
              const host = new URL(tabUrl).hostname;
              const tabs: any[] = await chrome.tabs.query({});
              cookieStoreId = tabs.find((t) => {
                try { return new URL(t.url).hostname === host; } catch { return false; }
              })?.cookieStoreId;
            } else {
              const tabs: any[] = await chrome.tabs.query({ active: true, currentWindow: true });
              const tab = tabs[0];
              tabUrl = tab?.url;
              cookieStoreId = tab?.cookieStoreId;
            }
          } catch { /* ignore */ }
          if (!tabUrl) {
            resolve({ success: false, error: 'No active tab' });
            return;
          }
          const captured = await handleCaptureSession(tabUrl, cookieStoreId);
          if (!captured?.success) {
            resolve({ success: false, error: captured?.error || 'Capture failed', valid: false });
            return;
          }
          const settings = await getSettings();
          const apiClient = new ApiClient(settings.backendUrl);
          try {
            const validated = await apiClient.validateSession(tabUrl);
            await chrome.storage.local.set({
              session_status: { ...validated, exists: true },
            });
            console.info('[spacemosquito] capture-and-validate', {
              valid: !!validated.valid,
              message: validated.message,
              cookieCount: captured.cookieCount,
            });
            resolve({
              success: true,
              valid: !!validated.valid,
              message: validated.message,
              cookieCount: captured.cookieCount,
            });
          } catch (error) {
            resolve({
              success: true,
              valid: false,
              message: (error as Error).message,
              cookieCount: captured.cookieCount,
            });
          }
        })().catch(err => resolve({ success: false, error: err.message, valid: false }));
      });

    case 'auto-renew-changed':
      sendResponse({ ok: true });
      return false;

    case 'get-settings':
      getSettings().then(sendResponse);
      return true;

    case 'validate-session':
      (async () => {
        const settings = await getSettings();
        const api = new ApiClient(settings.backendUrl);
        try {
          let tabUrl = '';
          try {
            const tabs: any[] = await chrome.tabs.query({ active: true, currentWindow: true });
            tabUrl = tabs[0]?.url || '';
          } catch { /* ignore */ }
          const result = await api.validateSession(isConfluenceUrl(tabUrl) ? tabUrl : undefined);
          await chrome.storage.local.set({ session_status: { ...result, exists: true } });
          sendResponse(result);
        } catch (error) {
          sendResponse({ valid: false, message: (error as Error).message });
        }
      })();
      return true;

    default:
      sendResponse({ error: 'unknown message type' });
  }
});

async function maybeAutoRenew() {
  try {
    const data: any = await chrome.storage.local.get('auto_renew');
    if (!data.auto_renew) return;

    const tabs: any[] = await chrome.tabs.query({});
    const confluenceTabs = tabs.filter((t) => t.url && isConfluenceUrl(t.url));
    if (confluenceTabs.length === 0) return;

    // Renew for each distinct host that has an open Confluence tab
    const seen = new Set<string>();
    for (const tab of confluenceTabs) {
      let host = '';
      try {
        host = new URL(tab.url).hostname;
      } catch {
        continue;
      }
      if (!host || seen.has(host)) continue;
      seen.add(host);
      await handleCaptureSession(tab.url, tab.cookieStoreId);
      try {
        const settings = await getSettings();
        const apiClient = new ApiClient(settings.backendUrl);
        await apiClient.validateSession(tab.url);
      } catch {
        /* ignore validate errors during auto-renew */
      }
    }
  } catch (err) {
    console.error('[spacemosquito] auto-renew failed:', err);
  }
}

// Periodic polling
setInterval(async () => {
  await pollSessionStatus();
  await pollCrawlStatus();
  await maybeAutoRenew();
}, 30 * 1000);

// Crawl toolbar icon: any backend job (incl. cron), not only active_crawl storage
setInterval(() => {
  void syncCrawlIconFromBackend();
}, 2 * 1000);

// Initialize on install
chrome.runtime.onInstalled.addListener(async () => {
  const data: any = await chrome.storage.local.get('backend_url');
  if (!data.backend_url) {
    await chrome.storage.local.set({ backend_url: DEFAULT_BACKEND_URL });
  }
  await refreshActionIconForActiveTab();
  void syncCrawlIconFromBackend();
});

chrome.tabs.onActivated.addListener(async (activeInfo) => {
  await updateActionIconForTab(activeInfo.tabId);
});

chrome.tabs.onUpdated.addListener(async (tabId, changeInfo, tab) => {
  if (changeInfo.url || changeInfo.status === 'complete') {
    await updateActionIconForTab(tabId, changeInfo.url || tab?.url);
  }
});

void refreshActionIconForActiveTab();
void syncCrawlIconFromBackend();
