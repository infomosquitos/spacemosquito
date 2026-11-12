package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestInstallModel(t *testing.T) {
	body := []byte(strings.Repeat("gguf-bytes ", 300000)) // ~3 MB, several reads
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(body)
	}))
	defer srv.Close()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "models")

	// Wrong checksum: nothing left in dir.
	if _, _, err := installModel(ctx, dir, srv.URL, strings.Repeat("0", 64), int64(len(body)), nil); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("leftovers after failed download: %v", entries)
	}

	var last int64
	path, downloaded, err := installModel(ctx, dir, srv.URL, good, int64(len(body)), func(done, _ int64) { last = done })
	if err != nil || !downloaded || last != int64(len(body)) {
		t.Fatalf("install: %v downloaded=%v progress=%d", err, downloaded, last)
	}
	if got, _ := os.ReadFile(path); string(got) != string(body) {
		t.Fatal("content differs")
	}

	// Already installed: no download.
	before := hits
	if _, downloaded, err := installModel(ctx, dir, srv.URL, good, int64(len(body)), nil); err != nil || downloaded || hits != before {
		t.Fatalf("second install downloaded again: %v %v", downloaded, err)
	}
}

// The release workflow publishes the model from scripts/model-pins.sh; the
// binary must download that same file.
func TestModelPinsMatchScript(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "model-pins.sh"))
	if err != nil {
		t.Skipf("model-pins.sh not found: %v", err)
	}
	pins := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^(\w+)="([^"]*)"`).FindAllStringSubmatch(string(b), -1) {
		pins[m[1]] = m[2]
	}
	tag := strings.ReplaceAll(pins["MODEL_TAG"], "${MODEL_VERSION}", pins["MODEL_VERSION"])
	wantURL := "https://github.com/infomosquitos/spacemosquito/releases/download/" + tag + "/" + pins["FILE"]
	if ModelURL != wantURL || ModelSHA256 != pins["SHA256"] || ModelFile != pins["FILE"] {
		t.Fatalf("embed pins (%s, %s, %s) differ from model-pins.sh (%s, %s, %s)",
			ModelURL, ModelSHA256, ModelFile, wantURL, pins["SHA256"], pins["FILE"])
	}
	if size, err := strconv.Atoi(pins["SIZE"]); err != nil || size != ModelSize {
		t.Fatalf("SIZE in model-pins.sh = %q, ModelSize = %d", pins["SIZE"], ModelSize)
	}
}
