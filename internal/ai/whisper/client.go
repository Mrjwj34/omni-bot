package whisper

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"omni-bot/internal/ai"
)

const (
	Protocol       = "whisper-stt"
	defaultTimeout = 2 * time.Minute
)

type Mode string

const (
	ModeAuto Mode = ""
	ModeCLI  Mode = "cli"
	ModeHTTP Mode = "http"
)

type Options struct {
	Mode              Mode
	FFmpegBin         string
	WhisperBin        string
	WhisperArgsPrefix []string
	HTTPClient        *http.Client
	BlobReader        ai.BlobReader
	Language          string
	Timeout           time.Duration
}

type Option func(*Options)

func WithMode(m Mode) Option {
	return func(o *Options) { o.Mode = m }
}

func WithFFmpegBin(bin string) Option {
	return func(o *Options) { o.FFmpegBin = bin }
}

func WithWhisperBin(bin string, argsPrefix ...string) Option {
	return func(o *Options) {
		o.WhisperBin = bin
		o.WhisperArgsPrefix = append([]string(nil), argsPrefix...)
	}
}

func WithHTTPClient(c *http.Client) Option {
	return func(o *Options) { o.HTTPClient = c }
}

func WithBlobReader(r ai.BlobReader) Option {
	return func(o *Options) { o.BlobReader = r }
}

func WithLanguage(lang string) Option {
	return func(o *Options) { o.Language = lang }
}

func WithTimeout(d time.Duration) Option {
	return func(o *Options) { o.Timeout = d }
}

// Client implements ai.TranscriptionModel using either local whisper-cli subprocess
// (with ffmpeg 16kHz mono PCM WAV transcoding) or a local OpenAI-compatible whisper-server.
type Client struct {
	mode              Mode
	provider          string
	modelID           string
	defaultLang       string
	timeout           time.Duration
	blobs             ai.BlobReader
	ffmpegBin         string
	whisperBin        string
	whisperArgsPrefix []string
	endpoint          string
	httpClient        *http.Client
	keys              *ai.KeyRotator
	headers           map[string]string
}

func RegisterFactory(reg *ai.Registry, opts ...Option) {
	reg.RegisterTranscriptionFactory(Protocol, func(rm ai.ResolvedModel) (ai.TranscriptionModel, error) {
		return New(rm, opts...)
	})
}

func New(resolved ai.ResolvedModel, opts ...Option) (*Client, error) {
	o := Options{
		FFmpegBin:  "ffmpeg",
		WhisperBin: "whisper-cli",
		Language:   "auto",
		Timeout:    resolved.Provider.Timeout,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if resolved.Model.DefaultParams != nil {
		if m, ok := resolved.Model.DefaultParams["mode"].(string); ok && o.Mode == ModeAuto {
			o.Mode = Mode(strings.ToLower(strings.TrimSpace(m)))
		}
		if b, ok := resolved.Model.DefaultParams["ffmpeg_bin"].(string); ok && strings.TrimSpace(b) != "" {
			o.FFmpegBin = strings.TrimSpace(b)
		}
		if b, ok := resolved.Model.DefaultParams["whisper_bin"].(string); ok && strings.TrimSpace(b) != "" {
			o.WhisperBin = strings.TrimSpace(b)
		}
		if l, ok := resolved.Model.DefaultParams["language"].(string); ok && strings.TrimSpace(l) != "" {
			o.Language = strings.TrimSpace(l)
		}
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}

	baseURL := strings.TrimSpace(resolved.Provider.BaseURL)
	mode := o.Mode
	if mode == ModeAuto {
		if strings.HasPrefix(baseURL, "http://") || strings.HasPrefix(baseURL, "https://") {
			mode = ModeHTTP
		} else {
			mode = ModeCLI
		}
	}

	c := &Client{
		mode:              mode,
		provider:          resolved.Provider.ID,
		modelID:           strings.TrimSpace(resolved.Model.ModelID),
		defaultLang:       o.Language,
		timeout:           o.Timeout,
		blobs:             o.BlobReader,
		ffmpegBin:         o.FFmpegBin,
		whisperBin:        o.WhisperBin,
		whisperArgsPrefix: o.WhisperArgsPrefix,
		keys:              resolved.Keys,
		headers:           resolved.Provider.Headers,
	}
	if c.keys == nil && len(resolved.Provider.APIKeys) > 0 {
		c.keys = ai.NewKeyRotator(resolved.Provider.APIKeys)
	}

	switch mode {
	case ModeCLI:
		if c.modelID == "" {
			return nil, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "whisper cli mode requires ModelID (model file path)"}
		}
	case ModeHTTP:
		if baseURL == "" {
			return nil, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "whisper http mode requires BaseURL"}
		}
		hc, err := buildHTTPClient(resolved.Provider, o.HTTPClient)
		if err != nil {
			return nil, err
		}
		c.httpClient = hc
		c.endpoint = normalizeTranscribeEndpoint(baseURL)
	default:
		return nil, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: fmt.Sprintf("unsupported whisper mode %q", mode)}
	}

	return c, nil
}

