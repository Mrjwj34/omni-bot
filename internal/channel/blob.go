package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

const defaultMaxBlobBytes int64 = 64 << 20

var ErrBlobNotFound = errors.New("blob not found")

type BlobMeta struct {
	BlobID    string `json:"blob_id"`
	MIMEType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
}

// BlobStore is a minimal consumer-side media storage contract so channel does not import store.
type BlobStore interface {
	Put(ctx context.Context, mimeType string, r io.Reader) (BlobMeta, error)
	Open(ctx context.Context, blobID string) (io.ReadCloser, BlobMeta, error)
}

func (h *Hub) handleUploadBlob(w http.ResponseWriter, r *http.Request) {
	if h.blobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "blob store not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBlobBytes)

	var (
		src      io.Reader
		mimeType string
	)

	ct := r.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(ct)
	if mediaType == "multipart/form-data" {
		mr, err := r.MultipartReader()
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid multipart form")
			return
		}
		part, err := nextBlobPart(mr)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeJSONError(w, http.StatusRequestEntityTooLarge, "blob exceeds size limit")
				return
			}
			writeJSONError(w, http.StatusBadRequest, "missing file part")
			return
		}
		defer part.Close()
		src = part
		partCT, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		mimeType = partCT
	} else {
		src = r.Body
		mimeType = mediaType
	}

	head := make([]byte, 512)
	n, readErr := io.ReadFull(src, head)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		var maxErr *http.MaxBytesError
		if errors.As(readErr, &maxErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "blob exceeds size limit")
			return
		}
		h.logger.Warn("read upload head failed", "err", readErr)
		writeJSONError(w, http.StatusBadRequest, "failed to read upload body")
		return
	}
	if n == 0 {
		writeJSONError(w, http.StatusBadRequest, "empty upload body")
		return
	}
	head = head[:n]

	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(head)
	}

	meta, err := h.blobs.Put(r.Context(), mimeType, io.MultiReader(bytes.NewReader(head), src))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "blob exceeds size limit")
			return
		}
		h.logger.Error("store blob failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to store blob")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(meta)
}

func (h *Hub) handleGetBlob(w http.ResponseWriter, r *http.Request) {
	if h.blobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "blob store not configured")
		return
	}

	blobID := r.PathValue("id")
	if !isSafeBlobID(blobID) {
		writeJSONError(w, http.StatusBadRequest, "invalid blob id")
		return
	}

	rc, meta, err := h.blobs.Open(r.Context(), blobID)
	if err != nil {
		if errors.Is(err, ErrBlobNotFound) {
			writeJSONError(w, http.StatusNotFound, "blob not found")
			return
		}
		h.logger.Error("open blob failed", "blob_id", blobID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to open blob")
		return
	}
	defer rc.Close()

	ct := meta.MIMEType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if meta.SizeBytes > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.SizeBytes, 10))
	}
	if _, err := io.Copy(w, rc); err != nil {
		h.logger.Debug("stream blob failed", "blob_id", blobID, "err", err)
	}
}

func nextBlobPart(mr *multipart.Reader) (*multipart.Part, error) {
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil, err
		}
		if part.FormName() == "file" || part.FileName() != "" {
			return part, nil
		}
		_ = part.Close()
	}
}

func isSafeBlobID(id string) bool {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	return true
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
