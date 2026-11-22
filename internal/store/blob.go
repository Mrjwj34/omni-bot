package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrInvalidBlobID = errors.New("store: invalid blob id")
	ErrPathTraversal = errors.New("store: path escapes storage root")
	ErrBlobNotFound  = errors.New("store: blob not found")
)

// BlobMeta stores content-addressed blob identity and MIME metadata.
type BlobMeta struct {
	BlobID      string `json:"blob_id"`
	MIMEType    string `json:"mime_type"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAtMs int64  `json:"created_at_ms"`
}

// BlobStore manages content-addressed local blobs/ and meme assets under memes/.
type BlobStore struct {
	rootDir  string
	blobsDir string
	memesDir string
}

// NewBlobStore initializes rootDir/blobs and rootDir/memes directories.
func NewBlobStore(rootDir string) (*BlobStore, error) {
	if strings.TrimSpace(rootDir) == "" {
		return nil, errors.New("store: blob root directory must not be empty")
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("store: resolve blob root %q: %w", rootDir, err)
	}
	blobsDir := filepath.Join(absRoot, "blobs")
	memesDir := filepath.Join(absRoot, "memes")
	for _, dir := range []string{blobsDir, memesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create dir %q: %w", dir, err)
		}
	}
	return &BlobStore{
		rootDir:  absRoot,
		blobsDir: blobsDir,
		memesDir: memesDir,
	}, nil
}

func (b *BlobStore) RootDir() string  { return b.rootDir }
func (b *BlobStore) BlobsDir() string { return b.blobsDir }
func (b *BlobStore) MemesDir() string { return b.memesDir }

// PutBlob streams r into an atomic temporary file, hashes its SHA-256 content ID, and stores data + MIME metadata.
func (b *BlobStore) PutBlob(ctx context.Context, r io.Reader, mimeType string) (BlobMeta, error) {
	if r == nil {
		return BlobMeta{}, errors.New("store: nil blob reader")
	}
	if err := ctx.Err(); err != nil {
		return BlobMeta{}, err
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	tmp, err := os.CreateTemp(b.blobsDir, ".tmp-blob-*")
	if err != nil {
		return BlobMeta{}, fmt.Errorf("store: create temp blob: %w", err)
	}
	tmpPath := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	size, err := copyWithContext(ctx, io.MultiWriter(tmp, h), r)
	if err != nil {
		return BlobMeta{}, fmt.Errorf("store: write temp blob: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return BlobMeta{}, fmt.Errorf("store: sync temp blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return BlobMeta{}, fmt.Errorf("store: close temp blob: %w", err)
	}

	blobID := hex.EncodeToString(h.Sum(nil))
	dstPath, err := b.blobDataPath(blobID)
	if err != nil {
		return BlobMeta{}, err
	}

	if info, statErr := os.Stat(dstPath); statErr == nil && info.Size() == size {
		// Content-addressed file already exists with identical digest; discard duplicate temp file.
		_ = os.Remove(tmpPath)
		cleanupTmp = false
		if existing, err := b.StatBlob(ctx, blobID); err == nil {
			return existing, nil
		}
	} else {
		if err := os.Rename(tmpPath, dstPath); err != nil {
			return BlobMeta{}, fmt.Errorf("store: commit blob %q: %w", blobID, err)
		}
		cleanupTmp = false
	}

	meta := BlobMeta{
		BlobID:      blobID,
		MIMEType:    mimeType,
		SizeBytes:   size,
		CreatedAtMs: time.Now().UnixMilli(),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return BlobMeta{}, fmt.Errorf("store: marshal blob meta: %w", err)
	}
	metaPath := dstPath + ".meta.json"
	if err := writeFileAtomic(b.blobsDir, metaPath, metaBytes); err != nil {
		return BlobMeta{}, fmt.Errorf("store: write blob meta %q: %w", blobID, err)
	}
	return meta, nil
}

// StatBlob returns stored metadata for blobID after validating against path traversal.
func (b *BlobStore) StatBlob(ctx context.Context, blobID string) (BlobMeta, error) {
	if err := ctx.Err(); err != nil {
		return BlobMeta{}, err
	}
	dstPath, err := b.blobDataPath(blobID)
	if err != nil {
		return BlobMeta{}, err
	}
	info, err := os.Stat(dstPath)
	if errors.Is(err, os.ErrNotExist) {
		return BlobMeta{}, ErrBlobNotFound
	}
	if err != nil {
		return BlobMeta{}, fmt.Errorf("store: stat blob %q: %w", blobID, err)
	}

	meta := BlobMeta{
		BlobID:      blobID,
		MIMEType:    "application/octet-stream",
		SizeBytes:   info.Size(),
		CreatedAtMs: info.ModTime().UnixMilli(),
	}
	if raw, err := os.ReadFile(dstPath + ".meta.json"); err == nil {
		var parsed BlobMeta
		if json.Unmarshal(raw, &parsed) == nil {
			parsed.BlobID = blobID
			parsed.SizeBytes = info.Size()
			meta = parsed
		}
	}
	return meta, nil
}

// GetBlob opens a read handle and returns metadata for blobID.
func (b *BlobStore) GetBlob(ctx context.Context, blobID string) (io.ReadCloser, BlobMeta, error) {
	meta, err := b.StatBlob(ctx, blobID)
	if err != nil {
		return nil, BlobMeta{}, err
	}
	dstPath, err := b.blobDataPath(blobID)
	if err != nil {
		return nil, BlobMeta{}, err
	}
	f, err := os.Open(dstPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, BlobMeta{}, ErrBlobNotFound
	}
	if err != nil {
		return nil, BlobMeta{}, fmt.Errorf("store: open blob %q: %w", blobID, err)
	}
	return f, meta, nil
}

// DeleteBlob removes a blob and its sidecar metadata file if present.
func (b *BlobStore) DeleteBlob(ctx context.Context, blobID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dstPath, err := b.blobDataPath(blobID)
	if err != nil {
		return err
	}
	errData := os.Remove(dstPath)
	if errors.Is(errData, os.ErrNotExist) {
		errData = nil
	}
	errMeta := os.Remove(dstPath + ".meta.json")
	if errors.Is(errMeta, os.ErrNotExist) {
		errMeta = nil
	}
	return errors.Join(errData, errMeta)
}

// PutMemeFile atomically writes a meme image or thumbnail under memes/<relName> and returns "memes/<cleanRel>".
func (b *BlobStore) PutMemeFile(ctx context.Context, relName string, r io.Reader) (string, int64, error) {
	if r == nil {
		return "", 0, errors.New("store: nil meme reader")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	absPath, cleanRel, err := resolveSafeSubpath(b.memesDir, relName)
	if err != nil {
		return "", 0, err
	}
	parentDir := filepath.Dir(absPath)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("store: create meme subdir: %w", err)
	}

	tmp, err := os.CreateTemp(parentDir, ".tmp-meme-*")
	if err != nil {
		return "", 0, fmt.Errorf("store: create temp meme file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	size, err := copyWithContext(ctx, tmp, r)
	if err != nil {
		return "", 0, fmt.Errorf("store: write temp meme file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, fmt.Errorf("store: sync temp meme file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("store: close temp meme file: %w", err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		return "", 0, fmt.Errorf("store: commit meme file %q: %w", cleanRel, err)
	}
	cleanupTmp = false
	return path.Join("memes", cleanRel), size, nil
}

// OpenMemeFile opens a stored meme asset by its relative path (e.g. "memes/<hash>.gif" or "<hash>.gif").
func (b *BlobStore) OpenMemeFile(ctx context.Context, relPath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	trimmed := strings.TrimPrefix(filepath.ToSlash(relPath), "memes/")
	absPath, _, err := resolveSafeSubpath(b.memesDir, trimmed)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(absPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrBlobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: open meme file %q: %w", relPath, err)
	}
	return f, nil
}

func (b *BlobStore) blobDataPath(blobID string) (string, error) {
	if !isValidBlobID(blobID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidBlobID, blobID)
	}
	absPath, _, err := resolveSafeSubpath(b.blobsDir, blobID)
	if err != nil {
		return "", err
	}
	return absPath, nil
}

func isValidBlobID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		isLower := c >= 'a' && c <= 'z'
		isUpper := c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		if !isLower && !isUpper && !isDigit && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func resolveSafeSubpath(baseDir, rawRel string) (string, string, error) {
	if rawRel == "" || strings.ContainsRune(rawRel, '\x00') {
		return "", "", fmt.Errorf("%w: empty or null byte", ErrPathTraversal)
	}
	if strings.ContainsRune(rawRel, '\\') {
		return "", "", fmt.Errorf("%w: backslash not allowed", ErrPathTraversal)
	}
	if strings.HasPrefix(rawRel, "/") || filepath.IsAbs(rawRel) || filepath.VolumeName(rawRel) != "" {
		return "", "", fmt.Errorf("%w: absolute path %q", ErrPathTraversal, rawRel)
	}
	if strings.ContainsRune(rawRel, ':') {
		return "", "", fmt.Errorf("%w: drive or stream colon in %q", ErrPathTraversal, rawRel)
	}

	cleanRel := path.Clean(rawRel)
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, "../") {
		return "", "", fmt.Errorf("%w: traversal %q", ErrPathTraversal, rawRel)
	}

	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", "", fmt.Errorf("store: resolve base dir: %w", err)
	}
	absTarget, err := filepath.Abs(filepath.Join(absBase, filepath.FromSlash(cleanRel)))
	if err != nil {
		return "", "", fmt.Errorf("store: resolve target path: %w", err)
	}

	relCheck, err := filepath.Rel(absBase, absTarget)
	if err != nil || relCheck == "." || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%w: %q escapes %q", ErrPathTraversal, rawRel, absBase)
	}
	return absTarget, cleanRel, nil
}

func writeFileAtomic(tmpDir, dstPath string, data []byte) error {
	tmp, err := os.CreateTemp(tmpDir, ".tmp-meta-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return err
	}
	cleanupTmp = false
	return nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			total += int64(nw)
			if werr != nil {
				return total, werr
			}
			if nw != nr {
				return total, io.ErrShortWrite
			}
		}
		if rerr == io.EOF {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
}
