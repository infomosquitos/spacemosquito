// spacemosquito-embedder computes embeddings for spacemosquito serve: it loads
// llama.cpp (-lib) and a GGUF embedding model (-model), then answers embed
// requests on stdin/stdout (internal/embed protocol). It is started by serve;
// running it by hand is only useful for debugging (see -check).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/vkh/spacemosquito/internal/embed"
	"github.com/vkh/spacemosquito/internal/embed/llamacpp"
)

func main() {
	var opts llamacpp.Options
	flag.StringVar(&opts.LibDir, "lib", "", "directory with the llama.cpp shared libraries")
	flag.StringVar(&opts.ModelPath, "model", "", "GGUF embedding model")
	flag.IntVar(&opts.GPULayers, "gpu-layers", -1, "layers to offload to the GPU (-1: all on Apple Silicon, none elsewhere)")
	flag.IntVar(&opts.ContextSize, "ctx", 2048, "tokens per text; longer texts are truncated")
	flag.IntVar(&opts.Threads, "threads", 0, "CPU threads (0: llama.cpp default)")
	flag.BoolVar(&opts.Verbose, "verbose", false, "keep llama.cpp log output on stderr")
	check := flag.String("check", "", "embed this text, print model info and timing, and exit")
	flag.Parse()

	if opts.LibDir == "" || opts.ModelPath == "" {
		fmt.Fprintln(os.Stderr, "usage: spacemosquito-embedder -lib DIR -model FILE.gguf [-check TEXT]")
		os.Exit(2)
	}

	start := time.Now()
	b, err := llamacpp.Open(opts)
	if *check != "" {
		os.Exit(runCheck(b, err, *check, time.Since(start)))
	}
	if err != nil {
		_ = embed.Serve(os.Stdin, os.Stdout, nil, err)
		os.Exit(1)
	}
	defer b.Close()
	if err := embed.Serve(os.Stdin, os.Stdout, b, nil); err != nil {
		fmt.Fprintln(os.Stderr, "spacemosquito-embedder:", err)
		os.Exit(1)
	}
}

func runCheck(b *llamacpp.Backend, err error, text string, load time.Duration) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer b.Close()
	info := b.Info()
	fmt.Printf("model: %s (%s), %d dims, gpu: %v, loaded in %v\n", info.Model, info.Description, info.Dims, info.GPU, load.Round(time.Millisecond))
	t := time.Now()
	vecs, err := b.Embed([]string{text})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	v := embed.Normalize(vecs[0])
	fmt.Printf("embedded in %v: [%.4f %.4f %.4f …]\n", time.Since(t).Round(time.Microsecond), v[0], v[1], v[2])
	return 0
}
