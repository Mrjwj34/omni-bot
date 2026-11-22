package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// FallbackConfig configures MediaFallbackMiddleware dependencies and video grid layout.
type FallbackConfig struct {
	Transcriber     TranscriptionModel
	BlobReader      BlobReader
	FFmpegBin       string
	FFprobeBin      string
	VideoGridCols   int
	VideoGridRows   int
	VideoFrameWidth int
}

type cachedAudio struct {
	text       string
	durationMs int64
}

type cachedVideoGrid struct {
	jpeg       []byte
	durationMs int64
}

type cachedVideoAudio struct {
	hasAudio   bool
	text       string
	durationMs int64
}

type cacheFlight[T any] struct {
	done chan struct{}
	val  T
	err  error
}

type flightCache[T any] struct {
	mu       sync.Mutex
	items    map[string]T
	inflight map[string]*cacheFlight[T]
}

func newFlightCache[T any]() *flightCache[T] {
	return &flightCache[T]{
		items:    make(map[string]T),
		inflight: make(map[string]*cacheFlight[T]),
	}
}

// getOrCompute runs fn without holding any mutex and deduplicates concurrent callers
// requesting the same key.
func (c *flightCache[T]) getOrCompute(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	c.mu.Lock()
	if v, ok := c.items[key]; ok {
		c.mu.Unlock()
		return v, nil
	}
	if f, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.val, f.err
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
	f := &cacheFlight[T]{done: make(chan struct{})}
	c.inflight[key] = f
	c.mu.Unlock()

	val, err := fn(ctx)

	c.mu.Lock()
	delete(c.inflight, key)
	if err == nil {
		c.items[key] = val
	}
	f.val = val
	f.err = err
	close(f.done)
	c.mu.Unlock()

	return val, err
}

// MediaFallbackMiddleware wraps a LanguageModel and transparently degrades image,
// audio, and video inputs unsupported by the inner model's Capabilities.
type MediaFallbackMiddleware struct {
	inner       LanguageModel
	transcriber TranscriptionModel
	blobs       BlobReader
	ffmpegBin   string
	ffprobeBin  string
	gridCols    int
	gridRows    int
	frameWidth  int

	audioCache      *flightCache[cachedAudio]
	videoGridCache  *flightCache[cachedVideoGrid]
	videoAudioCache *flightCache[cachedVideoAudio]
}

func NewMediaFallbackMiddleware(inner LanguageModel, cfg FallbackConfig) *MediaFallbackMiddleware {
	ffmpegBin := strings.TrimSpace(cfg.FFmpegBin)
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	ffprobeBin := strings.TrimSpace(cfg.FFprobeBin)
	if ffprobeBin == "" {
		ffprobeBin = "ffprobe"
	}
	cols := cfg.VideoGridCols
	if cols <= 0 {
		cols = 2
	}
	rows := cfg.VideoGridRows
	if rows <= 0 {
		rows = 2
	}
	frameWidth := cfg.VideoFrameWidth
	if frameWidth <= 0 {
		frameWidth = 320
	}
	return &MediaFallbackMiddleware{
		inner:           inner,
		transcriber:     cfg.Transcriber,
		blobs:           cfg.BlobReader,
		ffmpegBin:       ffmpegBin,
		ffprobeBin:      ffprobeBin,
		gridCols:        cols,
		gridRows:        rows,
		frameWidth:      frameWidth,
		audioCache:      newFlightCache[cachedAudio](),
		videoGridCache:  newFlightCache[cachedVideoGrid](),
		videoAudioCache: newFlightCache[cachedVideoAudio](),
	}
}

func (m *MediaFallbackMiddleware) Capabilities() Capabilities {
	return m.inner.Capabilities()
}

func (m *MediaFallbackMiddleware) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	caps := m.inner.Capabilities()
	if caps.Vision && caps.AudioIn && caps.VideoIn {
		return m.inner.Generate(ctx, req)
	}

	adapted := req
	adapted.Messages = make([]Message, len(req.Messages))
	for i, msg := range req.Messages {
		newMsg := msg
		if len(msg.Parts) > 0 && len(msg.NativeContent) == 0 {
			parts := make([]Part, 0, len(msg.Parts))
			for _, p := range msg.Parts {
				replaced, err := m.adaptPart(ctx, p, caps)
				if err != nil {
					return GenerateResponse{}, err
				}
				parts = append(parts, replaced...)
			}
			newMsg.Parts = parts
		}
		adapted.Messages[i] = newMsg
	}

	return m.inner.Generate(ctx, adapted)
}