func (c *Client) Transcribe(ctx context.Context, req ai.TranscribeRequest) (ai.TranscribeResponse, error) {
	if c.mode == ModeHTTP {
		return c.transcribeHTTP(ctx, req)
	}
	return c.transcribeCLI(ctx, req)
}

func (c *Client) transcribeCLI(ctx context.Context, req ai.TranscribeRequest) (ai.TranscribeResponse, error) {
	runCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && c.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	tmpDir, err := os.MkdirTemp("", "omni-whisper-*")
	if err != nil {
		return ai.TranscribeResponse{}, &ai.Error{Kind: ai.ErrTransient, Provider: c.provider, Message: "create whisper temp dir", Err: err}
	}
	defer os.RemoveAll(tmpDir)

	inputPath, err := c.materializeInputFile(runCtx, tmpDir, req)
	if err != nil {
		return ai.TranscribeResponse{}, err
	}

	wavPath := filepath.Join(tmpDir, "audio.wav")
	ffCmd := exec.CommandContext(runCtx, c.ffmpegBin,
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", inputPath,
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		wavPath,
	)
	if out, err := ffCmd.CombinedOutput(); err != nil {
		return ai.TranscribeResponse{}, classifyExecError(c.provider, "ffmpeg transcode", err, out)
	}

	durationMs := wavDurationMs(wavPath)

	lang := strings.TrimSpace(req.Language)
	if lang == "" {
		lang = c.defaultLang
	}
	if lang == "" {
		lang = "auto"
	}

	outPrefix := filepath.Join(tmpDir, "transcript")
	args := make([]string, 0, len(c.whisperArgsPrefix)+10)
	args = append(args, c.whisperArgsPrefix...)
	args = append(args,
		"-m", c.modelID,
		"-f", wavPath,
		"-l", lang,
		"-oj",
		"-of", outPrefix,
	)
	wCmd := exec.CommandContext(runCtx, c.whisperBin, args...)
	if out, err := wCmd.CombinedOutput(); err != nil {
		return ai.TranscribeResponse{}, classifyExecError(c.provider, "whisper-cli", err, out)
	}

	jsonPath := outPrefix + ".json"
	raw, err := os.ReadFile(jsonPath)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = os.ReadFile(wavPath + ".json")
	}
	if err != nil {
		return ai.TranscribeResponse{}, &ai.Error{
			Kind:     ai.ErrTransient,
			Provider: c.provider,
			Message:  fmt.Sprintf("read whisper json output: %v", err),
			Err:      err,
		}
	}

	parsed, err := parseWhisperCLIJSON(raw)
	if err != nil {
		return ai.TranscribeResponse{}, &ai.Error{
			Kind:     ai.ErrTransient,
			Provider: c.provider,
			Message:  fmt.Sprintf("parse whisper json output: %v", err),
			Err:      err,
		}
	}
	if parsed.DurationMs == 0 && durationMs > 0 {
		parsed.DurationMs = durationMs
	}
	return parsed, nil
}

