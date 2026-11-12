package embed

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// Backend computes embeddings inside the helper process.
type Backend interface {
	Info() Info
	// Embed returns one vector per text (any length; Serve normalizes).
	Embed(texts []string) ([][]float32, error)
}

// Serve runs the helper side of the protocol: it writes the hello frame for b
// (or loadErr, when the backend could not be created) and answers requests
// from r until r ends.
func Serve(r io.Reader, w io.Writer, b Backend, loadErr error) error {
	bw := bufio.NewWriter(w)
	send := func(resp Response) error {
		if err := WriteFrame(bw, resp); err != nil {
			return err
		}
		return bw.Flush()
	}
	if loadErr != nil {
		return send(Response{Error: loadErr.Error()})
	}
	info := b.Info()
	if err := send(Response{Info: &info}); err != nil {
		return err
	}

	br := bufio.NewReader(r)
	for {
		var req Request
		if err := ReadFrame(br, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		resp := Response{ID: req.ID}
		vecs, err := b.Embed(req.Texts)
		switch {
		case err != nil:
			resp.Error = err.Error()
		case len(vecs) != len(req.Texts):
			resp.Error = fmt.Sprintf("backend returned %d vectors for %d texts", len(vecs), len(req.Texts))
		default:
			resp.Vectors = make([][]byte, len(vecs))
			for i, v := range vecs {
				resp.Vectors[i] = EncodeVector(Normalize(v))
			}
		}
		if err := send(resp); err != nil {
			return err
		}
	}
}
