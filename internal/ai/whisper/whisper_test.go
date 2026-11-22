package whisper_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/ai/whisper"
)

// TestWhisperHelperProcess acts as a cross-platform subprocess fake for whisper-cli
// when invoked with GO_WANT_WHISPER_HELPER=1.
func TestWhisperHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_WHISPER_HELPER") != "1" {
		return
	}
	defer os.Exit(0)

	args := os.Args
	var sep int
	for i, a := range args {
		if a == "--" {
			sep = i + 1
			break
		}
	}
	cliArgs := args[sep:]
	var (
		modelPath string
		wavPath   string
		lang      string
		outPrefix string
		hasOJ     bool
	)
	for i := 0; i < len(cliArgs); i++ {
		switch cliArgs[i] {
		case "-m":
			i++
			modelPath = cliArgs[i]
		case "-f":
			i++
			wavPath = cliArgs[i]
		case "-l":
			i++
			lang = cliArgs[i]
		case "-of":
			i++
			outPrefix = cliArgs[i]
		case "-oj":
			hasOJ = true
		}
	}

	if os.Getenv("WHISPER_HELPER_SLEEP") == "1" {
		time.Sleep(10 * time.Second)
	}

	if modelPath == "" || wavPath == "" || outPrefix == "" || !hasOJ {
		_, _ = io.WriteString(os.Stderr, "missing required whisper-cli args")
		os.Exit(2)
	}

	rawWav, err := os.ReadFile(wavPath)
	if err != nil || len(rawWav) < 44 || string(rawWav[:4]) != "RIFF" {
		_, _ = io.WriteString(os.Stderr, "invalid wav input")
		os.Exit(3)
	}
	// Validate 16kHz mono PCM format produced by ffmpeg.
	channels := binary.LittleEndian.Uint16(rawWav[22:24])
	sampleRate := binary.LittleEndian.Uint32(rawWav[24:28])
	if channels != 1 || sampleRate != 16000 {
		_, _ = io.WriteString(os.Stderr, "expected 16kHz mono wav")
		os.Exit(4)
	}

	outDoc := map[string]any{
		"result": map[string]any{"language": lang},
		"transcription": []map[string]any{
			{
				"offsets": map[string]any{"from": 0, "to": 1000},
				"text":    " 你好，这是测试语音 ",
			},
		},
	}
	encoded, _ := json.Marshal(outDoc)
	if err := os.WriteFile(outPrefix+".json", encoded, 0o600); err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error())
		os.Exit(5)
	}
}

func TestWhisperHTTPMode(t *testing.T) {
	var (
		gotModel    string
		gotLang     string
		gotFormat   string
		gotFile     []byte
		gotAuth     string
		gotFileName string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.Error(w, "bad path "+r.URL.Path, http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(p)
			switch p.FormName() {
			case "file":
				gotFile = data
				gotFileName = p.FileName()
			case "model":
				gotModel = string(data)
			case "language":
				gotLang = string(data)
			case "response_format":
				gotFormat = string(data)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"今天一起吃饭吗","language":"zh","duration":2.5}`))
	}))
	defer srv.Close()

	client, err := whisper.New(ai.ResolvedModel{
		Provider: ai.ProviderConfig{
			ID:      "whisper-http",
			Kind:    "whisper",
			BaseURL: srv.URL,
			APIKeys: []string{"local-secret"},
		},
		Model: ai.ModelConfig{
			ID:         "asr",
			ProviderID: "whisper-http",
			Protocol:   whisper.Protocol,
			ModelID:    "whisper-large-v3",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := client.Transcribe(t.Context(), ai.TranscribeRequest{
		Audio:    []byte("fake-ogg-bytes"),
		MIMEType: "audio/ogg",
		Language: "zh",
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if resp.Text != "今天一起吃饭吗" || resp.Language != "zh" || resp.DurationMs != 2500 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if gotModel != "whisper-large-v3" || gotLang != "zh" || gotFormat != "verbose_json" {
		t.Fatalf("unexpected form fields: model=%q lang=%q format=%q", gotModel, gotLang, gotFormat)
	}
	if gotFileName != "audio.ogg" || string(gotFile) != "fake-ogg-bytes" {
		t.Fatalf("unexpected uploaded file: name=%q data=%q", gotFileName, string(gotFile))
	}
	if gotAuth != "Bearer local-secret" {
		t.Fatalf("Authorization = %q, want Bearer local-secret", gotAuth)
	}
}

func TestWhisperHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client, err := whisper.New(ai.ResolvedModel{
		Provider: ai.ProviderConfig{ID: "w", BaseURL: srv.URL},
		Model:    ai.ModelConfig{ID: "asr", ProviderID: "w", Protocol: whisper.Protocol, ModelID: "base"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Transcribe(t.Context(), ai.TranscribeRequest{Audio: []byte("abc")})
	if err == nil || ai.ErrorKindOf(err) != ai.ErrTransient || !ai.IsRetryable(err) {
		t.Fatalf("expected retryable transient error, got %v", err)
	}
}

func TestWhisperCLIModeWithRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}

	// Generate a real 1s 44.1kHz stereo WAV to verify ffmpeg transcoding to 16kHz mono PCM.
	dir := t.TempDir()
	srcWav := filepath.Join(dir, "src.wav")
	genCmd := exec.CommandContext(t.Context(), "ffmpeg",
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=1",
		"-ac", "2",
		srcWav,
	)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("generate test audio via ffmpeg: %v (%s)", err, string(out))
	}

	t.Setenv("GO_WANT_WHISPER_HELPER", "1")
	client, err := whisper.New(
		ai.ResolvedModel{
			Provider: ai.ProviderConfig{ID: "whisper-cli"},
			Model:    ai.ModelConfig{ID: "asr", ProviderID: "whisper-cli", Protocol: whisper.Protocol, ModelID: "ggml-base.bin"},
		},
		whisper.WithMode(whisper.ModeCLI),
		whisper.WithWhisperBin(os.Args[0], "-test.run=^TestWhisperHelperProcess$", "--"),
	)
	if err != nil {
		t.Fatalf("New CLI: %v", err)
	}

	resp, err := client.Transcribe(t.Context(), ai.TranscribeRequest{
		Path:     srcWav,
		Language: "auto",
	})
	if err != nil {
		t.Fatalf("Transcribe CLI: %v", err)
	}
	if resp.Text != "你好，这是测试语音" {
		t.Fatalf("resp.Text = %q, want 你好，这是测试语音", resp.Text)
	}
	if resp.DurationMs != 1000 {
		t.Fatalf("resp.DurationMs = %d, want 1000", resp.DurationMs)
	}

	// Verify context timeout stops hung CLI process.
	t.Setenv("WHISPER_HELPER_SLEEP", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	_, err = client.Transcribe(ctx, ai.TranscribeRequest{Path: srcWav})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "deadline") {
		if ai.ErrorKindOf(err) != ai.ErrTransient {
			t.Fatalf("expected transient timeout error, got %v", err)
		}
	}
}
