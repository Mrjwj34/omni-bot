package store

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct {
	payload []byte
	read    bool
}

func (f *failingReader) Read(p []byte) (int, error) {
	if !f.read {
		f.read = true
		n := copy(p, f.payload)
		return n, nil
	}
	return 0, errors.New("simulated read failure")
}

func TestBlobStorePutGetDedupAndDelete(t *testing.T) {
	ctx := t.Context()
	bs, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewBlobStore: %v", err)
	}

	payload := []byte("hello omni-bot media blob")
	meta, err := bs.PutBlob(ctx, bytes.NewReader(payload), "image/webp")
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if meta.BlobID == "" || meta.MIMEType != "image/webp" || meta.SizeBytes != int64(len(payload)) {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	// Second put of identical bytes deduplicates to the same content-addressed ID.
	meta2, err := bs.PutBlob(ctx, bytes.NewReader(payload), "image/webp")
	if err != nil {
		t.Fatalf("PutBlob dedup: %v", err)
	}
	if meta2.BlobID != meta.BlobID {
		t.Fatalf("dedup blob id mismatch: %q vs %q", meta2.BlobID, meta.BlobID)
	}

	rc, gotMeta, err := bs.GetBlob(ctx, meta.BlobID)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	gotBytes, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if !bytes.Equal(gotBytes, payload) || gotMeta.MIMEType != "image/webp" {
		t.Fatalf("GetBlob mismatch: bytes=%q meta=%+v", gotBytes, gotMeta)
	}

	if err := bs.DeleteBlob(ctx, meta.BlobID); err != nil {
		t.Fatalf("DeleteBlob: %v", err)
	}
	if _, _, err := bs.GetBlob(ctx, meta.BlobID); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("expected ErrBlobNotFound after delete, got %v", err)
	}
}

func TestBlobStoreAtomicWriteCleanupOnFailure(t *testing.T) {
	ctx := t.Context()
	bs, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewBlobStore: %v", err)
	}

	_, err = bs.PutBlob(ctx, &failingReader{payload: []byte("partial")}, "application/octet-stream")
	if err == nil {
		t.Fatalf("expected error from failing reader")
	}

	entries, err := os.ReadDir(bs.BlobsDir())
	if err != nil {
		t.Fatalf("ReadDir blobs: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected zero leftover temp files in blobs/, found %d", len(entries))
	}
}

func TestBlobStoreMemeFilesAndTraversalProtection(t *testing.T) {
	ctx := t.Context()
	bs, err := NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewBlobStore: %v", err)
	}

	rel, n, err := bs.PutMemeFile(ctx, "thumbs/abc123.webp", bytes.NewReader([]byte("RIFFWEBP")))
	if err != nil {
		t.Fatalf("PutMemeFile: %v", err)
	}
	if rel != "memes/thumbs/abc123.webp" || n != 8 {
		t.Fatalf("PutMemeFile = (%q, %d), want (memes/thumbs/abc123.webp, 8)", rel, n)
	}

	rc, err := bs.OpenMemeFile(ctx, rel)
	if err != nil {
		t.Fatalf("OpenMemeFile: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(data) != "RIFFWEBP" {
		t.Fatalf("OpenMemeFile content = %q err=%v", data, err)
	}

	// Verify file physically resides under memes/.
	if _, err := os.Stat(filepath.Join(bs.MemesDir(), "thumbs", "abc123.webp")); err != nil {
		t.Fatalf("expected physical meme thumb file: %v", err)
	}

	badBlobIDs := []string{
		"",
		".",
		"..",
		"../secret",
		"..\\secret",
		"a/b",
		"abc.txt",
		"bad\x00id",
		strings.Repeat("a", 129),
	}
	for _, id := range badBlobIDs {
		t.Run("bad_blob_"+id, func(t *testing.T) {
			if _, _, err := bs.GetBlob(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
				t.Fatalf("GetBlob(%q) err = %v, want ErrInvalidBlobID", id, err)
			}
		})
	}

	badMemePaths := []string{
		"",
		".",
		"..",
		"../blobs/escape",
		"thumbs/../../escape.png",
		"..\\escape.png",
		"/etc/passwd",
		`C:\Windows\win.ini`,
		"C:drive.png",
		"bad\x00.png",
	}
	for _, p := range badMemePaths {
		t.Run("bad_meme_"+p, func(t *testing.T) {
			if _, _, err := bs.PutMemeFile(ctx, p, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrPathTraversal) {
				t.Fatalf("PutMemeFile(%q) err = %v, want ErrPathTraversal", p, err)
			}
			if _, err := bs.OpenMemeFile(ctx, p); !errors.Is(err, ErrPathTraversal) {
				t.Fatalf("OpenMemeFile(%q) err = %v, want ErrPathTraversal", p, err)
			}
		})
	}
}
