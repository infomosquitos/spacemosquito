package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// The bundled model's release asset (ADR-019). Keep in step with
// scripts/model-pins.sh, which publishes it (TestModelPinsMatchScript).
const (
	ModelURL    = "https://github.com/infomosquitos/spacemosquito/releases/download/model-bge-m3-q8_0-v1/bge-m3-q8_0.gguf"
	ModelSHA256 = "aa473d51f451a22f0fcf39ba3330c14bed38a385712b1113440f69df4047a173"
	ModelSize   = 634553760
)

// InstallModel downloads the model into dir (created if needed) unless a
// file of the right size is already there, verifying its sha256 before
// moving it into place, so dir never holds a partial or corrupt model.
// progress, if set, is called with bytes received.
func InstallModel(ctx context.Context, dir string, progress func(done, total int64)) (string, bool, error) {
	return installModel(ctx, dir, ModelURL, ModelSHA256, ModelSize, progress)
}

func installModel(ctx context.Context, dir, url, wantSHA string, wantSize int64, progress func(done, total int64)) (string, bool, error) {
	path := filepath.Join(dir, ModelFile)
	if st, err := os.Stat(path); err == nil && st.Size() == wantSize {
		return path, false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("download model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("download model: %s", resp.Status)
	}

	part, err := os.CreateTemp(dir, ModelFile+".*.part")
	if err != nil {
		return "", false, err
	}
	defer os.Remove(part.Name()) // no-op after the rename

	h := sha256.New()
	var done int64
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := part.Write(buf[:n]); err != nil {
				part.Close()
				return "", false, err
			}
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil {
				progress(done, wantSize)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			part.Close()
			return "", false, fmt.Errorf("download model: %w", rerr)
		}
	}
	if err := part.Close(); err != nil {
		return "", false, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA || done != wantSize {
		return "", false, fmt.Errorf("downloaded model does not match: sha256 %s (%d bytes), want %s (%d bytes)", got, done, wantSHA, wantSize)
	}
	if err := os.Chmod(part.Name(), 0o644); err != nil {
		return "", false, err
	}
	if err := os.Rename(part.Name(), path); err != nil {
		return "", false, err
	}
	return path, true, nil
}
