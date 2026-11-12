package embed

import (
	"bytes"
	"context"
	"errors"
	"hash/fnv"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vkh/spacemosquito/pkg/logging"
)

// The test binary doubles as a fake helper: with EMBED_FAKE_HELPER set it
// serves fakeBackend on stdin/stdout instead of running tests.
func TestMain(m *testing.M) {
	switch os.Getenv("EMBED_FAKE_HELPER") {
	case "":
		os.Exit(m.Run())
	case "load-error":
		_ = Serve(os.Stdin, os.Stdout, nil, errors.New("model not found"))
	default:
		_ = Serve(os.Stdin, os.Stdout, fakeBackend{}, nil)
	}
	os.Exit(0)
}

// fakeBackend returns a deterministic 4-dim vector per text; the text "crash"
// kills the helper and "fail" returns an error.
type fakeBackend struct{}

func (fakeBackend) Info() Info { return Info{Model: "fake.gguf", Dims: 4} }

func (fakeBackend) Embed(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		switch t {
		case "crash":
			os.Exit(3)
		case "fail":
			return nil, errors.New("bad input")
		}
		h := fnv.New32a()
		h.Write([]byte(t))
		s := h.Sum32()
		out[i] = []float32{float32(s & 0xff), float32(s >> 8 & 0xff), float32(s >> 16 & 0xff), 1}
	}
	return out, nil
}

func fakeProcess(t *testing.T, mode string) *Process {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := NewProcess(ProcessConfig{
		Path:         exe,
		Env:          []string{"EMBED_FAKE_HELPER=" + mode},
		StartTimeout: 10 * time.Second,
	}, testLog())
	t.Cleanup(func() { p.Close() })
	return p
}

func TestProcessEmbed(t *testing.T) {
	p := fakeProcess(t, "ok")
	ctx := context.Background()

	info, err := p.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "fake.gguf" || info.Dims != 4 {
		t.Fatalf("info = %+v", info)
	}

	vecs, err := p.Embed(ctx, []string{"alpha", "beta", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || len(vecs[0]) != 4 {
		t.Fatalf("got %d vectors", len(vecs))
	}
	if d := dot(vecs[0], vecs[2]); d < 0.9999 {
		t.Fatalf("same text, dot = %v", d)
	}
	if d := dot(vecs[0], vecs[0]); d < 0.9999 || d > 1.0001 {
		t.Fatalf("not normalized: |v|² = %v", d)
	}
}

func TestProcessBackendErrorKeepsHelper(t *testing.T) {
	p := fakeProcess(t, "ok")
	ctx := context.Background()
	if _, err := p.Embed(ctx, []string{"fail"}); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want a plain backend error", err)
	}
	if !p.Running() {
		t.Fatal("helper stopped after a backend error")
	}
	if _, err := p.Embed(ctx, []string{"ok"}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessRestartsAfterCrash(t *testing.T) {
	p := fakeProcess(t, "ok")
	ctx := context.Background()
	if _, err := p.Embed(ctx, []string{"crash"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	// First failure: restart right away.
	if _, err := p.Embed(ctx, []string{"after"}); err != nil {
		t.Fatalf("after one crash: %v", err)
	}
	// Repeated failures back off.
	if _, err := p.Embed(ctx, []string{"crash"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := p.Embed(ctx, []string{"after"}); err != nil {
		t.Fatalf("crash after a success counts as first failure again: %v", err)
	}
}

func TestProcessLoadErrorBacksOff(t *testing.T) {
	p := fakeProcess(t, "load-error")
	ctx := context.Background()
	_, err := p.Embed(ctx, []string{"x"})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("err = %v", err)
	}
	// Second failure in a row schedules a backoff.
	if _, err := p.Embed(ctx, []string{"x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := p.Embed(ctx, []string{"x"}); err == nil || !strings.Contains(err.Error(), "retrying after") {
		t.Fatalf("err = %v, want backoff", err)
	}
	// Once the backoff has passed, it tries again.
	p.now = func() time.Time { return time.Now().Add(maxRestartBackoff) }
	if _, err := p.Embed(ctx, []string{"x"}); err == nil || strings.Contains(err.Error(), "retrying after") {
		t.Fatalf("err = %v, want a new start attempt", err)
	}
}

func TestProcessMissingBinary(t *testing.T) {
	p := NewProcess(ProcessConfig{Path: "/nonexistent/spacemosquito-embedder"}, testLog())
	if _, err := p.Embed(context.Background(), []string{"x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestProcessIdleStop(t *testing.T) {
	p := fakeProcess(t, "ok")
	p.cfg.IdleTimeout = 50 * time.Millisecond
	if _, err := p.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.Running() {
		if time.Now().After(deadline) {
			t.Fatal("helper still running after idle timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := p.Embed(context.Background(), []string{"y"}); err != nil {
		t.Fatalf("restart after idle stop: %v", err)
	}
}

func TestProcessContextCancel(t *testing.T) {
	p := fakeProcess(t, "ok")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Embed(ctx, []string{"x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := p.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
}

func TestServeLengthMismatch(t *testing.T) {
	var in, out bytes.Buffer
	if err := WriteFrame(&in, Request{ID: 7, Texts: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := Serve(&in, &out, shortBackend{}, nil); err != nil {
		t.Fatal(err)
	}
	var hello, resp Response
	if err := ReadFrame(&out, &hello); err != nil || hello.Info == nil {
		t.Fatalf("hello = %+v, %v", hello, err)
	}
	if err := ReadFrame(&out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 7 || !strings.Contains(resp.Error, "1 vectors for 2 texts") {
		t.Fatalf("resp = %+v", resp)
	}
	if err := ReadFrame(&out, &resp); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing frame: %v", err)
	}
}

type shortBackend struct{}

func (shortBackend) Info() Info { return Info{Model: "m", Dims: 1} }
func (shortBackend) Embed([]string) ([][]float32, error) {
	return [][]float32{{1}}, nil
}

func TestVectorRoundTrip(t *testing.T) {
	v := []float32{0.25, -1.5, 3e-7, 0}
	got, err := DecodeVector(EncodeVector(v))
	if err != nil {
		t.Fatal(err)
	}
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("got %v, want %v", got, v)
		}
	}
	if _, err := DecodeVector([]byte{1, 2, 3}); err == nil {
		t.Fatal("odd length accepted")
	}
}

func testLog() logging.Sugar { return logging.Sugar{} }

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