func (m *MediaFallbackMiddleware) adaptPart(ctx context.Context, p Part, caps Capabilities) ([]Part, error) {
	switch p.Kind {
	case PartImage:
		if caps.Vision {
			return []Part{p}, nil
		}
		return []Part{TextPart(formatImageFallback(p.Media))}, nil

	case PartAudio:
		if caps.AudioIn {
			return []Part{p}, nil
		}
		return m.fallbackAudio(ctx, p.Media)

	case PartVideo:
		if caps.VideoIn {
			return []Part{p}, nil
		}
		return m.fallbackVideo(ctx, p.Media, caps)

	default:
		return []Part{p}, nil
	}
}

func formatImageFallback(ref *MediaRef) string {
	if caption := extractCaption(ref); caption != "" {
		return fmt.Sprintf("[图片: %s]", caption)
	}
	return "[图片: 未提供视觉描述]"
}

func extractCaption(ref *MediaRef) string {
	if ref == nil {
		return ""
	}
	if s := strings.TrimSpace(ref.Caption); s != "" {
		return s
	}
	for _, key := range []string{"caption", "description", "title", "alt"} {
		if s := strings.TrimSpace(ref.Meta[key]); s != "" {
			return s
		}
	}
	return ""
}

func (m *MediaFallbackMiddleware) fallbackAudio(ctx context.Context, ref *MediaRef) ([]Part, error) {
	if ref == nil {
		return []Part{TextPart("[语音: 缺少音频内容]")}, nil
	}
	if m.transcriber == nil {
		return []Part{TextPart(formatMetaAudio(ref))}, nil
	}

	key, loadedBytes, err := m.cacheKeyForMedia(ctx, ref)
	if err != nil {
		return nil, err
	}

	res, err := m.audioCache.getOrCompute(ctx, key, func(callCtx context.Context) (cachedAudio, error) {
		trReq := TranscribeRequest{
			Audio:    loadedBytes,
			Path:     ref.Path,
			MIMEType: ref.MIMEType,
			BlobID:   ref.BlobID,
		}
		out, err := m.transcriber.Transcribe(callCtx, trReq)
		if err != nil {
			return cachedAudio{}, err
		}
		dur := out.DurationMs
		if dur <= 0 {
			dur = ref.DurationMs
		}
		return cachedAudio{text: strings.TrimSpace(out.Text), durationMs: dur}, nil
	})
	if err != nil {
		return nil, err
	}

	durLabel := formatDurationSeconds(res.durationMs)
	if res.text == "" {
		if durLabel != "" {
			return []Part{TextPart(fmt.Sprintf("[语音转写 (%s): 无有效语音内容]", durLabel))}, nil
		}
		return []Part{TextPart("[语音转写: 无有效语音内容]")}, nil
	}
	if durLabel != "" {
		return []Part{TextPart(fmt.Sprintf("[语音转写 (%s): %s]", durLabel, res.text))}, nil
	}
	return []Part{TextPart(fmt.Sprintf("[语音转写: %s]", res.text))}, nil
}

func formatMetaAudio(ref *MediaRef) string {
	dur := formatDurationSeconds(ref.DurationMs)
	caption := extractCaption(ref)
	switch {
	case dur != "" && caption != "":
		return fmt.Sprintf("[语音 (%s): %s]", dur, caption)
	case dur != "":
		return fmt.Sprintf("[语音 (%s)]", dur)
	case caption != "":
		return fmt.Sprintf("[语音: %s]", caption)
	default:
		return "[语音]"
	}
}

