package google

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/interactions"

	"omni-bot/internal/ai"
)

type cachedFile struct {
	uri       string
	mimeType  string
	expiresAt time.Time
}

type uploadCall struct {
	done chan struct{}
	file cachedFile
	err  error
}

// fileManager encapsulates Google File API uploads, video ACTIVE polling, and 44h TTL
// caching so no files/... URI detail ever escapes internal/ai/google.
type fileManager struct {
	client         *genai.Client
	provider       string
	blobs          ai.BlobReader
	inlineMaxBytes int64
	ttl            time.Duration
	pollInterval   time.Duration
	pollTimeout    time.Duration
	now            func() time.Time

	mu       sync.Mutex
	cache    map[string]cachedFile
	inflight map[string]*uploadCall
}

func newFileManager(client *genai.Client, provider string, opts Options) *fileManager {
	return &fileManager{
		client:         client,
		provider:       provider,
		blobs:          opts.BlobReader,
		inlineMaxBytes: opts.InlineMaxBytes,
		ttl:            opts.FileCacheTTL,
		pollInterval:   opts.FilePollInterval,
		pollTimeout:    opts.FilePollTimeout,
		now:            opts.Now,
		cache:          make(map[string]cachedFile),
		inflight:       make(map[string]*uploadCall),
	}
}

func (m *fileManager) toInteractionContent(ctx context.Context, kind ai.PartKind, ref *ai.MediaRef) (interactions.Content, error) {
	if ref == nil {
		return interactions.Content{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: m.provider,
			Message:  fmt.Sprintf("media part %q missing MediaRef", kind),
		}
	}

	data, mimeType, err := m.loadMediaBytes(ctx, kind, ref)
	if err != nil {
		return interactions.Content{}, err
	}

	useFileAPI := kind == ai.PartVideo || int64(len(data)) > m.inlineMaxBytes

	var (
		inlineB64 string
		fileURI   string
	)
	if useFileAPI {
		cf, err := m.ensureUploaded(ctx, ref, data, mimeType)
		if err != nil {
			return interactions.Content{}, err
		}
		fileURI = cf.uri
		if cf.mimeType != "" {
			mimeType = cf.mimeType
		}
	} else {
		inlineB64 = base64.StdEncoding.EncodeToString(data)
	}

	switch kind {
	case ai.PartImage:
		mt := interactions.ImageContentMimeType(mimeType)
		img := interactions.ImageContent{
			MimeType:   &mt,
			Resolution: resolveMediaResolution(ref, kind, len(data)),
		}
		if fileURI != "" {
			img.URI = &fileURI
		} else {
			img.Data = &inlineB64
		}
		return interactions.NewContent(img), nil

	case ai.PartAudio:
		mt := interactions.AudioContentMimeType(mimeType)
		aud := interactions.AudioContent{
			MimeType: &mt,
		}
		if fileURI != "" {
			aud.URI = &fileURI
		} else {
			aud.Data = &inlineB64
		}
		return interactions.NewContent(aud), nil

	case ai.PartVideo:
		mt := interactions.VideoContentMimeType(mimeType)
		vid := interactions.VideoContent{
			MimeType:   &mt,
			Resolution: resolveMediaResolution(ref, kind, len(data)),
		}
		if fileURI != "" {
			vid.URI = &fileURI
		} else {
			vid.Data = &inlineB64
		}
		return interactions.NewContent(vid), nil

	default:
		return interactions.Content{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: m.provider,
			Message:  fmt.Sprintf("unsupported media part kind %q", kind),
		}
	}
}

func (m *fileManager) loadMediaBytes(ctx context.Context, kind ai.PartKind, ref *ai.MediaRef) ([]byte, string, error) {
	var data []byte
	var err error

	switch {
	case len(ref.Data) > 0:
		data = ref.Data
	case strings.TrimSpace(ref.Path) != "":
		data, err = os.ReadFile(ref.Path)
		if err != nil {
			return nil, "", &ai.Error{
				Kind:     ai.ErrInvalidArg,
				Provider: m.provider,
				Message:  fmt.Sprintf("read media path %q: %v", ref.Path, err),
				Err:      err,
			}
		}
	case strings.TrimSpace(ref.BlobID) != "":
		if m.blobs == nil {
			return nil, "", &ai.Error{
				Kind:     ai.ErrInvalidArg,
				Provider: m.provider,
				Message:  fmt.Sprintf("blob %q requires BlobReader", ref.BlobID),
			}
		}
		data, err = m.blobs.ReadBlob(ctx, ref.BlobID)
		if err != nil {
			return nil, "", &ai.Error{
				Kind:     ai.ErrInvalidArg,
				Provider: m.provider,
				Message:  fmt.Sprintf("read blob %q: %v", ref.BlobID, err),
				Err:      err,
			}
		}
	default:
		return nil, "", &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: m.provider,
			Message:  "media reference has no Data, Path, or BlobID",
		}
	}

	mimeType := strings.TrimSpace(ref.MIMEType)
	if mimeType == "" {
		mimeType = inferMIMEType(kind, data)
	}
	return data, mimeType, nil
}

