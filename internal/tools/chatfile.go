package tools

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/roman220/bosun-smarthelper/internal/chatfiles"
	"github.com/roman220/bosun-smarthelper/internal/documents"
)

// maxReadableChatFileBytes bounds how much of an attached text file's
// content the "read" action returns straight into the tool result — this
// is a personal-appliance chat compose bar, not a bulk document loader
// (internal/filedump/RAG already exists for that), so a request to read
// something large is almost certainly a mistake, not a real use case
// worth spending context on.
const maxReadableChatFileBytes = 200_000

// ChatFileTool lets the model act on a file the user just attached
// directly to a chat message (see internal/chatfiles) — add it to the
// document search index, or link it to a memo — without any
// system-prompt changes: discovered only through this tool's own
// name/description and the user's own message mentioning the
// attachment, the same way any other tool is discovered.
type ChatFileTool struct {
	files *chatfiles.Store
	docs  *documents.Store
	memo  *MemoTool
}

// NewChatFileTool wires everything the tool's actions need. docs/memo may
// be nil (matching how other optional features degrade elsewhere in this
// codebase) — the corresponding action just returns a clear error instead
// of the tool failing to register at all. add_to_memo's actual filedump
// write happens inside memo.AttachFile, which holds its own
// *filedump.Store — this tool never needs one directly.
func NewChatFileTool(files *chatfiles.Store, docs *documents.Store, memo *MemoTool) *ChatFileTool {
	return &ChatFileTool{files: files, docs: docs, memo: memo}
}

func (t *ChatFileTool) Name() string { return "chat_file" }

func (t *ChatFileTool) Description() string {
	return "Work with a file the user just attached to this chat message (mentioned in their message as an attachment, not a document upload). " +
		"Call \"list\" first to see what's attached and get the exact filename. " +
		"\"read\" returns a small text file's content directly (txt/csv/markdown/json only) so you can discuss it or fold it into a memo yourself with the memo tool. " +
		"\"add_to_rag\" ingests any file — photo, PDF, or text — into the searchable document index; always ask the user what title (and optionally folder) to use first, never guess. " +
		"\"add_to_memo\" links the file to an existing memo (write the memo first with the memo tool if it doesn't exist yet) so it's shown alongside that note. " +
		"A file not claimed one of these ways is deleted automatically after about an hour."
}

func (t *ChatFileTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"list", "read", "add_to_rag", "add_to_memo"},
				"description": "list: show attached files. read: return a small text file's content. " +
					"add_to_rag: ingest into document search. add_to_memo: link to an existing memo.",
			},
			"filename": map[string]any{
				"type":        "string",
				"description": "Exact name of the attached file, from \"list\" — required for read/add_to_rag/add_to_memo.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Document title for add_to_rag — ask the user, don't guess.",
			},
			"folder": map[string]any{
				"type":        "string",
				"description": "Optional folder/topic for add_to_rag, e.g. \"manuals/generator\".",
			},
			"memo_key": map[string]any{
				"type":        "string",
				"description": "Existing memo's key for add_to_memo — write the memo first if it doesn't exist yet.",
			},
			"ocr_language": map[string]any{
				"type":        "string",
				"description": "Optional tesseract language code for add_to_rag on an image/PDF, e.g. \"eng\", \"rus\". Defaults to English.",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

func (t *ChatFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	if t.files == nil {
		return nil, fmt.Errorf("chat file attachments are not configured")
	}
	sessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no chat session available for file attachments")
	}
	action, _ := args["action"].(string)

	switch action {
	case "list":
		return t.list(sessionID)
	case "read":
		return t.read(sessionID, args)
	case "add_to_rag":
		return t.addToRAG(ctx, sessionID, args)
	case "add_to_memo":
		return t.addToMemo(sessionID, args)
	default:
		return nil, fmt.Errorf("unsupported chat_file action %q", action)
	}
}

func (t *ChatFileTool) list(sessionID string) (any, error) {
	files, err := t.files.List(sessionID)
	if err != nil {
		return nil, err
	}
	views := make([]map[string]any, len(files))
	for i, f := range files {
		views[i] = map[string]any{"name": f.Name, "size_bytes": f.Size}
	}
	return map[string]any{"files": views}, nil
}

