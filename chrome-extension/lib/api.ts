// @ts-nocheck
import { Cookie, Session, SessionStatus, CrawlJob, CrawlSpace, CronSpaceConfig, DeleteSpaceResult, SemanticSearchSetting } from './types';

export class ApiClient {
  private baseUrl: string = 'http://localhost:56403';

  constructor(baseUrl?: string) {
    if (baseUrl) {
      this.baseUrl = baseUrl;
    }
  }

  getBackendUrl(): string {
    return this.baseUrl;
  }

  setBackendUrl(url: string): void {
    this.baseUrl = url.replace(/\/+$/, '');
  }

  /** Probe GET /health. Short timeout; false on any failure. */
  async checkHealth(timeoutMs: number = 1500): Promise<boolean> {
    const url = `${this.baseUrl}/health`;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const response = await fetch(url, { method: 'GET', signal: controller.signal });
      if (!response.ok) return false;
      const text = (await response.text()).trim().toLowerCase();
      return text === 'ok' || text === '';
    } catch {
      return false;
    } finally {
      clearTimeout(timer);
    }
  }

  private async request<T>(path: string, options?: RequestInit): Promise<T> {
    const url = `${this.baseUrl}${path}`;
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...(options?.headers as Record<string, string> || {}),
    };

    try {
      const response = await fetch(url, {
        ...options,
        headers,
      });

      if (!response.ok) {
        const errorText = await response.text();
        throw new Error(`HTTP ${response.status}: ${errorText}`);
      }

      const contentType = response.headers.get('content-type');
      if (contentType && contentType.includes('application/json')) {
        return await response.json() as T;
      }
      return {} as T;
    } catch (error) {
      console.error(`[spacemosquito] API request failed: ${path}`, error);
      throw error;
    }
  }

  // Session management
  async captureSession(confluenceUrl: string, cookies: Cookie[]): Promise<{ message: string }> {
    return this.request<{ message: string }>('/api/session', {
      method: 'POST',
      body: JSON.stringify({ confluence_url: confluenceUrl, cookies }),
    });
  }

  async getSessionStatus(confluenceUrl?: string): Promise<SessionStatus> {
    const q = confluenceUrl ? `?url=${encodeURIComponent(confluenceUrl)}` : '';
    return this.request<SessionStatus>(`/api/session/status${q}`);
  }

  async validateSession(confluenceUrl?: string): Promise<{ valid: boolean; message: string }> {
    return this.request<{ valid: boolean; message: string }>('/api/session/validate', {
      method: 'POST',
      body: JSON.stringify(confluenceUrl ? { confluence_url: confluenceUrl } : {}),
    });
  }

  async deleteSession(): Promise<{ message: string }> {
    return this.request<{ message: string }>('/api/session', {
      method: 'DELETE',
    });
  }

  // Crawl management
  async startCrawl(spaceUrl: string): Promise<{ job_id: string }> {
    return this.request<{ job_id: string }>('/api/crawl', {
      method: 'POST',
      body: JSON.stringify({ space_url: spaceUrl }),
    });
  }

  async listCrawls(): Promise<JobSnapshot> {
    return this.request<JobSnapshot>('/api/crawl');
  }

  async getCrawlStatus(jobId: string): Promise<CrawlJob> {
    return this.request<CrawlJob>(`/api/crawl/status?id=${encodeURIComponent(jobId)}`);
  }

  async cancelCrawl(jobId: string): Promise<void> {
    await this.request(`/api/crawl/cancel?id=${encodeURIComponent(jobId)}`, {
      method: 'POST',
    });
  }

  async getSemanticSearch(): Promise<SemanticSearchSetting> {
    return this.request<SemanticSearchSetting>('/api/settings/semantic-search');
  }

  async setSemanticSearch(enabled: boolean): Promise<SemanticSearchSetting> {
    return this.request<SemanticSearchSetting>('/api/settings/semantic-search', {
      method: 'POST',
      body: JSON.stringify({ enabled }),
    });
  }

  async cleanupCrawls(): Promise<{ message: string }> {
    return this.request<{ message: string }>('/api/crawl/cleanup', {
      method: 'POST',
    });
  }

  // Spaces management
  async addSpace(url: string): Promise<{ message: string; space_key: string }> {
    return this.request<{ message: string; space_key: string }>('/api/spaces', {
      method: 'POST',
      body: JSON.stringify({ url }),
    });
  }

  async listSpaces(): Promise<CrawlSpace[]> {
    return this.request<CrawlSpace[]>('/api/spaces');
  }

  /** Removes a space from the catalog; saved files are kept unless deleteFiles. */
  async deleteSpace(spaceKey: string, deleteFiles = false): Promise<DeleteSpaceResult> {
    const query = deleteFiles ? '?delete_files=1' : '';
    return this.request<DeleteSpaceResult>(`/api/spaces/${encodeURIComponent(spaceKey)}${query}`, {
      method: 'DELETE',
    });
  }

  async getSpaceStatus(spaceKey: string): Promise<{
    space_key: string;
    space_name: string;
    space_url: string;
    pages_crawled: number;
    last_crawled?: string;
  }> {
    return this.request(`/api/spaces/${encodeURIComponent(spaceKey)}`);
  }

  // Cron management
  async getCronConfig(): Promise<{
    yaml_full_crawl: { enabled: boolean; interval: string; spaces: string[]; max_duration: string };
    yaml_incremental: { enabled: boolean; interval: string; detection: string; spaces: string[]; max_duration: string };
    per_space_overrides: CronSpaceConfig[];
  }> {
    return this.request('/api/cron/config');
  }

  async updateCronConfig(spaceKey: string, config: Partial<CronSpaceConfig>): Promise<{ message: string }> {
    return this.request('/api/cron/config', {
      method: 'POST',
      body: JSON.stringify({ space_key: spaceKey, ...config }),
    });
  }

  async updateSpaceCron(spaceKey: string, config: Partial<CronSpaceConfig>): Promise<{ message: string }> {
    return this.request(`/api/cron/space/${encodeURIComponent(spaceKey)}`, {
      method: 'POST',
      body: JSON.stringify(config),
    });
  }

  async deleteSpaceCron(spaceKey: string): Promise<{ message: string }> {
    return this.request(`/api/cron/space/${encodeURIComponent(spaceKey)}`, {
      method: 'DELETE',
    });
  }

  async getSpaceCronConfig(spaceKey: string): Promise<CronSpaceConfig> {
    return this.request(`/api/cron/space/${encodeURIComponent(spaceKey)}`);
  }

  async reloadCron(): Promise<{ message: string }> {
    return this.request('/api/cron/reload', {
      method: 'POST',
    });
  }

  async listCronJobs(): Promise<{ id: string; next_run: string; last_run: string; disabled: boolean }[]> {
    return this.request('/api/cron');
  }

  async startCronJobs(): Promise<{ status: string }> {
    return this.request('/api/cron/start', {
      method: 'POST',
    });
  }

  async getPage(confluenceId: number, spaceKey?: string): Promise<{
    confluence_id: number;
    space_key: string;
    title: string;
    version: number;
    content: string;
    updated_at: string;
    body_format?: string;
  }> {
    const q = spaceKey ? `?space_key=${encodeURIComponent(spaceKey)}` : '';
    return this.request(`/api/pages/${confluenceId}${q}`);
  }

  async comparePage(confluenceId: number, spaceKey?: string): Promise<{
    confluence_id: number;
    space_key: string;
    live: { version: number; when?: string; title?: string };
    stored: { version: number; updated_at: string; title?: string; body_format?: string } | null;
  }> {
    const q = spaceKey ? `?space_key=${encodeURIComponent(spaceKey)}` : '';
    return this.request(`/api/pages/${confluenceId}/compare${q}`);
  }

  async refreshPage(confluenceId: number, spaceKey?: string): Promise<{
    confluence_id: number;
    space_key: string;
    title: string;
    version: number;
    updated_at?: string;
  }> {
    return this.request(`/api/pages/${confluenceId}/refresh`, {
      method: 'POST',
      body: JSON.stringify(spaceKey ? { space_key: spaceKey } : {}),
    });
  }

  // Search
  async searchPages(query: string, spaceKey?: string): Promise<{
    query: string;
    count: number;
    results: {
      confluence_id: number;
      space_key: string;
      title: string;
      excerpt: string;
      similarity: number;
      file_path?: string;
      internal_id?: string;
    }[];
  }> {
    const params = new URLSearchParams({ q: query });
    if (spaceKey) params.set('space_key', spaceKey);
    return this.request(`/api/search?${params.toString()}`);
  }

  async getSearchStats(): Promise<{
    total_spaces: number;
    total_pages: number;
    content_indexing: string;
    last_crawled: string;
  }> {
    return this.request('/api/search/stats');
  }
}
