// Package llamacpp is the embed.Backend of spacemosquito-embedder: it loads
// llama.cpp's shared libraries at runtime through yzma (no CGO) and embeds
// texts with a GGUF embedding model.
//
// Only the helper binary may import this package: yzma's libffi dependency
// panics at init when libffi is missing (ADR-019).
package llamacpp

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/hybridgroup/yzma/pkg/llama"

	"github.com/vkh/spacemosquito/internal/embed"
)

// Options configure Open.
type Options struct {
	LibDir    string
	ModelPath string
	// GPULayers is the number of layers offloaded to the GPU; negative means
	// all of them on Apple Silicon (Metal) and none elsewhere.
	GPULayers int
	// ContextSize is the token window per text; longer texts are truncated.
	ContextSize int
	// Threads for CPU work; 0 lets llama.cpp choose.
	Threads int
	// Verbose keeps llama.cpp's own log output (stderr).
	Verbose bool
}

// Backend embeds texts one at a time with a single llama.cpp context.
type Backend struct {
	model llama.Model
	lctx  llama.Context
	vocab llama.Vocab
	mem   llama.Memory
	batch llama.Batch
	nEmbd int32
	nCtx  int
	info  embed.Info
}

// Open loads the libraries and the model.
func Open(opts Options) (*Backend, error) {
	if opts.ContextSize <= 0 {
		opts.ContextSize = 2048
	}
	gpuLayers := opts.GPULayers
	if gpuLayers < 0 {
		gpuLayers = 0
		if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
			gpuLayers = 999
		}
	}

	if err := llama.Load(opts.LibDir); err != nil {
		return nil, fmt.Errorf("load llama.cpp from %s: %w", opts.LibDir, err)
	}
	if !opts.Verbose {
		llama.LogSet(llama.LogSilent())
	}
	if err := llama.Init(); err != nil {
		return nil, fmt.Errorf("init llama.cpp: %w", err)
	}

	mp := llama.ModelDefaultParams()
	mp.NGpuLayers = int32(gpuLayers)
	model, err := llama.ModelLoadFromFile(opts.ModelPath, mp)
	if err == nil && model == 0 {
		err = fmt.Errorf("no model returned")
	}
	if err != nil {
		return nil, fmt.Errorf("load model %s: %w", opts.ModelPath, err)
	}

	cp := llama.ContextDefaultParams()
	cp.NCtx = uint32(opts.ContextSize)
	cp.NBatch = uint32(opts.ContextSize)
	cp.NUbatch = uint32(opts.ContextSize)
	cp.NSeqMax = 1
	cp.Embeddings = 1
	cp.PoolingType = llama.PoolingTypeUnspecified // the model's own (CLS for bge-m3)
	if opts.Threads > 0 {
		cp.NThreads = int32(opts.Threads)
		cp.NThreadsBatch = int32(opts.Threads)
	}
	lctx, err := llama.InitFromModel(model, cp)
	if err != nil {
		llama.ModelFree(model)
		return nil, fmt.Errorf("create context: %w", err)
	}
	if llama.GetPoolingType(lctx) == llama.PoolingTypeNone {
		llama.Free(lctx)
		llama.ModelFree(model)
		return nil, fmt.Errorf("model %s has no pooling; not an embedding model", opts.ModelPath)
	}
	mem, err := llama.GetMemory(lctx)
	if err != nil {
		mem = 0
	}

	b := &Backend{
		model: model,
		lctx:  lctx,
		vocab: llama.ModelGetVocab(model),
		mem:   mem,
		batch: llama.BatchInit(int32(opts.ContextSize), 0, 1),
		nEmbd: llama.ModelNEmbd(model),
		nCtx:  opts.ContextSize,
	}
	b.info = embed.Info{
		Model:       filepath.Base(opts.ModelPath),
		Dims:        int(b.nEmbd),
		Description: llama.ModelDesc(model),
		GPU:         gpuLayers > 0,
	}
	return b, nil
}

// Info describes the loaded model.
func (b *Backend) Info() embed.Info { return b.info }

// Embed returns one (unnormalized) vector per text.
func (b *Backend) Embed(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := b.embedOne(t)
		if err != nil {
			return nil, fmt.Errorf("text %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

func (b *Backend) embedOne(text string) ([]float32, error) {
	toks := llama.Tokenize(b.vocab, text, true, true)
	if len(toks) == 0 {
		return make([]float32, b.nEmbd), nil
	}
	if len(toks) > b.nCtx {
		// Keep the closing special token the model expects at the end.
		toks = append(toks[:b.nCtx-1], toks[len(toks)-1])
	}
	if b.mem != 0 {
		if err := llama.MemoryClear(b.mem, true); err != nil {
			return nil, err
		}
	}
	if err := b.batch.Clear(); err != nil {
		return nil, err
	}
	seq := []llama.SeqId{0}
	for p, tok := range toks {
		if err := b.batch.Add(tok, llama.Pos(p), seq, true); err != nil {
			return nil, err
		}
	}
	ret, err := llama.Decode(b.lctx, b.batch)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if ret != 0 {
		return nil, fmt.Errorf("decode returned %d", ret)
	}
	v, err := llama.GetEmbeddingsSeq(b.lctx, 0, b.nEmbd)
	if err != nil {
		return nil, err
	}
	return append([]float32(nil), v...), nil
}

// Close frees the context and the model.
func (b *Backend) Close() {
	llama.BatchFree(b.batch)
	llama.Free(b.lctx)
	llama.ModelFree(b.model)
	llama.Close()
}