func (m *fileManager) ensureUploaded(ctx context.Context, ref *ai.MediaRef, data []byte, mimeType string) (cachedFile, error) {
	key := mediaCacheKey(ref, data)
	now := m.now()

	m.mu.Lock()
	if hit, ok := m.cache[key]; ok && hit.expiresAt.After(now) {
		m.mu.Unlock()
		return hit, nil
	}
	if call, ok := m.inflight[key]; ok {
		m.mu.Unlock()
		select {
		case <-call.done:
			return call.file, call.err
		case <-ctx.Done():
			return cachedFile{}, classifyError(m.provider, ctx.Err())
		}
	}
	call := &uploadCall{done: make(chan struct{})}
	m.inflight[key] = call
	m.mu.Unlock()

	cf, err := m.uploadAndWaitActive(ctx, data, mimeType)

	m.mu.Lock()
	delete(m.inflight, key)
	if err == nil {
		m.cache[key] = cf
	}
	call.file = cf
	call.err = err
	close(call.done)
	m.mu.Unlock()

	return cf, err
}

func (m *fileManager) uploadAndWaitActive(ctx context.Context, data []byte, mimeType string) (cachedFile, error) {
	uploaded, err := m.client.Files.Upload(ctx, bytes.NewReader(data), &genai.UploadFileConfig{
		MIMEType: mimeType,
	})
	if err != nil {
		return cachedFile{}, classifyError(m.provider, err)
	}
	if uploaded == nil || uploaded.URI == "" {
		return cachedFile{}, &ai.Error{
			Kind:     ai.ErrTransient,
			Provider: m.provider,
			Message:  "file upload returned empty URI",
		}
	}

	cur := uploaded
	if cur.State == genai.FileStateProcessing {
		pollCtx, cancel := context.WithTimeout(ctx, m.pollTimeout)
		defer cancel()

		for cur.State == genai.FileStateProcessing {
			timer := time.NewTimer(m.pollInterval)
			select {
			case <-pollCtx.Done():
				timer.Stop()
				return cachedFile{}, classifyError(m.provider, pollCtx.Err())
			case <-timer.C:
			}

			cur, err = m.client.Files.Get(pollCtx, cur.Name, nil)
			if err != nil {
				return cachedFile{}, classifyError(m.provider, err)
			}
			if cur == nil {
				return cachedFile{}, &ai.Error{
					Kind:     ai.ErrTransient,
					Provider: m.provider,
					Message:  "file poll returned nil file",
				}
			}
		}
	}

	if cur.State == genai.FileStateFailed {
		msg := "file processing failed"
		if cur.Error != nil && cur.Error.Message != "" {
			msg = cur.Error.Message
		}
		return cachedFile{}, &ai.Error{
			Kind:     ai.ErrInvalidArg,
			Provider: m.provider,
			Message:  msg,
		}
	}

	effectiveMIME := cur.MIMEType
	if effectiveMIME == "" {
		effectiveMIME = mimeType
	}
	return cachedFile{
		uri:       cur.URI,
		mimeType:  effectiveMIME,
		expiresAt: m.now().Add(m.ttl),
	}, nil
}

func mediaCacheKey(ref *ai.MediaRef, data []byte) string {
	if ref != nil && strings.TrimSpace(ref.BlobID) != "" {
		return "blob:" + strings.TrimSpace(ref.BlobID)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func resolveMediaResolution(ref *ai.MediaRef, kind ai.PartKind, sizeBytes int) *interactions.MediaResolution {
	if ref != nil {
		switch ref.Resolution {
		case ai.ResolutionLow:
			return interactions.MediaResolutionLow.ToPointer()
		case ai.ResolutionMedium:
			return interactions.MediaResolutionMedium.ToPointer()
		case ai.ResolutionHigh:
			return interactions.MediaResolutionHigh.ToPointer()
		}
		if ref.Meta != nil {
			switch strings.ToLower(ref.Meta["detail"]) {
			case "low":
				return interactions.MediaResolutionLow.ToPointer()
			case "medium":
				return interactions.MediaResolutionMedium.ToPointer()
			case "high":
				return interactions.MediaResolutionHigh.ToPointer()
			}
			if strings.EqualFold(ref.Meta["kind"], "sticker") || strings.EqualFold(ref.Meta["kind"], "meme") {
				return interactions.MediaResolutionLow.ToPointer()
			}
		}
	}
	if kind == ai.PartVideo {
		return interactions.MediaResolutionLow.ToPointer()
	}
	if sizeBytes > 0 && sizeBytes <= 64<<10 {
		return interactions.MediaResolutionLow.ToPointer()
	}
	return interactions.MediaResolutionMedium.ToPointer()
}

func inferMIMEType(kind ai.PartKind, data []byte) string {
	if len(data) > 0 {
		detected := http.DetectContentType(data)
		if detected != "" && detected != "application/octet-stream" {
			return detected
		}
	}
	switch kind {
	case ai.PartImage:
		return "image/jpeg"
	case ai.PartAudio:
		return "audio/wav"
	case ai.PartVideo:
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}
