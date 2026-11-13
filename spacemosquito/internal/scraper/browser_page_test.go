package scraper

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// A page that never finishes loading must fail after browserPageTimeout and
// leave the browser usable, instead of hanging the crawl (and later panicking).
// Needs a local Chrome/Chromium: SPACEMOSQUITO_BROWSER_TEST=1 go test -run BrowserPage ./internal/scraper
func TestBrowserPageHTML_hangingPageTimesOut(t *testing.T) {
	if os.Getenv("SPACEMOSQUITO_BROWSER_TEST") == "" {
		t.Skip("set SPACEMOSQUITO_BROWSER_TEST=1 to run with a local Chrome/Chromium")
	}
	bin, ok := launcher.LookPath()
	if !ok {
		t.Skip("no Chrome/Chromium found")
	}

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			fmt.Fprint(w, "<html><body>loading")
			w.(http.Flusher).Flush()
			select { // never finishes the response
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		fmt.Fprint(w, "<html><body><p>ok page</p></body></html>")
	}))
	defer srv.Close()
	defer close(release)

	u, err := buildLauncher(bin).Launch()
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer browser.Close()
	s := &Scraper{browser: browser}

	old := browserPageTimeout
	browserPageTimeout = 3 * time.Second
	defer func() { browserPageTimeout = old }()

	start := time.Now()
	if _, err := s.browserPageHTML(srv.URL + "/hang"); err == nil {
		t.Fatal("hanging page returned no error")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("hanging page took %v, want about browserPageTimeout", d)
	}

	html, err := s.browserPageHTML(srv.URL + "/ok")
	if err != nil || !strings.Contains(html, "ok page") {
		t.Fatalf("next page after a timeout: %v (%d bytes)", err, len(html))
	}
	pages, err := browser.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) > 1 { // the initial about:blank tab may remain
		t.Fatalf("%d tabs left open, want the fetched tabs closed", len(pages))
	}
}
