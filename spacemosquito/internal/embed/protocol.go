// Package embed computes text embeddings for semantic search (ADR-019).
//
// The model runs in a helper process, spacemosquito-embedder, that loads
// llama.cpp at runtime. serve talks to it over stdin/stdout with the frames
// defined here: a 4-byte big-endian length followed by that many bytes of
// JSON. The helper first writes a hello Response (ID 0) carrying Info, or an
// Error when the model cannot be loaded, then answers one Request at a time.
package embed

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

// maxFrame bounds a frame so a corrupt length cannot allocate gigabytes.
const maxFrame = 64 << 20

// Request asks the helper to embed Texts.
type Request struct {
	ID    uint64   `json:"id"`
	Texts []string `json:"texts"`
}

// Response answers the Request with the same ID. Vectors are L2-normalized
// float32 values, little-endian (EncodeVector), one per text and in order.
type Response struct {
	ID      uint64   `json:"id"`
	Info    *Info    `json:"info,omitempty"`
	Vectors [][]byte `json:"vectors,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Info describes the loaded model.
type Info struct {
	// Model identifies the model file (its base name); stored with each
	// chunk so a model change re-embeds everything.
	Model string `json:"model"`
	Dims  int    `json:"dims"`
	// Description is llama.cpp's model description, e.g. "bert 335M Q8_0".
	Description string `json:"description,omitempty"`
	// GPU is true when model layers run on a GPU (Metal).
	GPU bool `json:"gpu,omitempty"`
}

// WriteFrame writes v as one frame.
func WriteFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return fmt.Errorf("frame too large: %d bytes", len(b))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadFrame reads one frame into v. It returns io.EOF only when the stream
// ends cleanly between frames.
func ReadFrame(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return fmt.Errorf("frame too large: %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	return json.Unmarshal(b, v)
}

// EncodeVector packs v as little-endian float32 values.
func EncodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

// DecodeVector unpacks EncodeVector's output.
func DecodeVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vector length %d is not a multiple of 4", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

// Normalize scales v to unit length in place, so cosine similarity is a dot
// product. A zero vector is left as is.
func Normalize(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return v
	}
	n := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= n
	}
	return v
}
