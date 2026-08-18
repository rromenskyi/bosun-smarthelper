package documents

import (
	"regexp"
	"strings"
)

// maxChunkChars bounds how large one chunk (and therefore one embedding
// request, and one line of a search result fed back to a weak local model)
// can get. Paragraphs already under this size are kept whole so real
// paragraph boundaries survive; only oversized paragraphs are split further,
// at sentence boundaries, so a chunk never cuts a sentence in half. This
// means chunks are uneven in size by design — see docs/memo-search.md.
const maxChunkChars = 1500

var (
	paragraphSplit = regexp.MustCompile(`\n\s*\n`)
	sentenceSplit  = regexp.MustCompile(`(?s)([.!?])\s+`)
)

// chunkText splits text into paragraph- or sentence-bounded pieces for
// embedding. Returns nil for blank input.
func chunkText(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var chunks []string
	for _, paragraph := range paragraphSplit.Split(text, -1) {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if len([]rune(paragraph)) <= maxChunkChars {
			chunks = append(chunks, paragraph)
			continue
		}
		chunks = append(chunks, splitBySentence(paragraph)...)
	}
	return chunks
}

// splitBySentence greedily packs whole sentences into chunks no larger than
// maxChunkChars.
func splitBySentence(paragraph string) []string {
	sentences := splitSentences(paragraph)
	var chunks []string
	var current strings.Builder
	for _, sentence := range sentences {
		if current.Len() > 0 && len([]rune(current.String()))+len([]rune(sentence)) > maxChunkChars {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString(" ")
		}
		current.WriteString(sentence)
	}
	if current.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(current.String()))
	}
	return chunks
}

// splitSentences keeps the terminating punctuation attached to its sentence.
func splitSentences(paragraph string) []string {
	indices := sentenceSplit.FindAllStringIndex(paragraph, -1)
	if len(indices) == 0 {
		return []string{paragraph}
	}
	sentences := make([]string, 0, len(indices)+1)
	start := 0
	for _, idx := range indices {
		sentences = append(sentences, strings.TrimSpace(paragraph[start:idx[0]+1]))
		start = idx[1]
	}
	if start < len(paragraph) {
		sentences = append(sentences, strings.TrimSpace(paragraph[start:]))
	}
	return sentences
}
