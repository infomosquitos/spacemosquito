//go:build llama

package embed

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Runs the real helper with llama.cpp and a model:
//
//	SPACEMOSQUITO_LLAMA_LIB=/path/to/lib SPACEMOSQUITO_LLAMA_MODEL=/path/to/bge-m3-q8_0.gguf \
//	  go test -tags=llama ./internal/embed -run Llama -v
func TestLlamaHelper(t *testing.T) {
	lib, model := os.Getenv("SPACEMOSQUITO_LLAMA_LIB"), os.Getenv("SPACEMOSQUITO_LLAMA_MODEL")
	if lib == "" || model == "" {
		t.Skip("SPACEMOSQUITO_LLAMA_LIB / SPACEMOSQUITO_LLAMA_MODEL not set")
	}
	helper := filepath.Join(t.TempDir(), "spacemosquito-embedder")
	build := exec.Command("go", "build", "-o", helper, "github.com/vkh/spacemosquito/cmd/spacemosquito-embedder")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}

	p := NewProcess(ProcessConfig{Path: helper, Args: []string{"-lib", lib, "-model", model}}, testLog())
	defer p.Close()
	ctx := context.Background()

	info, err := p.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("info: %+v", info)

	query := "how do we handle price updates from SAP"
	docs := []string{
		"ZCONDA -> PriceRow Import: condition records exported from SAP are imported nightly as price rows.",
		"Team offsite agenda: lunch at 12, hiking in the afternoon.",
	}
	vecs, err := p.Embed(ctx, append([]string{query}, docs...))
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || len(vecs[0]) != info.Dims {
		t.Fatalf("got %d vectors of %d dims", len(vecs), len(vecs[0]))
	}
	related, unrelated := dot(vecs[0], vecs[1]), dot(vecs[0], vecs[2])
	t.Logf("related %.3f, unrelated %.3f", related, unrelated)
	if related <= unrelated {
		t.Fatalf("related %.3f <= unrelated %.3f", related, unrelated)
	}
}
