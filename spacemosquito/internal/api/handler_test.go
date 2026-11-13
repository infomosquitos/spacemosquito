package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/scraper"
	"github.com/vkh/spacemosquito/internal/session"
	"github.com/vkh/spacemosquito/pkg/logger"
	"github.com/vkh/spacemosquito/pkg/logging"
)

func testLogger(t *testing.T) logging.Sugar {
	t.Helper()
	zl, err := logger.NewProduction(nil)
	if err != nil {
		t.Fatal(err)
	}
	return logging.New("api", zl)
}

func testConfig(key string) *config.Config {
	return &config.Config{
		Session: config.SessionConfig{
			EncryptionKey: key,
			FilePath:      "",
		},
		MCP: config.MCPConfig{Timeout: 10},
	}
}

func TestCORSMiddleware(t *testing.T) {
	called := false
	handler := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), logging.Sugar{})

	t.Run("OPTIONS preflight", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodOptions, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if called {
			t.Fatal("inner handler should not run for OPTIONS")
		}
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d", rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Error("missing CORS header")
		}
	})

	t.Run("GET passes through", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if !called {
			t.Fatal("inner handler should run")
		}
		if rec.Header().Get("Access-Control-Allow-Methods") == "" {
			t.Error("missing Allow-Methods header")
		}
	})
}

