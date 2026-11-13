-- Settings changed at runtime (e.g. from the browser extension); config.yaml
-- stays the place for startup configuration.
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
