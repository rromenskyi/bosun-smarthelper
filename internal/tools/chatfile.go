package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/roman220/bosun-smarthelper/internal/chatfiles"
	"github.com/roman220/bosun-smarthelper/internal/documents"
	"github.com/roman220/bosun-smarthelper/internal/llm"
)

// defaultDescribePrompt is used when the model calls "describe" without
// its own prompt — a plain "what's in this photo" ask, not tailored to
// any particular use case (fuse panel, engine part, whatever it turns
// out to be), since the model can always pass a more specific prompt
// once it knows what it's looking at.
const defaultDescribePrompt = "Describe what is shown in this image in detail: objects, people, animals, any readable text, numbers, or tags. Be factual and specific."

// maxReadableChatFileBytes bounds how much of an attached text file's
// content the "read" action returns straight into the tool result — this
// is a personal-appliance chat compose bar, not a bulk document loader
// (internal/filedump/RAG already exists for that), so a request to read
// something large is almost certainly a mistake, not a real use case
// worth spending context on.
const maxReadableChatFileBytes = 200_000

// maxDownloadBytes matches internal/webui/chatfiles.go's own upload cap
// (maxChatFileUploadBytes) — a URL download lands in the exact same
// store as a browser upload, so it gets the same size ceiling.
const maxDownloadBytes = 25 << 20

// downloadTimeout bounds the whole GET, not just connect — a slow or
// stalled server shouldn't hang a tool call indefinitely. 30s (the
// original value) turned out to be too tight for real use: a large,
// legitimate PDF (a state wildlife agency's full regulations booklet,
// confirmed live) from an ordinary, not-particularly-fast server needs
// more than that just to finish transferring, and the whole request
// aborts (mid-body, per net/http's Client.Timeout covering the entire
// round trip) rather than the model ever seeing a clean "too slow, give
// up" — it just looked like the download silently broke. The overall
// chat turn has a 600s budget (web.request_timeout) to spend this in.
const downloadTimeout = 3 * time.Minute

// ChatFileTool lets the model act on a file the user just attached
// directly to a chat message (see internal/chatfiles) — add it to the
// document search index, or link it to a memo — without any
// system-prompt changes: discovered only through this tool's own
// name/description and the user's own message mentioning the
// attachment, the same way any other tool is discovered.
type ChatFileTool struct {
	files        *chatfiles.Store
	docs         *documents.Store
	memo         *MemoTool
	remoteVision *llm.RemoteClient
	localVision  *llm.LocalClient
}

// NewChatFileTool wires everything the tool's actions need. docs/memo/
// vision clients may be nil (matching how other optional features
// degrade elsewhere in this codebase) — the corresponding action just
// returns a clear error instead of the tool failing to register at all.
// add_to_memo's actual filedump write happens inside memo.AttachFile,
// which holds its own *filedump.Store — this tool never needs one
// directly.
//
// describe tries remoteVision first (see internal/llm/vision.go — it
// already retries a few times against the flaky upstream this
// deployment sits behind) and only falls back to localVision if that's
// exhausted: remote is fast when it lands on a working backend, local
// is slow (a real photo measured ~170s on this deployment's CPU-only
// hardware) but doesn't depend on someone else's infrastructure at all.
func NewChatFileTool(files *chatfiles.Store, docs *documents.Store, memo *MemoTool, remoteVision *llm.RemoteClient, localVision *llm.LocalClient) *ChatFileTool {
	return &ChatFileTool{files: files, docs: docs, memo: memo, remoteVision: remoteVision, localVision: localVision}
}

func (t *ChatFileTool) Name() string { return "chat_file" }