type whisperCLIJSON struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []struct {
		Text    string `json:"text"`
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
	} `json:"transcription"`
}

func parseWhisperCLIJSON(raw []byte) (ai.TranscribeResponse, error) {
	var doc whisperCLIJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ai.TranscribeResponse{}, err
	}
	parts := make([]string, 0, len(doc.Transcription))
	var maxTo int64
	for _, seg := range doc.Transcription {
		if t := strings.TrimSpace(seg.Text); t != "" {
			parts = append(parts, t)
		}
		if seg.Offsets.To > maxTo {
			maxTo = seg.Offsets.To
		}
	}
	return ai.TranscribeResponse{
		Text:       strings.TrimSpace(strings.Join(parts, " ")),
		Language:   strings.TrimSpace(doc.Result.Language),
		DurationMs: maxTo,
	}, nil
}

func (c *Client) transcribeHTTP(ctx context.Context, req ai.TranscribeRequest) (ai.TranscribeResponse, error) {
	audioBytes, filename, err := c.loadInputBytes(ctx, req)
	if err != nil {
		return ai.TranscribeResponse{}, err
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return ai.TranscribeResponse{}, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "create multipart file", Err: err}
	}
	if _, err := part.Write(audioBytes); err != nil {
		return ai.TranscribeResponse{}, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "write multipart file", Err: err}
	}
	if c.modelID != "" {
		_ = mw.WriteField("model", c.modelID)
	}
	lang := strings.TrimSpace(req.Language)
	if lang == "" {
		lang = c.defaultLang
	}
	if lang != "" && !strings.EqualFold(lang, "auto") {
		_ = mw.WriteField("language", lang)
	}
	_ = mw.WriteField("response_format", "verbose_json")
	if err := mw.Close(); err != nil {
		return ai.TranscribeResponse{}, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "close multipart writer", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &buf)
	if err != nil {
		return ai.TranscribeResponse{}, &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "build transcription request", Err: err}
	}
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	if c.keys != nil && c.keys.Len() > 0 {
		key, err := c.keys.Next()
		if err != nil {
			return ai.TranscribeResponse{}, &ai.Error{Kind: ai.ErrAuth, Provider: c.provider, Message: "select api key", Err: err}
		}
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return ai.TranscribeResponse{}, classifyTransportError(c.provider, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ai.TranscribeResponse{}, classifyTransportError(c.provider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 400 {
			msg = msg[:400] + "..."
		}
		return ai.TranscribeResponse{}, &ai.Error{
			Kind:       ai.ClassifyHTTPStatus(resp.StatusCode, msg),
			StatusCode: resp.StatusCode,
			Provider:   c.provider,
			Message:    msg,
		}
	}

	var parsed struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ai.TranscribeResponse{}, &ai.Error{
			Kind:     ai.ErrTransient,
			Provider: c.provider,
			Message:  fmt.Sprintf("decode transcription response: %v", err),
			Err:      err,
		}
	}
	return ai.TranscribeResponse{
		Text:       strings.TrimSpace(parsed.Text),
		Language:   strings.TrimSpace(parsed.Language),
		DurationMs: int64(parsed.Duration * 1000),
	}, nil
}

func (c *Client) materializeInputFile(ctx context.Context, tmpDir string, req ai.TranscribeRequest) (string, error) {
	if p := strings.TrimSpace(req.Path); p != "" {
		return p, nil
	}
	data, filename, err := c.loadInputBytes(ctx, req)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(tmpDir, filename)
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", &ai.Error{Kind: ai.ErrTransient, Provider: c.provider, Message: "write temp audio", Err: err}
	}
	return dst, nil
}