func filenameArg(args map[string]any) (string, error) {
	filename, _ := args["filename"].(string)
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return "", fmt.Errorf(`filename is required — call "list" first to see what's attached`)
	}
	return filename, nil
}

func (t *ChatFileTool) read(sessionID string, args map[string]any) (any, error) {
	filename, err := filenameArg(args)
	if err != nil {
		return nil, err
	}
	content, err := t.files.Read(sessionID, filename)
	if err != nil {
		return nil, err
	}
	if len(content) > maxReadableChatFileBytes {
		return nil, fmt.Errorf("%q is too large to read directly (%d bytes) — use add_to_rag instead", filename, len(content))
	}
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("%q doesn't look like a text file — use add_to_rag instead", filename)
	}
	return map[string]any{"filename": filename, "content": string(content)}, nil
}

// addToRAG dispatches on content, the same PDF/image/plain-text
// classification internal/webui/filedump.go's handleFileDumpUpload
// already uses — via internal/documents' exported ingestion functions,
// so both paths share exactly one implementation.
func (t *ChatFileTool) addToRAG(ctx context.Context, sessionID string, args map[string]any) (any, error) {
	if t.docs == nil {
		return nil, fmt.Errorf("document search is not configured")
	}
	filename, err := filenameArg(args)
	if err != nil {
		return nil, err
	}
	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("title is required — ask the user what to call this before calling add_to_rag")
	}
	folder, _ := args["folder"].(string)
	folder = strings.TrimSpace(folder)
	ocrLanguage, _ := args["ocr_language"].(string)
	ocrLanguage = strings.TrimSpace(ocrLanguage)
	if ocrLanguage != "" && !documents.ValidOCRLanguage.MatchString(ocrLanguage) {
		return nil, fmt.Errorf("ocr_language must look like a tesseract language code, e.g. eng, rus, or eng+rus")
	}

	content, err := t.files.Read(sessionID, filename)
	if err != nil {
		return nil, err
	}

	var pages []documents.PageInput
	switch {
	case documents.IsPDF(content):
		pages, err = documents.ExtractPDFPages(ctx, content, t.docs.ImagesDir(), "/document-images/", ocrLanguage)
	case documents.SniffImageExt(content) != "":
		pages, err = documents.IngestStandaloneImage(ctx, content, documents.SniffImageExt(content), t.docs.ImagesDir(), "/document-images/", ocrLanguage)
	case utf8.Valid(content):
		pages = []documents.PageInput{{Text: string(content)}}
	default:
		return nil, fmt.Errorf("%q isn't a PDF, a recognized image, or valid text — can't add it to search", filename)
	}
	if err != nil {
		return nil, fmt.Errorf("extract content: %w", err)
	}

	summary, err := t.docs.AddPages(ctx, title, pages, folder)
	if err != nil {
		return nil, err
	}
	// Best-effort: the document is already added at this point, so a
	// failure to clean up the temp copy just means the TTL reaper gets to
	// it later instead of right now — not a reason to report failure for
	// what actually succeeded.
	_ = t.files.Forget(sessionID, filename)
	return map[string]any{"id": summary.ID, "title": summary.Title, "chunk_count": summary.ChunkCount}, nil
}

func (t *ChatFileTool) addToMemo(sessionID string, args map[string]any) (any, error) {
	if t.memo == nil {
		return nil, fmt.Errorf("memo file attachments are not configured")
	}
	filename, err := filenameArg(args)
	if err != nil {
		return nil, err
	}
	memoKey, _ := args["memo_key"].(string)
	memoKey = strings.TrimSpace(memoKey)
	if memoKey == "" {
		return nil, fmt.Errorf("memo_key is required — write the memo first if it doesn't exist yet")
	}

	content, err := t.files.Read(sessionID, filename)
	if err != nil {
		return nil, err
	}
	view, err := t.memo.AttachFile(memoKey, filename, content)
	if err != nil {
		return nil, err
	}
	_ = t.files.Forget(sessionID, filename)
	return view, nil
}
