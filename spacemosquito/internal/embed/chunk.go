package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Chunk sizes in bytes of Markdown; this catalog averages ~3.3 bytes per
// bge-m3 token, so the target is ~360 tokens and the hard limit ~600.
const (
	chunkTarget  = 1200
	chunkMax     = 2000
	overlapMax   = 400 // a trailing paragraph up to this size is repeated in the next chunk
	minSection   = 300 // smaller sections merge with the next one
	headingJoint = " > "
)

// Chunk is one embedded piece of a page.
type Chunk struct {
	Index int
	// HeadingPath is the section the chunk starts in, e.g. "Setup > Receiver".
	HeadingPath string
	// Text is the chunk's Markdown (shown as the search excerpt).
	Text string
	// Embed is what the model sees: title, heading path and Text.
	Embed string
	// Hash identifies Embed; unchanged chunks are not embedded again.
	Hash string
}

type blockKind int

const (
	blockText blockKind = iota
	blockHeading
	blockTable
	blockCode
)

type block struct {
	kind  blockKind
	text  string
	level int // headings
	title string
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	hrRe      = regexp.MustCompile(`^(-[ \t]*){3,}$|^(\*[ \t]*){3,}$|^(_[ \t]*){3,}$`)
	linkRe    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
)

// ChunkPage splits a page's Markdown into chunks. A page without content
// still gets one chunk (its title), so it can be found by meaning.
func ChunkPage(title, markdown string) []Chunk {
	title = strings.TrimSpace(title)
	var chunks []Chunk
	var path []string // heading titles by level
	var cur []string
	curSize := 0
	curPath := ""
	hasContent := false

	pathString := func() string {
		var parts []string
		for _, p := range path {
			if p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, headingJoint)
	}
	flush := func(keepOverlap bool) {
		if !hasContent {
			cur, curSize = nil, 0
			return
		}
		chunks = append(chunks, newChunk(len(chunks), title, curPath, strings.Join(cur, "\n\n")))
		var carry []string
		if keepOverlap && len(cur) > 0 {
			if last := cur[len(cur)-1]; len(last) <= overlapMax && !strings.HasPrefix(last, "#") {
				carry = []string{last}
			}
		}
		cur, curSize, hasContent = carry, 0, false
		for _, c := range carry {
			curSize += len(c)
		}
		curPath = pathString()
	}
	add := func(text string, content bool) {
		if len(cur) == 0 {
			curPath = pathString()
		}
		cur = append(cur, text)
		curSize += len(text) + 2
		hasContent = hasContent || content
	}

	for _, b := range splitBlocks(markdown) {
		if b.kind == blockHeading {
			if hasContent && curSize >= minSection {
				flush(false)
			}
			for len(path) < b.level {
				path = append(path, "")
			}
			path = append(path[:b.level-1], b.title)
			if !hasContent {
				// Nothing but headings so far: restart at this section.
				cur, curSize = nil, 0
			}
			add(b.text, false)
			continue
		}
		for _, piece := range splitOversized(b) {
			if hasContent && curSize+len(piece) > chunkTarget {
				flush(true)
			}
			add(piece, true)
		}
	}
	flush(false)

	if len(chunks) == 0 {
		chunks = append(chunks, newChunk(0, title, "", ""))
	}
	return chunks
}

func newChunk(i int, title, path, text string) Chunk {
	var b strings.Builder
	b.WriteString(title)
	if path != "" {
		b.WriteString("\n")
		b.WriteString(path)
	}
	if text != "" {
		b.WriteString("\n\n")
		b.WriteString(text)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return Chunk{Index: i, HeadingPath: path, Text: text, Embed: b.String(), Hash: hex.EncodeToString(sum[:])}
}

// splitBlocks cuts Markdown into headings, tables, fenced code and text
// paragraphs; horizontal rules are dropped.
func splitBlocks(md string) []block {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var blocks []block
	var para []string
	kind := blockText
	endPara := func() {
		if len(para) > 0 {
			if text := strings.TrimSpace(strings.Join(para, "\n")); text != "" {
				blocks = append(blocks, block{kind: kind, text: text})
			}
		}
		para, kind = nil, blockText
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			endPara()
			fence := trimmed[:3]
			code := []string{line}
			for i++; i < len(lines); i++ {
				code = append(code, lines[i])
				if strings.HasPrefix(strings.TrimSpace(lines[i]), fence) {
					break
				}
			}
			blocks = append(blocks, block{kind: blockCode, text: strings.Join(code, "\n")})
		case trimmed == "":
			endPara()
		case hrRe.MatchString(trimmed) && len(para) == 0:
			// horizontal rule
		case headingRe.MatchString(trimmed):
			endPara()
			m := headingRe.FindStringSubmatch(trimmed)
			if t := cleanHeading(m[2]); t != "" {
				blocks = append(blocks, block{kind: blockHeading, text: trimmed, level: len(m[1]), title: t})
			}
		case strings.HasPrefix(trimmed, "|"):
			if kind != blockTable {
				endPara()
				kind = blockTable
			}
			para = append(para, line)
		default:
			if kind == blockTable {
				endPara()
			}
			para = append(para, line)
		}
	}
	endPara()
	return blocks
}

func cleanHeading(s string) string {
	s = linkRe.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("**", "", "__", "", "`", "").Replace(s)
	return strings.TrimSpace(s)
}

// splitOversized breaks a block larger than chunkMax: tables by rows (the
// header repeated in each piece), anything else by lines, then bytes.
func splitOversized(b block) []string {
	if len(b.text) <= chunkMax {
		return []string{b.text}
	}
	lines := strings.Split(b.text, "\n")
	var header []string
	if b.kind == blockTable && len(lines) > 2 {
		header, lines = lines[:2], lines[2:]
	}
	headerSize := 0
	for _, h := range header {
		headerSize += len(h) + 1
	}
	var pieces []string
	cur := append([]string(nil), header...)
	size := headerSize
	emit := func() {
		if len(cur) > len(header) {
			pieces = append(pieces, strings.Join(cur, "\n"))
		}
		cur = append([]string(nil), header...)
		size = headerSize
	}
	for _, line := range lines {
		for len(line) > chunkTarget {
			emit()
			cut := runeCut(line, chunkTarget)
			pieces = append(pieces, line[:cut])
			line = line[cut:]
		}
		if size+len(line) > chunkTarget {
			emit()
		}
		cur = append(cur, line)
		size += len(line) + 1
	}
	emit()
	return pieces
}

// runeCut returns an index <= n that does not split a UTF-8 sequence.
func runeCut(s string, n int) int {
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return n
}
