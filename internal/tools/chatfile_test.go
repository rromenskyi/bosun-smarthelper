package tools

import (
	"context"
	"encoding/base64"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/chatfiles"
	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/documents"
	"github.com/roman220/bosun-smarthelper/internal/filedump"
)

// onePixelPNG is a minimal valid 1x1 transparent PNG — same fixture
// internal/documents' own ingestion tests use, kept as a separate copy
// here rather than exported across packages (test-only data).
var onePixelPNG = mustDecodeBase64ChatFile("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")

func mustDecodeBase64ChatFile(s string) []byte {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return data
}

func requireOCRTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"tesseract", "pdftoppm", "pdftotext", "pdfinfo"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed; skipping OCR-dependent test", bin)
		}
	}
}

func newChatFileTestTool(t *testing.T) (*ChatFileTool, *chatfiles.Store, *documents.Store, *MemoTool) {
	t.Helper()
	filesStore, err := chatfiles.NewStore(filepath.Join(t.TempDir(), "chatfiles"))
	if err != nil {
		t.Fatalf("chatfiles.NewStore: %v", err)
	}
	docStore := documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	memoTool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	fileDumpStore, err := filedump.NewStore(filepath.Join(t.TempDir(), "filedump"))
	if err != nil {
		t.Fatalf("filedump.NewStore: %v", err)
	}
	memoTool.SetFileDumpStore(fileDumpStore)

	tool := NewChatFileTool(filesStore, docStore, memoTool)
	return tool, filesStore, docStore, memoTool
}

func TestChatFileToolRequiresSessionID(t *testing.T) {
	tool, _, _, _ := newChatFileTestTool(t)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if err == nil {
		t.Error("expected an error when no session id is in context")
	}
}

func TestChatFileToolNotConfiguredReturnsError(t *testing.T) {
	tool := NewChatFileTool(nil, nil, nil)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := tool.Execute(ctx, map[string]any{"action": "list"}); err == nil {
		t.Error("expected an error when chatfiles isn't configured")
	}
}

func TestChatFileToolList(t *testing.T) {
	tool, filesStore, _, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")

	empty, err := tool.Execute(ctx, map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list (empty): %v", err)
	}
	if files, _ := empty.(map[string]any)["files"].([]map[string]any); len(files) != 0 {
		t.Errorf("expected no files, got %#v", files)
	}

	if _, err := filesStore.Save("session-1", "notes.txt", strings.NewReader("hello")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	result, err := tool.Execute(ctx, map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	files := result.(map[string]any)["files"].([]map[string]any)
	if len(files) != 1 || files[0]["name"] != "notes.txt" {
		t.Errorf("files = %#v, want [{name: notes.txt}]", files)
	}
}

func TestChatFileToolRead(t *testing.T) {
	tool, filesStore, _, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := filesStore.Save("session-1", "notes.txt", strings.NewReader("hello world")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "read", "filename": "notes.txt"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.(map[string]any)["content"] != "hello world" {
		t.Errorf("content = %#v, want %q", result.(map[string]any)["content"], "hello world")
	}
}

func TestChatFileToolReadRejectsBinaryFile(t *testing.T) {
	tool, filesStore, _, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := filesStore.Save("session-1", "photo.png", strings.NewReader(string(onePixelPNG))); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "read", "filename": "photo.png"}); err == nil {
		t.Error("expected an error reading a binary file as text")
	}
}

func TestChatFileToolReadRequiresFilename(t *testing.T) {
	tool, _, _, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := tool.Execute(ctx, map[string]any{"action": "read"}); err == nil {
		t.Error("expected an error with no filename given")
	}
}

func TestChatFileToolAddToRAGPlainText(t *testing.T) {
	tool, filesStore, docStore, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := filesStore.Save("session-1", "notes.txt", strings.NewReader("fuse panel wiring notes")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{
		"action": "add_to_rag", "filename": "notes.txt", "title": "Wiring notes", "folder": "manuals/generator",
	})
	if err != nil {
		t.Fatalf("add_to_rag: %v", err)
	}
	view := result.(map[string]any)
	if view["title"] != "Wiring notes" {
		t.Errorf("title = %#v, want Wiring notes", view["title"])
	}

	list, err := docStore.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].SourcePath != "manuals/generator" {
		t.Fatalf("documents = %#v, want one with source_path manuals/generator", list)
	}

	// The temp attachment should be cleaned up once it's added to RAG.
	remaining, err := filesStore.List("session-1")
	if err != nil {
		t.Fatalf("List chat files: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("chat files = %#v, want cleared after add_to_rag", remaining)
	}
}

func TestChatFileToolAddToRAGImage(t *testing.T) {
	requireOCRTools(t)
	tool, filesStore, docStore, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := filesStore.Save("session-1", "photo.png", strings.NewReader(string(onePixelPNG))); err != nil {
		t.Fatalf("Save: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{
		"action": "add_to_rag", "filename": "photo.png", "title": "Fuse panel photo",
	})
	if err != nil {
		t.Fatalf("add_to_rag: %v", err)
	}
	if result.(map[string]any)["title"] != "Fuse panel photo" {
		t.Errorf("unexpected result: %#v", result)
	}
	list, err := docStore.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("documents = %#v, want 1", list)
	}
}