func (t *ChatFileTool) Description() string {
	return "Work with a file the user just attached to this chat message (mentioned in their message as an attachment, not a document upload), " +
		"or one fetched by URL with \"download_url\" — either way it lands in the same place and the other actions below treat it identically. " +
		"Call \"list\" first to see what's attached and get the exact filename. " +
		"\"download_url\" fetches a file from a public http(s) URL (e.g. one found with web_search) and attaches it, so you can then add_to_rag it — only public internet addresses are allowed, not internal/local ones. " +
		"\"describe\" answers what's actually in a photo/image — use this whenever the user asks what something in the picture is, before reaching for add_to_rag. " +
		"It can occasionally fail or time out (the vision backend behind it is flaky) — if so, just say the description failed and offer to try again, don't guess at the image's contents yourself. " +
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
				"enum": []string{"list", "download_url", "describe", "read", "add_to_rag", "add_to_memo"},
				"description": "list: show attached files. download_url: fetch a file by URL and attach it. describe: answer what's in a photo. read: return a small text file's content. " +
					"add_to_rag: ingest into document search. add_to_memo: link to an existing memo.",
			},
			"filename": map[string]any{
				"type":        "string",
				"description": "Exact name of the attached file, from \"list\" — required for describe/read/add_to_rag/add_to_memo. For download_url, an optional override for the saved name (defaults to the URL's own filename).",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "http(s) URL to fetch — required for download_url. Must resolve to a public internet address.",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "Optional for describe — a specific question about the image (e.g. \"what breed is this cow?\"). Defaults to a general description.",
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
	case "download_url":
		return t.downloadURL(ctx, sessionID, args)
	case "describe":
		return t.describe(ctx, sessionID, args)
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

// downloadURL fetches a file server-side and lands it in the same
// per-session store a browser upload/paste would — the bridge that lets
// "find the manual for X, add it to the manuals folder" work in one
// turn: the model downloads it here, then calls add_to_rag on the exact
// filename this returns, exactly as if the user had pasted it in.
// Without this, a URL a tool (e.g. web_search) turned up had nowhere to
// go — run_code has network access, but a file it downloads is trapped
// in that sandbox's own workspace with no path out to chat_file/RAG.
//
// Every resolved IP is checked against isPrivateOrLocal — including on
// each redirect hop — before it's ever dialed: this server fetches
// whatever URL the model was told about (a web_search result, or
// something echoed back from a page's own content), so without this a
// prompt-injected or just-wrong URL could reach an internal service
// (this LAN's other devices, this host's own other ports) that was
// never meant to be reachable from a chat request. Restricting to
// public addresses only closes that off; genuine "download a document"
// use never needed anything else.
func (t *ChatFileTool) downloadURL(ctx context.Context, sessionID string, args map[string]any) (any, error) {
	rawURL, _ := args["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http/https URLs are supported")
	}

	client := &http.Client{
		Timeout: downloadTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return checkPublicURLFunc(req.URL)
		},
	}
	if err := checkPublicURLFunc(parsed); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Bosun/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: server returned %s", resp.Status)
	}

	limited := io.LimitReader(resp.Body, maxDownloadBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxDownloadBytes {
		return nil, fmt.Errorf("file is too large (over %d MB)", maxDownloadBytes/(1<<20))
	}

	filename, _ := args["filename"].(string)
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = filenameFromURL(parsed)
	}

	saved, err := t.files.Save(sessionID, filename, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return map[string]any{"filename": saved, "size_bytes": len(data)}, nil
}

// checkPublicURLFunc is a var (not a direct call to checkPublicURL) so
// tests can swap in a permissive stand-in — an httptest.Server is
// necessarily loopback, exactly what this check exists to reject, so
// exercising the rest of downloadURL's logic against a real HTTP
// response needs a way around it. checkPublicURL itself is still tested
// directly, unmocked.
var checkPublicURLFunc = checkPublicURL

// checkPublicURL rejects a URL whose host resolves to any
// loopback/private/link-local/unspecified address — see downloadURL's
// comment for why. Checked before every fetch, including redirects.
func checkPublicURL(u *url.URL) error {
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url has no host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve host %q: %w", host, err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("refusing to fetch %q: resolves to a private/internal address", host)
		}
	}
	return nil
}

// filenameFromURL derives a reasonable attachment name from a URL's own
// path when the caller didn't give one — e.g.
// "https://example.com/docs/manual.pdf" -> "manual.pdf". Falls back to a
// generic name for a URL with no useful path segment (e.g. a bare domain
// or one ending in "/"); chatfiles.Store.Save sanitizes/deduplicates the
// result further regardless.
func filenameFromURL(u *url.URL) string {
	base := path.Base(u.Path)
	if base == "" || base == "." || base == "/" {
		return "download"
	}
	return base
}

// describe answers what's actually shown in an attached photo — the tool
// action to reach for on "what's in this picture", instead of forcing the
// user through add_to_rag (an OCR pipeline that finds printed text, not a
// description of the scene) just to get an answer to that question.
func (t *ChatFileTool) describe(ctx context.Context, sessionID string, args map[string]any) (any, error) {
	if t.remoteVision == nil && t.localVision == nil {
		return nil, fmt.Errorf("image description is not configured")
	}
	filename, err := filenameArg(args)
	if err != nil {
		return nil, err
	}
	content, err := t.files.Read(sessionID, filename)
	if err != nil {
		return nil, err
	}
	mimeType := http.DetectContentType(content)
	if !strings.HasPrefix(mimeType, "image/") {
		return nil, fmt.Errorf("%q isn't an image — describe only works on photos/pictures", filename)
	}
	prompt, _ := args["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = defaultDescribePrompt
	}

	var description string
	var visionErr error
	if t.remoteVision != nil {
		description, visionErr = t.remoteVision.DescribeImage(ctx, prompt, content, mimeType)
	} else {
		visionErr = fmt.Errorf("remote vision not configured")
	}
	if visionErr != nil && t.localVision != nil {
		// Slow (a real photo can take a couple of minutes on modest
		// hardware) but doesn't depend on someone else's flaky backend —
		// worth the wait only after remote's own retries are exhausted.
		description, visionErr = t.localVision.DescribeImage(ctx, prompt, content, mimeType)
	}
	if visionErr != nil {
		return nil, fmt.Errorf("couldn't describe the image right now — worth trying again: %w", visionErr)
	}
	return map[string]any{"filename": filename, "description": description}, nil
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