func TestLoggingMiddleware_smoke(t *testing.T) {
	zl, err := logger.NewProduction(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}), logging.New("http", zl))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestHandler_CreateSession_validation(t *testing.T) {
	store := session.NewStore(t.TempDir()+"/session.enc", logging.Sugar{})
	h := New(store, testConfig("12345678901234567890123456789012"), testLogger(t))

	tests := []struct {
		name       string
		method     string
		body       string
		wantStatus int
	}{
		{"bad json", http.MethodPost, "{", http.StatusBadRequest},
		{"missing url", http.MethodPost, `{"cookies":[{"name":"a","value":"b"}]}`, http.StatusBadRequest},
		{"no cookies", http.MethodPost, `{"confluence_url":"https://x.atlassian.net"}`, http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/session", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.CreateSession(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d, body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestHandler_CreateSession_missingEncryptionKey(t *testing.T) {
	store := session.NewStore(t.TempDir()+"/session.enc", logging.Sugar{})
	h := New(store, testConfig(""), testLogger(t))

	body := `{"confluence_url":"https://x.atlassian.net","cookies":[{"name":"a","value":"b"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSession(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_DeleteSession_noSession(t *testing.T) {
	store := session.NewStore(t.TempDir()+"/session.enc", logging.Sugar{})
	h := New(store, testConfig("12345678901234567890123456789012"), testLogger(t))

	req := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	rec := httptest.NewRecorder()
	h.DeleteSession(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestSearchHandler_validation(t *testing.T) {
	h := NewSearchHandler(nil, nil, testConfig(""), testLogger(t))

	t.Run("missing q", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/search", nil)
		rec := httptest.NewRecorder()
		h.Search(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/search?q=x", nil)
		rec := httptest.NewRecorder()
		h.Search(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("reindex confluence_id without space_key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/search/reindex?confluence_id=1", nil)
		rec := httptest.NewRecorder()
		h.Reindex(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestSemanticSetting_unavailable(t *testing.T) {
	h := NewSearchHandler(nil, nil, testConfig(""), testLogger(t))
	h.SemanticUnavailable = "turned off in config.yaml (embedder.provider: none)"

	rec := httptest.NewRecorder()
	h.SemanticSettingGet(rec, httptest.NewRequest(http.MethodGet, "/api/settings/semantic-search", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":false`) || !strings.Contains(rec.Body.String(), "config.yaml") {
		t.Errorf("get: %d %s", rec.Code, rec.Body)
	}
	for body, want := range map[string]int{`{"enabled":false}`: http.StatusConflict, `{}`: http.StatusBadRequest, `nope`: http.StatusBadRequest} {
		rec := httptest.NewRecorder()
		h.SemanticSettingUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/settings/semantic-search", strings.NewReader(body)))
		if rec.Code != want {
			t.Errorf("post %s: %d, want %d", body, rec.Code, want)
		}
	}
}

func TestCrawlHandler_validation(t *testing.T) {
	mgr := scraper.NewJobManager(testConfig(""), nil, nil, nil, nil, testLogger(t))
	h := NewCrawlHandler(mgr, testConfig(""), testLogger(t))

	t.Run("create missing space_url", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/crawl", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("create bad json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/crawl", strings.NewReader("{"))
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("status missing id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/crawl/status", nil)
		rec := httptest.NewRecorder()
		h.Status(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("status unknown job", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/crawl/status?id=missing", nil)
		rec := httptest.NewRecorder()
		h.Status(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("cancel missing id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/crawl/cancel", nil)
		rec := httptest.NewRecorder()
		h.Cancel(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("cancel body alone does not work", func(t *testing.T) {
		job, err := mgr.CreateJob("https://example.atlassian.net/wiki/spaces/X")
		if err != nil {
			t.Fatal(err)
		}
		body := `{"job_id":"` + job.ID + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/crawl/cancel", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.Cancel(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (query id required)", rec.Code)
		}
		got, _ := mgr.GetJob(job.ID)
		if got.Status != scraper.JobStatusPending {
			t.Errorf("status = %q, want still pending", got.Status)
		}
	})

	t.Run("cancel with query id", func(t *testing.T) {
		job, err := mgr.CreateJob("https://example.atlassian.net/wiki/spaces/Y")
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/crawl/cancel?id="+job.ID, nil)
		rec := httptest.NewRecorder()
		h.Cancel(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		got, err := mgr.GetJob(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != scraper.JobStatusCancelled {
			t.Errorf("status = %q, want cancelled", got.Status)
		}
	})

	t.Run("create method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/crawl", nil)
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestSpacesHandler_validation(t *testing.T) {
	h := SpacesHandler(nil, "", testLogger(t))

	t.Run("POST invalid json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/spaces", strings.NewReader("{"))
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("POST missing url", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/spaces", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("POST invalid confluence url", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"url": "https://example.com/no-space"})
		req := httptest.NewRequest(http.MethodPost, "/api/spaces", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unsupported method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/spaces", nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestSpaceByIDHandler_validation(t *testing.T) {
	h := SpaceByIDHandler(nil, "", nil, nil, logging.Sugar{})

	req := httptest.NewRequest(http.MethodGet, "/api/spaces/", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestSpacePagesHandler_validation(t *testing.T) {
	h := SpacePagesHandler(nil, testConfig(""), testLogger(t))

	t.Run("missing space key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/spaces//pages", nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("invalid limit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/spaces/PROJ/pages?limit=bad", nil)
		req.SetPathValue("key", "PROJ")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("invalid include_content", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/spaces/PROJ/pages?include_content=bad", nil)
		req.SetPathValue("key", "PROJ")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/spaces/PROJ/pages", nil)
		req.SetPathValue("key", "PROJ")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestHandler_SessionStatus_requiresURL(t *testing.T) {
	key := "12345678901234567890123456789012"
	store := session.NewStore(t.TempDir()+"/session.enc", logging.Sugar{})
	h := New(store, testConfig(key), testLogger(t))
	if err := store.Upsert(&session.Session{
		ConfluenceURL: "https://wiki.example.com",
		Cookies:       []session.Cookie{{Name: "JSESSIONID", Value: "x"}},
		CapturedAt:    time.Now(),
	}, key); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/session/status", nil)
	rec := httptest.NewRecorder()
	h.SessionStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["valid"] != false || body["message"] != "url query parameter is required" {
		t.Fatalf("body = %v", body)
	}
}

func TestHandler_ValidateSession_requiresURL(t *testing.T) {
	key := "12345678901234567890123456789012"
	store := session.NewStore(t.TempDir()+"/session.enc", logging.Sugar{})
	h := New(store, testConfig(key), testLogger(t))
	req := httptest.NewRequest(http.MethodPost, "/api/session/validate", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	h.ValidateSession(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_SessionStatus_TTL(t *testing.T) {
	key := "12345678901234567890123456789012"
	store := session.NewStore(t.TempDir()+"/session.enc", logging.Sugar{})
	h := New(store, testConfig(key), testLogger(t))

	fresh := time.Now().Add(-30 * time.Minute)
	stale := time.Now().Add(-61 * time.Minute)

	t.Run("valid within 60m", func(t *testing.T) {
		if err := store.Upsert(&session.Session{
			ConfluenceURL: "https://wiki.example.com",
			Cookies:       []session.Cookie{{Name: "JSESSIONID", Value: "x"}},
			CapturedAt:    time.Now(),
			ValidatedAt:   &fresh,
		}, key); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/session/status?url=https://wiki.example.com", nil)
		rec := httptest.NewRecorder()
		h.SessionStatus(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["valid"] != true {
			t.Fatalf("body = %v", body)
		}
	})

	t.Run("invalid after 60m", func(t *testing.T) {
		if err := store.Upsert(&session.Session{
			ConfluenceURL: "https://wiki.example.com",
			Cookies:       []session.Cookie{{Name: "JSESSIONID", Value: "x"}},
			CapturedAt:    time.Now(),
			ValidatedAt:   &stale,
		}, key); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/session/status?url=https://wiki.example.com", nil)
		rec := httptest.NewRecorder()
		h.SessionStatus(rec, req)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["valid"] != false {
			t.Fatalf("body = %v", body)
		}
		if body["message"] != "session validation expired" {
			t.Fatalf("message = %v", body["message"])
		}
	})
}

func TestHandler_ValidateSession_clearsValidatedAt(t *testing.T) {
	key := "12345678901234567890123456789012"
	store := session.NewStore(t.TempDir()+"/session.enc", logging.Sugar{})
	h := New(store, testConfig(key), testLogger(t))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	prev := time.Now().Add(-10 * time.Minute)
	if err := store.Upsert(&session.Session{
		ConfluenceURL: srv.URL,
		Cookies:       []session.Cookie{{Name: "JSESSIONID", Value: "dead"}},
		CapturedAt:    time.Now(),
		ValidatedAt:   &prev,
		Flavor:        session.FlavorServer,
	}, key); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"confluence_url": srv.URL})
	req := httptest.NewRequest(http.MethodPost, "/api/session/validate", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ValidateSession(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["valid"] != false {
		t.Fatalf("result = %v", result)
	}

	got, err := store.GetForURL(key, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.ValidatedAt != nil {
		t.Fatalf("ValidatedAt still set: %v", got.ValidatedAt)
	}

	// Status must stay invalid after failed validate
	statusReq := httptest.NewRequest(http.MethodGet, "/api/session/status?url="+srv.URL, nil)
	statusRec := httptest.NewRecorder()
	h.SessionStatus(statusRec, statusReq)
	var status map[string]any
	_ = json.Unmarshal(statusRec.Body.Bytes(), &status)
	if status["valid"] != false {
		t.Fatalf("status after failed validate = %v", status)
	}
}
