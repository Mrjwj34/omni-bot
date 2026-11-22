package ai_test

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"omni-bot/internal/ai"
)

type captureLanguageModel struct {
	caps     ai.Capabilities
	mu       sync.Mutex
	requests []ai.GenerateRequest
}

func (c *captureLanguageModel) Capabilities() ai.Capabilities { return c.caps }

func (c *captureLanguageModel) Generate(_ context.Context, req ai.GenerateRequest) (ai.GenerateResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	return ai.GenerateResponse{Text: "ok"}, nil
}

func (c *captureLanguageModel) lastRequest() ai.GenerateRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[len(c.requests)-1]
}

type countingTranscriber struct {
	calls atomic.Int32
	delay time.Duration
	resp  ai.TranscribeResponse
}

func (c *countingTranscriber) Transcribe(ctx context.Context, _ ai.TranscribeRequest) (ai.TranscribeResponse, error) {
	c.calls.Add(1)
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return ai.TranscribeResponse{}, ctx.Err()
		}
	}
	return c.resp, nil
}

func TestMediaFallbackPassthroughWhenSupported(t *testing.T) {
	inner := &captureLanguageModel{caps: ai.Capabilities{Vision: true, AudioIn: true, VideoIn: true}}
	asr := &countingTranscriber{resp: ai.TranscribeResponse{Text: "should not run"}}
	mw := ai.NewMediaFallbackMiddleware(inner, ai.FallbackConfig{Transcriber: asr})

	req := ai.GenerateRequest{
		Messages: []ai.Message{{
			Role: ai.RoleUser,
			Parts: []ai.Part{
				ai.MediaPart(ai.PartImage, ai.MediaRef{BlobID: "img1", Data: []byte("img")}),
				ai.MediaPart(ai.PartAudio, ai.MediaRef{BlobID: "aud1", Data: []byte("aud")}),
				ai.MediaPart(ai.PartVideo, ai.MediaRef{BlobID: "vid1", Data: []byte("vid")}),
			},
		}},
	}
	if _, err := mw.Generate(t.Context(), req); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if asr.calls.Load() != 0 {
		t.Fatalf("transcriber should not be called when native AudioIn/VideoIn supported")
	}
	got := inner.lastRequest().Messages[0].Parts
	if len(got) != 3 || got[0].Kind != ai.PartImage || got[1].Kind != ai.PartAudio || got[2].Kind != ai.PartVideo {
		t.Fatalf("expected untouched media parts, got %+v", got)
	}
}

func TestMediaFallbackImageAndAudioWithBlobCache(t *testing.T) {
	inner := &captureLanguageModel{caps: ai.Capabilities{Vision: false, AudioIn: false, VideoIn: false}}
	asr := &countingTranscriber{
		delay: 15 * time.Millisecond,
		resp:  ai.TranscribeResponse{Text: "晚上八点开黑", DurationMs: 4200},
	}
	mw := ai.NewMediaFallbackMiddleware(inner, ai.FallbackConfig{Transcriber: asr})

	req := ai.GenerateRequest{
		Messages: []ai.Message{{
			Role: ai.RoleUser,
			Parts: []ai.Part{
				ai.MediaPart(ai.PartImage, ai.MediaRef{Caption: "橘猫趴在键盘上"}),
				ai.MediaPart(ai.PartImage, ai.MediaRef{}),
				ai.MediaPart(ai.PartAudio, ai.MediaRef{BlobID: "voice-blob-1", Data: []byte("wav"), DurationMs: 4200}),
			},
		}},
	}

	// Concurrent calls with the same blob_id should deduplicate via single-flight and cache.
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := mw.Generate(t.Context(), req); err != nil {
				t.Errorf("Generate: %v", err)
			}
		}()
	}
	wg.Wait()

	if asr.calls.Load() != 1 {
		t.Fatalf("expected 1 ASR call due to blob_id cache/single-flight, got %d", asr.calls.Load())
	}

	parts := inner.lastRequest().Messages[0].Parts
	if len(parts) != 3 {
		t.Fatalf("len(parts) = %d, want 3", len(parts))
	}
	if parts[0].Kind != ai.PartText || parts[0].Text != "[图片: 橘猫趴在键盘上]" {
		t.Fatalf("parts[0] = %+v", parts[0])
	}
	if parts[1].Kind != ai.PartText || parts[1].Text != "[图片: 未提供视觉描述]" {
		t.Fatalf("parts[1] = %+v", parts[1])
	}
	if parts[2].Kind != ai.PartText || parts[2].Text != "[语音转写 (4s): 晚上八点开黑]" {
		t.Fatalf("parts[2] = %+v", parts[2])
	}
}

func TestMediaFallbackVideoWithRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found on PATH")
	}

	dir := t.TempDir()
	avPath := filepath.Join(dir, "clip_av.mp4")
	silentPath := filepath.Join(dir, "clip_silent.mp4")

	// Generate a 1s synthetic video WITH audio track.
	cmdAV := exec.CommandContext(t.Context(), "ffmpeg",
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest",
		avPath,
	)
	if out, err := cmdAV.CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic AV video: %v (%s)", err, string(out))
	}

	// Generate a 1s synthetic video WITHOUT audio track.
	cmdSilent := exec.CommandContext(t.Context(), "ffmpeg",
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=1",
		"-an", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		silentPath,
	)
	if out, err := cmdSilent.CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic silent video: %v (%s)", err, string(out))
	}

	t.Run("vision enabled + audio track yields JPEG grid + transcript + caches by blob_id", func(t *testing.T) {
		inner := &captureLanguageModel{caps: ai.Capabilities{Vision: true, AudioIn: false, VideoIn: false}}
		asr := &countingTranscriber{resp: ai.TranscribeResponse{Text: "测试视频台词", DurationMs: 1000}}
		mw := ai.NewMediaFallbackMiddleware(inner, ai.FallbackConfig{Transcriber: asr})

		req := ai.GenerateRequest{
			Messages: []ai.Message{{
				Role: ai.RoleUser,
				Parts: []ai.Part{
					ai.MediaPart(ai.PartVideo, ai.MediaRef{BlobID: "vid-av-1", Path: avPath, MIMEType: "video/mp4"}),
				},
			}},
		}
		if _, err := mw.Generate(t.Context(), req); err != nil {
			t.Fatalf("Generate #1: %v", err)
		}
		if _, err := mw.Generate(t.Context(), req); err != nil {
			t.Fatalf("Generate #2: %v", err)
		}
		if asr.calls.Load() != 1 {
			t.Fatalf("expected 1 ASR call with blob_id cache, got %d", asr.calls.Load())
		}

		parts := inner.lastRequest().Messages[0].Parts
		if len(parts) != 2 {
			t.Fatalf("expected 2 parts (grid image + transcript), got %d: %+v", len(parts), parts)
		}
		if parts[0].Kind != ai.PartImage || parts[0].Media == nil || parts[0].Media.MIMEType != "image/jpeg" {
			t.Fatalf("parts[0] should be JPEG grid image, got %+v", parts[0])
		}
		// Verify JPEG magic header FF D8.
		if !bytes.HasPrefix(parts[0].Media.Data, []byte{0xff, 0xd8}) {
			t.Fatalf("grid image does not start with JPEG magic bytes")
		}
		if parts[1].Kind != ai.PartText || !strings.Contains(parts[1].Text, "测试视频台词") {
			t.Fatalf("parts[1] = %+v", parts[1])
		}
	})

	t.Run("silent video skips ASR and degrades cleanly without vision", func(t *testing.T) {
		inner := &captureLanguageModel{caps: ai.Capabilities{Vision: false, AudioIn: false, VideoIn: false}}
		asr := &countingTranscriber{resp: ai.TranscribeResponse{Text: "should not be called"}}
		mw := ai.NewMediaFallbackMiddleware(inner, ai.FallbackConfig{Transcriber: asr})

		req := ai.GenerateRequest{
			Messages: []ai.Message{{
				Role: ai.RoleUser,
				Parts: []ai.Part{
					ai.MediaPart(ai.PartVideo, ai.MediaRef{BlobID: "vid-silent-1", Path: silentPath, Caption: "猫咪跳窗"}),
				},
			}},
		}
		if _, err := mw.Generate(t.Context(), req); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if asr.calls.Load() != 0 {
			t.Fatalf("silent video must not call transcriber, got %d calls", asr.calls.Load())
		}
		parts := inner.lastRequest().Messages[0].Parts
		if len(parts) != 1 || parts[0].Kind != ai.PartText || !strings.Contains(parts[0].Text, "猫咪跳窗") {
			t.Fatalf("expected metadata fallback text, got %+v", parts)
		}
	})
}