func (m *MediaFallbackMiddleware) fallbackVideo(ctx context.Context, ref *MediaRef, caps Capabilities) ([]Part, error) {
	if ref == nil {
		return []Part{TextPart("[视频: 缺少视频内容]")}, nil
	}
	if !caps.Vision && m.transcriber == nil {
		return []Part{TextPart(formatMetaVideo(ref, ref.DurationMs))}, nil
	}

	key, loadedBytes, err := m.cacheKeyForMedia(ctx, ref)
	if err != nil {
		return nil, err
	}

	var (
		outParts   []Part
		durationMs = ref.DurationMs
	)

	if caps.Vision {
		grid, err := m.videoGridCache.getOrCompute(ctx, key, func(callCtx context.Context) (cachedVideoGrid, error) {
			return m.extractVideoGrid(callCtx, ref, loadedBytes)
		})
		if err != nil {
			return nil, err
		}
		if grid.durationMs > 0 {
			durationMs = grid.durationMs
		}
		if len(grid.jpeg) > 0 {
			outParts = append(outParts, MediaPart(PartImage, MediaRef{
				MIMEType:   "image/jpeg",
				Data:       grid.jpeg,
				SizeBytes:  int64(len(grid.jpeg)),
				Resolution: ResolutionLow,
				Caption:    "视频关键帧拼图",
				Meta:       map[string]string{"source_blob_id": ref.BlobID, "derived": "video_frame_grid"},
			}))
		}
	}

	if m.transcriber != nil {
		aud, err := m.videoAudioCache.getOrCompute(ctx, key, func(callCtx context.Context) (cachedVideoAudio, error) {
			return m.extractAndTranscribeVideoAudio(callCtx, ref, loadedBytes)
		})
		if err != nil {
			return nil, err
		}
		if aud.durationMs > 0 {
			durationMs = aud.durationMs
		}
		if aud.hasAudio && aud.text != "" {
			durLabel := formatDurationSeconds(durationMs)
			if durLabel != "" {
				outParts = append(outParts, TextPart(fmt.Sprintf("[视频语音转写 (%s): %s]", durLabel, aud.text)))
			} else {
				outParts = append(outParts, TextPart(fmt.Sprintf("[视频语音转写: %s]", aud.text)))
			}
		}
	}

	if len(outParts) == 0 {
		outParts = append(outParts, TextPart(formatMetaVideo(ref, durationMs)))
	}
	return outParts, nil
}

func formatMetaVideo(ref *MediaRef, durationMs int64) string {
	dur := formatDurationSeconds(durationMs)
	caption := extractCaption(ref)
	switch {
	case dur != "" && caption != "":
		return fmt.Sprintf("[视频 (%s): %s]", dur, caption)
	case dur != "":
		return fmt.Sprintf("[视频 (%s)]", dur)
	case caption != "":
		return fmt.Sprintf("[视频: %s]", caption)
	default:
		return "[视频]"
	}
}

func (m *MediaFallbackMiddleware) extractVideoGrid(ctx context.Context, ref *MediaRef, loadedBytes []byte) (cachedVideoGrid, error) {
	tmpDir, err := os.MkdirTemp("", "omni-vidgrid-*")
	if err != nil {
		return cachedVideoGrid{}, &Error{Kind: ErrTransient, Message: "create video grid temp dir", Err: err}
	}
	defer os.RemoveAll(tmpDir)

	videoPath, err := m.materializeVideoFile(ctx, tmpDir, ref, loadedBytes)
	if err != nil {
		return cachedVideoGrid{}, err
	}

	durationSec := m.probeDurationSeconds(ctx, videoPath, ref.DurationMs)
	frameCount := m.gridCols * m.gridRows
	effDur := math.Max(durationSec, 0.25)
	fpsExpr := fmt.Sprintf("%.4f", float64(frameCount)/effDur)
	vf := fmt.Sprintf("fps=%s,scale=%d:-2,tile=%dx%d:padding=2:margin=2", fpsExpr, m.frameWidth, m.gridCols, m.gridRows)

	gridPath := filepath.Join(tmpDir, "grid.jpg")
	cmd := exec.CommandContext(ctx, m.ffmpegBin,
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", videoPath,
		"-vf", vf,
		"-frames:v", "1",
		"-q:v", "4",
		gridPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return cachedVideoGrid{}, &Error{
			Kind:    ErrTransient,
			Message: fmt.Sprintf("ffmpeg extract video grid: %s", strings.TrimSpace(string(out))),
			Err:     err,
		}
	}

	jpegBytes, err := os.ReadFile(gridPath)
	if err != nil {
		return cachedVideoGrid{}, &Error{Kind: ErrTransient, Message: "read video grid jpeg", Err: err}
	}
	return cachedVideoGrid{
		jpeg:       jpegBytes,
		durationMs: int64(math.Round(durationSec * 1000)),
	}, nil
}

