package search

import (
	"context"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// EvalCase is one question with the Confluence IDs of the pages that answer it.
type EvalCase struct {
	Question string `yaml:"question"`
	Space    string `yaml:"space,omitempty"`
	Expect   []int  `yaml:"expect"`
}

// EvalScore is recall@K and mean reciprocal rank (first expected page) over
// all cases, for one mode.
type EvalScore struct {
	Mode   Mode
	Recall float64 // share of cases with an expected page in the top K
	MRR    float64
	Missed []string // questions with no expected page in the top K
}

// LoadEvalCases reads a YAML list of EvalCase.
func LoadEvalCases(path string) ([]EvalCase, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []EvalCase
	if err := yaml.Unmarshal(b, &cases); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, c := range cases {
		if c.Question == "" || len(c.Expect) == 0 {
			return nil, fmt.Errorf("%s: case %d needs question and expect", path, i+1)
		}
	}
	return cases, nil
}

// Evaluate runs every case in each mode and scores the top k results.
func (s *Service) Evaluate(ctx context.Context, cases []EvalCase, modes []Mode, k int) ([]EvalScore, error) {
	var out []EvalScore
	for _, mode := range modes {
		score := EvalScore{Mode: mode}
		for _, c := range cases {
			res, err := s.Search(ctx, Request{Query: c.Question, SpaceKey: c.Space, Limit: k, Mode: mode})
			if err != nil {
				return nil, err
			}
			if res.Mode != mode {
				return nil, fmt.Errorf("mode %s unavailable (got %s); is the semantic index built?", mode, res.Mode)
			}
			rank := 0
			for i, r := range res.Results {
				for _, id := range c.Expect {
					if r.ConfluenceID == id {
						rank = i + 1
						break
					}
				}
				if rank > 0 {
					break
				}
			}
			if rank > 0 {
				score.Recall++
				score.MRR += 1 / float64(rank)
			} else {
				score.Missed = append(score.Missed, c.Question)
			}
		}
		if n := float64(len(cases)); n > 0 {
			score.Recall /= n
			score.MRR /= n
		}
		out = append(out, score)
	}
	return out, nil
}

// PrintEval writes a comparison table.
func PrintEval(w io.Writer, scores []EvalScore, k, cases int) {
	fmt.Fprintf(w, "%d questions, top %d\n\n%-9s  %9s  %6s\n", cases, k, "mode", fmt.Sprintf("recall@%d", k), "MRR")
	for _, s := range scores {
		fmt.Fprintf(w, "%-9s  %9.2f  %6.3f\n", s.Mode, s.Recall, s.MRR)
	}
	for _, s := range scores {
		if len(s.Missed) > 0 {
			fmt.Fprintf(w, "\nMissed by %s:\n", s.Mode)
			for _, q := range s.Missed {
				fmt.Fprintf(w, "  - %s\n", q)
			}
		}
	}
}
