package documents

import (
	"context"

	"github.com/roman220/bosun-smarthelper/internal/embeddings"
)

// AttachSummary reports what AttachOrphanedImages did.
type AttachSummary struct {
	Attached              int // image chunks merged into a matching text chunk
	Unmatched             int // image chunks left standalone (no good enough match found)
	EmptyDocumentsRemoved int // documents left with zero chunks (this run or a previous one), deleted
}

// chunkRef locates one chunk within a Store's documents.
type chunkRef struct {
	docID string
	index int
}

// AttachOrphanedImages finds every image chunk (ImageURL set) across every
// document and, if some text chunk anywhere in the store covers the same
// topic well enough — cosine similarity between the image chunk's own
// embedding (which already reflects its caption/breadcrumb text) and the
// text chunk's embedding, at or above minRelevance — merges the image onto
// that text chunk and removes the now-redundant standalone image chunk.
// An image chunk with no good match is left as-is, still searchable by
// whatever text it has on its own.
//
// This is deliberately generic: matching is by embedding similarity across
// the whole store, not by document title, a naming convention like
// "(Diagrams)", or any other assumption about how a particular batch of
// documents was produced — the same mechanism applies to any future
// upload shape. See docs/memo-search.md.
func (s *Store) AttachOrphanedImages(ctx context.Context, minRelevance float64) (AttachSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return AttachSummary{}, err
	}

	var imageRefs, textRefs []chunkRef
	for docID, record := range data.Documents {
		for i, chunk := range record.Chunks {
			switch {
			case chunk.ImageURL != "" && len(chunk.Embedding) > 0:
				imageRefs = append(imageRefs, chunkRef{docID, i})
			case chunk.ImageURL == "" && len(chunk.Embedding) > 0:
				textRefs = append(textRefs, chunkRef{docID, i})
			}
		}
	}

	var summary AttachSummary
	removals := make(map[string]map[int]bool)
	for _, ir := range imageRefs {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		imageChunk := data.Documents[ir.docID].Chunks[ir.index]

		bestScore := 0.0
		var bestRef chunkRef
		found := false
		for _, tr := range textRefs {
			textChunk := data.Documents[tr.docID].Chunks[tr.index]
			if textChunk.ImageURL != "" {
				continue // already has an image attached from an earlier match
			}
			if score := embeddings.CosineSimilarity(imageChunk.Embedding, textChunk.Embedding); score > bestScore {
				bestScore, bestRef, found = score, tr, true
			}
		}

		if !found || bestScore < minRelevance {
			summary.Unmatched++
			continue
		}

		record := data.Documents[bestRef.docID]
		record.Chunks[bestRef.index].ImageURL = imageChunk.ImageURL
		data.Documents[bestRef.docID] = record
		summary.Attached++

		if removals[ir.docID] == nil {
			removals[ir.docID] = make(map[int]bool)
		}
		removals[ir.docID][ir.index] = true
	}

	for docID, indices := range removals {
		record := data.Documents[docID]
		kept := make([]Chunk, 0, len(record.Chunks)-len(indices))
		for i, chunk := range record.Chunks {
			if !indices[i] {
				kept = append(kept, chunk)
			}
		}
		record.Chunks = kept
		data.Documents[docID] = record
	}

	// A document can end up with zero chunks — every one of its images
	// merged elsewhere, nothing else in it — either from this run or a
	// previous one (before this pruning existed). An empty document is
	// pure clutter: it can never contribute a search result, so leaving
	// the record around (still listed by List()/topics) only misleads
	// about what's actually searchable.
	for docID, record := range data.Documents {
		if len(record.Chunks) == 0 {
			delete(data.Documents, docID)
			summary.EmptyDocumentsRemoved++
		}
	}

	if err := s.save(data); err != nil {
		return summary, err
	}
	return summary, nil
}
