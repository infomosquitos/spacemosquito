package embed

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/vkh/spacemosquito/pkg/logging"
)

// ErrUnavailable means the embedder cannot run right now (helper missing,
// model failed to load, helper crashed and is backing off). Callers fall
// back to keyword search and retry indexing later.
var ErrUnavailable = errors.New("embedder unavailable")

// Embedder turns texts into L2-normalized vectors.
type Embedder interface {
	Info(ctx context.Context) (Info, error)
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// ProcessConfig describes how to run the helper.
type ProcessConfig struct {
	Path string
	Args []string
	// Env is added to the current environment (e.g. LD_LIBRARY_PATH).
	Env []string
	// IdleTimeout stops the helper (freeing the model's memory) after this
	// long without requests; 0 keeps it running.
	IdleTimeout time.Duration
	// StartTimeout bounds loading the model; 0 means 2 minutes.
	StartTimeout time.Duration
}

const (
	minRestartBackoff = 30 * time.Second
	maxRestartBackoff = 10 * time.Minute
	stopGrace         = 3 * time.Second
)

// Process is an Embedder backed by a helper process, started on first use.
// Calls are serialized: the helper embeds one request at a time anyway.
type Process struct {
	cfg ProcessConfig
	log logging.Sugar
	now func() time.Time

	mu       sync.Mutex
	cmd      *exec.Cmd
	in       io.WriteCloser
	out      *bufio.Reader
	exited   chan struct{}
	info     Info
	nextID   uint64
	useGen   uint64
	idle     *time.Timer
	failures int
	retryAt  time.Time
	lastErr  error
	closed   bool
}

// NewProcess returns a Process for cfg; nothing starts until the first call.
func NewProcess(cfg ProcessConfig, log logging.Sugar) *Process {
	if cfg.StartTimeout == 0 {
		cfg.StartTimeout = 2 * time.Minute
	}
	return &Process{cfg: cfg, log: log, now: time.Now}
}

// Info returns the model description, starting the helper if it never ran.
func (p *Process) Info(ctx context.Context) (Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.info.Model != "" {
		return p.info, nil
	}
	if err := p.startLocked(ctx); err != nil {
		return Info{}, err
	}
	p.touchLocked()
	return p.info, nil
}

// Embed returns one normalized vector per text.
func (p *Process) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.startLocked(ctx); err != nil {
		return nil, err
	}
	defer p.touchLocked()

	p.nextID++
	req := Request{ID: p.nextID, Texts: texts}
	resp, err := p.roundTripLocked(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			// The helper may be busy with the abandoned request; restart it.
			p.stopLocked()
			return nil, ctx.Err()
		}
		p.failLocked(err)
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("embed: %s", resp.Error)
	}
	if resp.ID != req.ID || len(resp.Vectors) != len(texts) {
		err := fmt.Errorf("unexpected response (id %d/%d, %d vectors for %d texts)", resp.ID, req.ID, len(resp.Vectors), len(texts))
		p.failLocked(err)
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	p.failures = 0
	out := make([][]float32, len(resp.Vectors))
	for i, b := range resp.Vectors {
		v, err := DecodeVector(b)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// Close stops the helper; later calls fail with ErrUnavailable.
func (p *Process) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.stopLocked()
	return nil
}

// Stop ends the helper now; unlike Close, the next Embed starts it again.
func (p *Process) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

// Running reports whether the helper process is up (for status output).
func (p *Process) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

func (p *Process) startLocked(ctx context.Context) error {
	if p.closed {
		return fmt.Errorf("%w: closed", ErrUnavailable)
	}
	if p.cmd != nil {
		select {
		case <-p.exited:
			p.failLocked(errors.New("helper exited"))
		default:
			return nil
		}
	}
	if p.now().Before(p.retryAt) {
		return fmt.Errorf("%w: %v (retrying after %s)", ErrUnavailable, p.lastErr, p.retryAt.Format(time.TimeOnly))
	}

	cmd := exec.Command(p.cfg.Path, p.cfg.Args...)
	cmd.Env = append(os.Environ(), p.cfg.Env...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return p.startFailed(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return p.startFailed(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return p.startFailed(err)
	}
	if err := cmd.Start(); err != nil {
		return p.startFailed(err)
	}
	exited := make(chan struct{})
	go p.logStderr(stderr)
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	p.cmd, p.in, p.out, p.exited = cmd, in, bufio.NewReader(out), exited

	startCtx, cancel := context.WithTimeout(ctx, p.cfg.StartTimeout)
	defer cancel()
	hello, err := p.readLocked(startCtx)
	if err == nil && hello.Error != "" {
		err = errors.New(hello.Error)
	}
	if err == nil && (hello.Info == nil || hello.Info.Dims <= 0) {
		err = errors.New("helper sent no model info")
	}
	if err != nil {
		p.stopLocked()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return p.startFailed(err)
	}
	p.info = *hello.Info
	p.lastErr = nil
	if p.log.Enabled() {
		p.log.Infow("embedder started", "model", p.info.Model, "dims", p.info.Dims,
			"description", p.info.Description, "gpu", p.info.GPU, "pid", cmd.Process.Pid)
	}
	return nil
}

func (p *Process) startFailed(err error) error {
	p.failLocked(err)
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// failLocked stops the helper and schedules the next start: immediately after
// a first failure, then with doubling backoff while failures repeat.
func (p *Process) failLocked(err error) {
	p.stopLocked()
	p.failures++
	p.lastErr = err
	backoff := time.Duration(0)
	if p.failures > 1 {
		backoff = minRestartBackoff << (p.failures - 2)
		if backoff > maxRestartBackoff || backoff <= 0 {
			backoff = maxRestartBackoff
		}
	}
	p.retryAt = p.now().Add(backoff)
	if p.log.Enabled() {
		p.log.Warnw("embedder failed", "error", err, "failures", p.failures, "retry_in", backoff)
	}
}

func (p *Process) roundTripLocked(ctx context.Context, req Request) (Response, error) {
	if err := WriteFrame(p.in, req); err != nil {
		return Response{}, err
	}
	return p.readLocked(ctx)
}

// readLocked reads one frame; when ctx ends first it kills the helper so the
// reading goroutine returns.
func (p *Process) readLocked(ctx context.Context) (Response, error) {
	type result struct {
		resp Response
		err  error
	}
	ch := make(chan result, 1)
	out := p.out
	go func() {
		var r Response
		err := ReadFrame(out, &r)
		ch <- result{r, err}
	}()
	select {
	case r := <-ch:
		if errors.Is(r.err, io.EOF) {
			r.err = errors.New("helper exited")
		}
		return r.resp, r.err
	case <-ctx.Done():
		p.stopLocked()
		<-ch
		return Response{}, ctx.Err()
	}
}

// stopLocked closes stdin (the helper exits at EOF) and kills it if it does
// not exit within stopGrace.
func (p *Process) stopLocked() {
	if p.cmd == nil {
		return
	}
	_ = p.in.Close()
	select {
	case <-p.exited:
	case <-time.After(stopGrace):
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
	p.cmd, p.in, p.out, p.exited = nil, nil, nil, nil
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
}

// touchLocked restarts the idle timer after a call.
func (p *Process) touchLocked() {
	if p.cfg.IdleTimeout <= 0 || p.cmd == nil {
		return
	}
	p.useGen++
	gen := p.useGen
	if p.idle != nil {
		p.idle.Stop()
	}
	p.idle = time.AfterFunc(p.cfg.IdleTimeout, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.useGen == gen && p.cmd != nil {
			if p.log.Enabled() {
				p.log.Infow("embedder idle, stopping", "idle", p.cfg.IdleTimeout)
			}
			p.stopLocked()
		}
	})
}

func (p *Process) logStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if p.log.Enabled() {
			p.log.Infow("embedder", "stderr", sc.Text())
		}
	}
}