func TestChatFileToolAddToRAGRequiresTitle(t *testing.T) {
	tool, filesStore, _, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := filesStore.Save("session-1", "notes.txt", strings.NewReader("content")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "add_to_rag", "filename": "notes.txt"}); err == nil {
		t.Error("expected an error when title is missing")
	}
}

func TestChatFileToolAddToRAGRejectsBadOCRLanguage(t *testing.T) {
	tool, filesStore, _, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := filesStore.Save("session-1", "notes.txt", strings.NewReader("content")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, err := tool.Execute(ctx, map[string]any{
		"action": "add_to_rag", "filename": "notes.txt", "title": "t", "ocr_language": "; rm -rf /",
	})
	if err == nil {
		t.Error("expected an error for a malformed ocr_language")
	}
}

func TestChatFileToolAddToMemo(t *testing.T) {
	tool, filesStore, _, memoTool := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")

	if _, err := memoTool.Execute(ctx, map[string]any{"action": "write", "key": "fuse-panel", "content": "Fuse panel note"}); err != nil {
		t.Fatalf("write memo: %v", err)
	}
	if _, err := filesStore.Save("session-1", "photo.jpg", strings.NewReader("fake-jpeg-bytes")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{
		"action": "add_to_memo", "filename": "photo.jpg", "memo_key": "fuse-panel",
	})
	if err != nil {
		t.Fatalf("add_to_memo: %v", err)
	}
	attachments, _ := result.(map[string]any)["attachments"].([]string)
	if len(attachments) != 1 || attachments[0] != "/files/memos/fuse-panel/photo.jpg" {
		t.Fatalf("attachments = %#v", result.(map[string]any)["attachments"])
	}

	remaining, err := filesStore.List("session-1")
	if err != nil {
		t.Fatalf("List chat files: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("chat files = %#v, want cleared after add_to_memo", remaining)
	}
}

func TestChatFileToolAddToMemoRequiresExistingMemo(t *testing.T) {
	tool, filesStore, _, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := filesStore.Save("session-1", "photo.jpg", strings.NewReader("x")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, err := tool.Execute(ctx, map[string]any{
		"action": "add_to_memo", "filename": "photo.jpg", "memo_key": "never-written",
	})
	if err == nil {
		t.Error("expected an error attaching to a memo that doesn't exist")
	}
}

func TestChatFileToolUnsupportedAction(t *testing.T) {
	tool, _, _, _ := newChatFileTestTool(t)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := tool.Execute(ctx, map[string]any{"action": "explode"}); err == nil {
		t.Error("expected an error for an unsupported action")
	}
}