func (c *Client) loadInputBytes(ctx context.Context, req ai.TranscribeRequest) ([]byte, string, error) {
	ext := extFromMIME(req.MIMEType)
	switch {
	case len(req.Audio) > 0:
		return req.Audio, "audio" + ext, nil
	case strings.TrimSpace(req.Path) != "":
		data, err := os.ReadFile(req.Path)
		if err != nil {
			return nil, "", &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: fmt.Sprintf("read audio file %q: %v", req.Path, err), Err: err}
		}
		name := filepath.Base(req.Path)
		if name == "" || name == "." {
			name = "audio" + ext
		}
		return data, name, nil
	case strings.TrimSpace(req.BlobID) != "":
		if c.blobs == nil {
			return nil, "", &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: fmt.Sprintf("blob %q requires BlobReader", req.BlobID)}
		}
		data, err := c.blobs.ReadBlob(ctx, req.BlobID)
		if err != nil {
			return nil, "", &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: fmt.Sprintf("read audio blob %q: %v", req.BlobID, err), Err: err}
		}
		return data, "audio" + ext, nil
	default:
		return nil, "", &ai.Error{Kind: ai.ErrInvalidArg, Provider: c.provider, Message: "transcribe request requires Audio, Path, or BlobID"}
	}
}

func wavDurationMs(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 44 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return 0
	}
	var (
		byteRate uint32
		dataSize uint32
	)
	for off := 12; off+8 <= len(raw); {
		chunkID := string(raw[off : off+4])
		chunkSize := binary.LittleEndian.Uint32(raw[off+4 : off+8])
		dataStart := off + 8
		if chunkID == "fmt " && dataStart+12 <= len(raw) {
			byteRate = binary.LittleEndian.Uint32(raw[dataStart+8 : dataStart+12])
		}
		if chunkID == "data" {
			dataSize = chunkSize
			break
		}
		off = dataStart + int(chunkSize)
		if chunkSize%2 == 1 {
			off++
		}
	}
	if byteRate == 0 || dataSize == 0 {
		return 0
	}
	return int64(uint64(dataSize) * 1000 / uint64(byteRate))
}

func normalizeTranscribeEndpoint(baseURL string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(trimmed, "/audio/transcriptions") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/audio/transcriptions"
	}
	return trimmed + "/v1/audio/transcriptions"
}

func extFromMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg", "audio/opus":
		return ".ogg"
	case "audio/mp4", "audio/m4a":
		return ".m4a"
	case "audio/flac":
		return ".flac"
	default:
		return ".wav"
	}
}

func classifyExecError(provider, step string, err error, stderr []byte) error {
	if errors.Is(err, context.Canceled) {
		return &ai.Error{Kind: ai.ErrUnknown, Provider: provider, Message: step + " canceled", Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ai.Error{Kind: ai.ErrTransient, Provider: provider, Message: step + " timed out", Err: err}
	}
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		msg = err.Error()
	}
	if len(msg) > 400 {
		msg = msg[:400] + "..."
	}
	kind := ai.ErrTransient
	if errors.Is(err, exec.ErrNotFound) {
		kind = ai.ErrInvalidArg
	}
	return &ai.Error{Kind: kind, Provider: provider, Message: fmt.Sprintf("%s failed: %s", step, msg), Err: err}
}

func classifyTransportError(provider string, err error) error {
	if errors.Is(err, context.Canceled) {
		return &ai.Error{Kind: ai.ErrUnknown, Provider: provider, Message: "request canceled", Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ai.Error{Kind: ai.ErrTransient, Provider: provider, Message: "request timed out", Err: err}
	}
	kind := ai.ErrorKindOf(err)
	if kind == "" {
		kind = ai.ErrTransient
	}
	return &ai.Error{Kind: kind, Provider: provider, Message: err.Error(), Err: err}
}

func buildHTTPClient(cfg ai.ProviderConfig, custom *http.Client) (*http.Client, error) {
	if custom != nil {
		return custom, nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.ProxyURL != "" {
		u, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, &ai.Error{Kind: ai.ErrInvalidArg, Provider: cfg.ID, Message: fmt.Sprintf("invalid proxy url: %v", err), Err: err}
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{
		Timeout:   cfg.Timeout,
		Transport: tr,
	}, nil
}