func (m *MediaFallbackMiddleware) extractAndTranscribeVideoAudio(ctx context.Context, ref *MediaRef, loadedBytes []byte) (cachedVideoAudio, error) {
	tmpDir, err := os.MkdirTemp("", "omni-vidaud-*")
	if err != nil {
		return cachedVideoAudio{}, &Error{Kind: ErrTransient, Message: "create video audio temp dir", Err: err}
	}
	defer os.RemoveAll(tmpDir)

	videoPath, err := m.materializeVideoFile(ctx, tmpDir, ref, loadedBytes)
	if err != nil {
		return cachedVideoAudio{}, err
	}

	durationSec := m.probeDurationSeconds(ctx, videoPath, ref.DurationMs)
	durationMs := int64(math.Round(durationSec * 1000))

	if !m.hasAudioStream(ctx, videoPath) {
		return cachedVideoAudio{hasAudio: false, durationMs: durationMs}, nil
	}

	wavPath := filepath.Join(tmpDir, "audio.wav")
	cmd := exec.CommandContext(ctx, m.ffmpegBin,
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", videoPath,
		"-vn",
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		wavPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return cachedVideoAudio{}, &Error{
			Kind:    ErrTransient,
			Message: fmt.Sprintf("ffmpeg extract video audio: %s", strings.TrimSpace(string(out))),
			Err:     err,
		}
	}

	trResp, err := m.transcriber.Transcribe(ctx, TranscribeRequest{
		Path:     wavPath,
		MIMEType: "audio/wav",
	})
	if err != nil {
		return cachedVideoAudio{}, err
	}
	if trResp.DurationMs > 0 {
		durationMs = trResp.DurationMs
	}
	return cachedVideoAudio{
		hasAudio:   true,
		text:       strings.TrimSpace(trResp.Text),
		durationMs: durationMs,
	}, nil
}

func (m *MediaFallbackMiddleware) materializeVideoFile(ctx context.Context, tmpDir string, ref *MediaRef, loadedBytes []byte) (string, error) {
	if p := strings.TrimSpace(ref.Path); p != "" {
		return p, nil
	}
	data := loadedBytes
	if len(data) == 0 {
		var err error
		data, err = m.loadMediaBytes(ctx, ref)
		if err != nil {
			return "", err
		}
	}
	dst := filepath.Join(tmpDir, "video.mp4")
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", &Error{Kind: ErrTransient, Message: "write temp video", Err: err}
	}
	return dst, nil
}

func (m *MediaFallbackMiddleware) probeDurationSeconds(ctx context.Context, videoPath string, fallbackMs int64) float64 {
	cmd := exec.CommandContext(ctx, m.ffprobeBin,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		videoPath,
	)
	if out, err := cmd.Output(); err == nil {
		if sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil && sec > 0 {
			return sec
		}
	}
	if fallbackMs > 0 {
		return float64(fallbackMs) / 1000.0
	}
	return 0
}

func (m *MediaFallbackMiddleware) hasAudioStream(ctx context.Context, videoPath string) bool {
	cmd := exec.CommandContext(ctx, m.ffprobeBin,
		"-v", "error",
		"-select_streams", "a",
		"-show_entries", "stream=index",
		"-of", "csv=p=0",
		videoPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

func (m *MediaFallbackMiddleware) cacheKeyForMedia(ctx context.Context, ref *MediaRef) (string, []byte, error) {
	if id := strings.TrimSpace(ref.BlobID); id != "" {
		return "blob:" + id, ref.Data, nil
	}
	data, err := m.loadMediaBytes(ctx, ref)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), data, nil
}

func (m *MediaFallbackMiddleware) loadMediaBytes(ctx context.Context, ref *MediaRef) ([]byte, error) {
	switch {
	case len(ref.Data) > 0:
		return ref.Data, nil
	case strings.TrimSpace(ref.Path) != "":
		b, err := os.ReadFile(ref.Path)
		if err != nil {
			return nil, &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("read media path %q: %v", ref.Path, err), Err: err}
		}
		return b, nil
	case strings.TrimSpace(ref.BlobID) != "":
		if m.blobs == nil {
			return nil, &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("blob %q requires BlobReader", ref.BlobID)}
		}
		b, err := m.blobs.ReadBlob(ctx, ref.BlobID)
		if err != nil {
			return nil, &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("read blob %q: %v", ref.BlobID, err), Err: err}
		}
		return b, nil
	default:
		return nil, &Error{Kind: ErrInvalidArg, Message: "media reference has no Data, Path, or BlobID"}
	}
}

func formatDurationSeconds(ms int64) string {
	if ms <= 0 {
		return ""
	}
	secs := int(math.Round(float64(ms) / 1000.0))
	if secs < 1 {
		secs = 1
	}
	return fmt.Sprintf("%ds", secs)
}
