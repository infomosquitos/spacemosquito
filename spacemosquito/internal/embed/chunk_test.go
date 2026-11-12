package embed

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkPageSmallPageIsOneChunk(t *testing.T) {
	md := "## General Information\n\n---\n\n**JIRA:** [CCP-1049](https://x/browse/CCP-1049)\n\n### Results\n\n- [x] Local tests done\n"
	chunks := ChunkPage("Order Simulation", md)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks: %+v", len(chunks), chunks)
	}
	c := chunks[0]
	if c.HeadingPath != "General Information" {
		t.Errorf("path = %q", c.HeadingPath)
	}
	if strings.Contains(c.Text, "---") {
		t.Errorf("horizontal rule kept: %q", c.Text)
	}
	if !strings.HasPrefix(c.Embed, "Order Simulation\nGeneral Information\n\n") {
		t.Errorf("embed = %q", c.Embed)
	}
	if !strings.Contains(c.Text, "### Results") {
		t.Errorf("merged subsection missing: %q", c.Text)
	}
}

func TestChunkPageEmptyPageKeepsTitle(t *testing.T) {
	for _, md := range []string{"", "## Content\n\n---\n\n## Acceptance Criteria\n"} {
		chunks := ChunkPage("Release Notes 2026", md)
		if len(chunks) != 1 || chunks[0].Embed != "Release Notes 2026" || chunks[0].Text != "" {
			t.Errorf("%q: chunks = %+v", md, chunks)
		}
	}
}

func TestChunkPageSectionsAndHeadingPath(t *testing.T) {
	long := strings.Repeat("The receiver validates each message and stores it. ", 12) // ~600 bytes
	md := "# Setup\n\n" + long + "\n\n## Receiver\n\n" + long + "\n\n### Errors\n\n" + long + "\n\n# Operations\n\n" + long
	chunks := ChunkPage("Integration", md)
	var paths []string
	for _, c := range chunks {
		paths = append(paths, c.HeadingPath)
	}
	want := []string{"Setup", "Setup > Receiver", "Setup > Receiver > Errors", "Operations"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d has index %d", i, c.Index)
		}
	}
}

func TestChunkPageLongSectionSplitsWithOverlap(t *testing.T) {
	var paras []string
	for i := range 12 {
		paras = append(paras, fmt.Sprintf("Paragraph %d. %s", i, strings.Repeat("word ", 50)))
	}
	chunks := ChunkPage("Long", "## Details\n\n"+strings.Join(paras, "\n\n"))
	if len(chunks) < 3 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	for i, c := range chunks {
		if len(c.Text) > chunkMax {
			t.Errorf("chunk %d is %d bytes", i, len(c.Text))
		}
		if c.HeadingPath != "Details" {
			t.Errorf("chunk %d path = %q", i, c.HeadingPath)
		}
	}
	// The last paragraph of a chunk starts the next one.
	prevParas := strings.Split(chunks[0].Text, "\n\n")
	if last := prevParas[len(prevParas)-1]; !strings.HasPrefix(chunks[1].Text, last) {
		t.Errorf("no overlap: chunk 1 starts %q, chunk 0 ends %q", chunks[1].Text[:40], last[:40])
	}
}

func TestChunkPageTableKeptWholeOrSplitWithHeader(t *testing.T) {
	header := "| Key | Value |\n| --- | --- |"
	small := header + "\n| a | 1 |\n| b | 2 |"
	chunks := ChunkPage("T", "Intro text.\n\n"+small+"\n\nOutro.")
	if len(chunks) != 1 || !strings.Contains(chunks[0].Text, small) {
		t.Fatalf("small table not kept whole: %+v", chunks)
	}

	var rows []string
	for i := range 80 {
		rows = append(rows, fmt.Sprintf("| key-%02d | %s |", i, strings.Repeat("v", 40)))
	}
	big := header + "\n" + strings.Join(rows, "\n")
	chunks = ChunkPage("T", big)
	if len(chunks) < 2 {
		t.Fatalf("big table not split: %d chunks", len(chunks))
	}
	seen := 0
	for i, c := range chunks {
		if !strings.Contains(c.Text, header) {
			t.Errorf("chunk %d lacks the table header", i)
		}
		seen += strings.Count(c.Text, "| key-")
	}
	if seen < 80 {
		t.Errorf("rows lost: %d of 80", seen)
	}
}

func TestChunkPageCodeBlockNotSplitAtBlankLines(t *testing.T) {
	code := "```yaml\nreceiver:\n\n  port: 8080\n\n# not a heading\n```"
	chunks := ChunkPage("C", "## Config\n\n"+code)
	if len(chunks) != 1 || !strings.Contains(chunks[0].Text, code) {
		t.Fatalf("code block split: %+v", chunks)
	}
	if chunks[0].HeadingPath != "Config" {
		t.Errorf("comment in code taken as heading: %q", chunks[0].HeadingPath)
	}
}

func TestChunkPageHugeLineSplitsOnRuneBoundary(t *testing.T) {
	line := strings.Repeat("Größenänderung ", 400) // one 6 KB paragraph, multibyte
	for _, c := range ChunkPage("U", line) {
		if !utf8.ValidString(c.Text) {
			t.Fatalf("invalid UTF-8 in chunk %d", c.Index)
		}
		if len(c.Text) > chunkMax {
			t.Fatalf("chunk %d is %d bytes", c.Index, len(c.Text))
		}
	}
}

func TestChunkPageHashStable(t *testing.T) {
	md := "## A\n\ntext one\n\n## B\n\n" + strings.Repeat("text two ", 100)
	a, b := ChunkPage("P", md), ChunkPage("P", md)
	for i := range a {
		if a[i].Hash != b[i].Hash {
			t.Fatal("hash not deterministic")
		}
	}
	c := ChunkPage("P renamed", md)
	if c[0].Hash == a[0].Hash {
		t.Error("title change must change the hash (title is embedded)")
	}
}

func TestCleanHeading(t *testing.T) {
	for in, want := range map[string]string{
		"**Bold** heading":         "Bold heading",
		"[Link](https://x) `code`": "Link code",
		"🌱 Growth":                 "🌱 Growth",
	} {
		if got := cleanHeading(in); got != want {
			t.Errorf("cleanHeading(%q) = %q, want %q", in, got, want)
		}
	}
}
