package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"omni-bot/internal/protocol"
)

const (
	defaultChannelID       = "napcat"
	defaultMaxForwardDepth = 3
	defaultMaxMediaBytes   = 128 << 20
)

var (
	bvidPattern = regexp.MustCompile(`\bBV[0-9A-Za-z]{10}\b`)

	bilibiliTrackingParams = map[string]struct{}{
		"bbid":             {},
		"buvid":            {},
		"is_story_h5":      {},
		"mid":              {},
		"plat_id":          {},
		"share_from":       {},
		"share_medium":     {},
		"share_plat":       {},
		"share_session_id": {},
		"share_source":     {},
		"share_tag":        {},
		"spm_id_from":      {},
		"timestamp":        {},
		"ts":               {},
		"unique_k":         {},
		"up_id":            {},
		"vd_source":        {},
	}
)

// FlexID unmarshals OneBot v11 IDs that NapCat may serialize as either integers or strings.
type FlexID string

func (id *FlexID) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*id = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return fmt.Errorf("unmarshal flex id string: %w", err)
		}
		*id = FlexID(s)
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var num json.Number
	if err := dec.Decode(&num); err != nil {
		return fmt.Errorf("unmarshal flex id number: %w", err)
	}
	*id = FlexID(num.String())
	return nil
}

type OneBotSender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
	Role     string `json:"role,omitempty"`
}

type OneBotSegment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type OneBotEvent struct {
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type,omitempty"`
	SelfID      int64           `json:"self_id"`
	GroupID     int64           `json:"group_id"`
	UserID      int64           `json:"user_id"`
	MessageID   FlexID          `json:"message_id"`
	Time        int64           `json:"time"`
	Sender      OneBotSender    `json:"sender"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message,omitempty"`
}

// OneBotForwardNode accommodates both standard OneBot v11 node wrappers and NapCat's flattened forward entries.
type OneBotForwardNode struct {
	Type     string          `json:"type,omitempty"`
	Data     *OneBotNodeData `json:"data,omitempty"`
	UserID   FlexID          `json:"user_id,omitempty"`
	Nickname string          `json:"nickname,omitempty"`
	Sender   *OneBotSender   `json:"sender,omitempty"`
	Time     int64           `json:"time,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
	Message  json.RawMessage `json:"message,omitempty"`
}

type OneBotNodeData struct {
	UserID   FlexID          `json:"user_id,omitempty"`
	Uin      FlexID          `json:"uin,omitempty"`
	Nickname string          `json:"nickname,omitempty"`
	Name     string          `json:"name,omitempty"`
	Time     int64           `json:"time,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
	Message  json.RawMessage `json:"message,omitempty"`
}

func (n OneBotForwardNode) fields() (senderID, senderName string, timestampMs int64, raw json.RawMessage) {
	if n.Data != nil {
		senderID = firstNonEmpty(string(n.Data.UserID), string(n.Data.Uin))
		senderName = firstNonEmpty(n.Data.Nickname, n.Data.Name)
		if n.Data.Time > 0 {
			timestampMs = toMillis(n.Data.Time)
		}
		raw = firstRaw(n.Data.Content, n.Data.Message)
	}
	if senderID == "" {
		if n.Sender != nil && n.Sender.UserID != 0 {
			senderID = strconv.FormatInt(n.Sender.UserID, 10)
		} else {
			senderID = string(n.UserID)
		}
	}
	if senderName == "" {
		if n.Sender != nil {
			senderName = firstNonEmpty(n.Sender.Card, n.Sender.Nickname)
		}
		if senderName == "" {
			senderName = n.Nickname
		}
	}
	if timestampMs == 0 && n.Time > 0 {
		timestampMs = toMillis(n.Time)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = firstRaw(n.Content, n.Message)
	}
	return senderID, senderName, timestampMs, raw
}

type OneBotFileResult struct {
	File     string `json:"file,omitempty"`
	URL      string `json:"url,omitempty"`
	Base64   string `json:"base64,omitempty"`
	FileName string `json:"file_name,omitempty"`
	FileSize FlexID `json:"file_size,omitempty"`
}

type OneBotGroupMember struct {
	GroupID  int64  `json:"group_id"`
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
	Role     string `json:"role,omitempty"`
}

type BlobUploader interface {
	Upload(ctx context.Context, mimeType string, data []byte) (protocol.MediaRef, error)
}

type ForwardFetcher interface {
	GetForwardMsg(ctx context.Context, forwardID string) ([]OneBotForwardNode, error)
}

type MediaHelper interface {
	GetRecord(ctx context.Context, file, outFormat string) (OneBotFileResult, error)
	GetFile(ctx context.Context, fileID string) (OneBotFileResult, error)
}

type AudioTranscodeFunc func(ctx context.Context, data []byte, srcMIME string) ([]byte, string, error)

type HTTPBlobClient struct {
	baseURL    string
	httpClient *http.Client
	maxBytes   int64
}

func NewHTTPBlobClient(baseURL string, client *http.Client) *HTTPBlobClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPBlobClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: client,
		maxBytes:   defaultMaxMediaBytes,
	}
}

func (c *HTTPBlobClient) Upload(ctx context.Context, mimeType string, data []byte) (protocol.MediaRef, error) {
	if len(data) == 0 {
		return protocol.MediaRef{}, errors.New("upload blob: empty payload")
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/blobs", bytes.NewReader(data))
	if err != nil {
		return protocol.MediaRef{}, fmt.Errorf("build blob upload request: %w", err)
	}
	req.Header.Set("Content-Type", mimeType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return protocol.MediaRef{}, fmt.Errorf("upload blob to kernel: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return protocol.MediaRef{}, fmt.Errorf("upload blob status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var ref protocol.MediaRef
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		return protocol.MediaRef{}, fmt.Errorf("decode blob upload response: %w", err)
	}
	if ref.BlobID == "" {
		return protocol.MediaRef{}, errors.New("blob upload response missing blob_id")
	}
	if ref.MIMEType == "" {
		ref.MIMEType = mimeType
	}
	if ref.SizeBytes == 0 {
		ref.SizeBytes = int64(len(data))
	}
	return ref, nil
}

func (c *HTTPBlobClient) Download(ctx context.Context, blobID string) ([]byte, string, error) {
	if blobID == "" {
		return nil, "", errors.New("download blob: empty blob_id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/blobs/"+url.PathEscape(blobID), nil)
	if err != nil {
		return nil, "", fmt.Errorf("build blob download request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download blob %q: %w", blobID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("download blob %q status %d: %s", blobID, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	data, err := readLimited(resp.Body, c.maxBytes)
	if err != nil {
		return nil, "", fmt.Errorf("read blob %q: %w", blobID, err)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	return data, ct, nil
}

type NormalizerOptions struct {
	ChannelID       string
	Blobs           BlobUploader
	Forwards        ForwardFetcher
	Media           MediaHelper
	HTTPClient      *http.Client
	AudioTranscoder AudioTranscodeFunc
	FFmpegPath      string
	MaxForwardDepth int
	MaxMediaBytes   int64
	Logger          *slog.Logger
	Now             func() time.Time
}

type Normalizer struct {
	channelID       string
	blobs           BlobUploader
	forwards        ForwardFetcher
	media           MediaHelper
	httpClient      *http.Client
	audioTranscoder AudioTranscodeFunc
	maxForwardDepth int
	maxMediaBytes   int64
	logger          *slog.Logger
	now             func() time.Time

	nickMu sync.RWMutex
	nicks  map[string]string
}

func NewNormalizer(opts NormalizerOptions) *Normalizer {
	channelID := opts.ChannelID
	if channelID == "" {
		channelID = defaultChannelID
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	maxDepth := opts.MaxForwardDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxForwardDepth
	}
	maxMedia := opts.MaxMediaBytes
	if maxMedia <= 0 {
		maxMedia = defaultMaxMediaBytes
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	transcoder := opts.AudioTranscoder
	if transcoder == nil {
		ffmpegPath := opts.FFmpegPath
		if ffmpegPath == "" {
			ffmpegPath = "ffmpeg"
		}
		transcoder = NewFFmpegAudioTranscoder(ffmpegPath)
	}

	return &Normalizer{
		channelID:       channelID,
		blobs:           opts.Blobs,
		forwards:        opts.Forwards,
		media:           opts.Media,
		httpClient:      httpClient,
		audioTranscoder: transcoder,
		maxForwardDepth: maxDepth,
		maxMediaBytes:   maxMedia,
		logger:          logger.With("component", "napcat.normalizer"),
		now:             nowFn,
		nicks:           make(map[string]string),
	}
}

func (n *Normalizer) NormalizeGroupMessage(ctx context.Context, ev OneBotEvent) (protocol.InboundMessage, bool, error) {
	if ev.PostType != "message" || ev.MessageType != "group" {
		return protocol.InboundMessage{}, false, nil
	}
	if ev.GroupID == 0 {
		return protocol.InboundMessage{}, false, errors.New("group message missing group_id")
	}

	segs, err := decodeSegments(ev.Message)
	if err != nil {
		return protocol.InboundMessage{}, false, fmt.Errorf("decode message segments: %w", err)
	}

	tsMs := toMillis(ev.Time)
	if tsMs == 0 {
		tsMs = n.now().UnixMilli()
	}

	senderIDStr := strconv.FormatInt(ev.UserID, 10)
	senderName := firstNonEmpty(ev.Sender.Card, ev.Sender.Nickname, senderIDStr)
	if ev.UserID != 0 && senderName != senderIDStr {
		n.rememberNick(senderIDStr, senderName)
	}
	msg := protocol.InboundMessage{
		ChannelID:   n.channelID,
		RoomID:      strconv.FormatInt(ev.GroupID, 10),
		MsgID:       string(ev.MessageID),
		SenderID:    senderIDStr,
		SenderName:  senderName,
		IsBot:       ev.SelfID != 0 && ev.UserID == ev.SelfID,
		TimestampMs: tsMs,
	}

	blocks, replyTo, isAtBot := n.normalizeSegments(ctx, segs, ev.SelfID, 0)
	msg.Blocks = blocks
	msg.ReplyToMsgID = replyTo
	msg.IsAtBot = isAtBot
	return msg, true, nil
}

func (n *Normalizer) normalizeSegments(
	ctx context.Context,
	segs []OneBotSegment,
	selfID int64,
	depth int,
) (blocks []protocol.ContentBlock, replyTo string, isAtBot bool) {
	selfStr := ""
	if selfID != 0 {
		selfStr = strconv.FormatInt(selfID, 10)
	}

	for _, seg := range segs {
		switch seg.Type {
		case "text":
			text := segString(seg.Data, "text")
			if text != "" {
				blocks = append(blocks, protocol.ContentBlock{
					Type: protocol.BlockText,
					Text: text,
				})
			}

		case "at":
			qq := segString(seg.Data, "qq")
			if qq == "" {
				continue
			}
			targetName := segString(seg.Data, "name")
			if selfStr != "" && qq == selfStr {
				isAtBot = true
				if targetName == "" {
					targetName = "我"
				}
			} else if targetName == "" {
				targetName = n.lookupNick(qq)
			}
			blocks = append(blocks, protocol.ContentBlock{
				Type:       protocol.BlockMention,
				TargetID:   qq,
				TargetName: targetName,
			})

		case "reply":
			id := segString(seg.Data, "id")
			if id == "" {
				continue
			}
			if replyTo == "" {
				replyTo = id
			}
			blocks = append(blocks, protocol.ContentBlock{
				Type:         protocol.BlockReplyRef,
				ReplyToMsgID: id,
			})

		case "image":
			blocks = append(blocks, n.normalizeImage(ctx, seg.Data))

		case "record":
			blocks = append(blocks, n.normalizeRecord(ctx, seg.Data))

		case "video":
			blocks = append(blocks, n.normalizeVideo(ctx, seg.Data))

		case "forward":
			blocks = append(blocks, n.normalizeForward(ctx, seg.Data, selfID, depth))

		case "json":
			cardBlocks := unpackJSONCard(segString(seg.Data, "data"))
			blocks = append(blocks, cardBlocks...)
		}
	}
	return blocks, replyTo, isAtBot
}

func (n *Normalizer) rememberNick(userID, name string) {
	if userID == "" || name == "" {
		return
	}
	n.nickMu.Lock()
	if n.nicks == nil {
		n.nicks = make(map[string]string)
	}
	n.nicks[userID] = name
	n.nickMu.Unlock()
}

func (n *Normalizer) lookupNick(userID string) string {
	n.nickMu.RLock()
	name := n.nicks[userID]
	n.nickMu.RUnlock()
	return name
}

func (n *Normalizer) normalizeImage(ctx context.Context, data map[string]any) protocol.ContentBlock {
	summary := segString(data, "summary")
	raw, mimeType, err := n.fetchSegmentBytes(ctx, "image", data)
	if err != nil {
		n.logger.Warn("fetch image segment failed", "err", err)
		return mediaFallbackBlock("[图片]", summary)
	}
	if n.blobs == nil {
		n.logger.Warn("blob uploader not configured; dropping binary image")
		return mediaFallbackBlock("[图片]", summary)
	}

	ref, err := n.blobs.Upload(ctx, mimeType, raw)
	if err != nil {
		n.logger.Warn("upload image blob failed", "err", err)
		return mediaFallbackBlock("[图片]", summary)
	}
	meta := make(map[string]any)
	if summary != "" {
		meta["summary"] = summary
	}
	if subType := segString(data, "sub_type"); subType != "" {
		meta["sub_type"] = subType
	} else if subType := segString(data, "subType"); subType != "" {
		meta["sub_type"] = subType
	}
	if len(meta) > 0 {
		ref.Meta = meta
	}
	return protocol.ContentBlock{
		Type:        protocol.BlockImage,
		Description: summary,
		Media:       &ref,
	}
}

func (n *Normalizer) normalizeRecord(ctx context.Context, data map[string]any) protocol.ContentBlock {
	raw, mimeType, err := n.fetchRecordBytes(ctx, data)
	if err != nil {
		n.logger.Warn("fetch record segment failed", "err", err)
		return mediaFallbackBlock("[语音]", "")
	}

	if n.audioTranscoder != nil {
		converted, convertedMIME, err := n.audioTranscoder(ctx, raw, mimeType)
		if err != nil {
			n.logger.Warn("transcode audio failed; uploading original bytes", "err", err)
		} else if len(converted) > 0 {
			raw = converted
			if convertedMIME != "" {
				mimeType = convertedMIME
			}
		}
	}

	if n.blobs == nil {
		n.logger.Warn("blob uploader not configured; dropping binary audio")
		return mediaFallbackBlock("[语音]", "")
	}
	ref, err := n.blobs.Upload(ctx, mimeType, raw)
	if err != nil {
		n.logger.Warn("upload audio blob failed", "err", err)
		return mediaFallbackBlock("[语音]", "")
	}
	return protocol.ContentBlock{
		Type:  protocol.BlockAudio,
		Media: &ref,
	}
}

func (n *Normalizer) normalizeVideo(ctx context.Context, data map[string]any) protocol.ContentBlock {
	raw, mimeType, err := n.fetchSegmentBytes(ctx, "video", data)
	if err != nil {
		n.logger.Warn("fetch video segment failed", "err", err)
		return mediaFallbackBlock("[视频]", "")
	}
	if n.blobs == nil {
		n.logger.Warn("blob uploader not configured; dropping binary video")
		return mediaFallbackBlock("[视频]", "")
	}
	ref, err := n.blobs.Upload(ctx, mimeType, raw)
	if err != nil {
		n.logger.Warn("upload video blob failed", "err", err)
		return mediaFallbackBlock("[视频]", "")
	}
	return protocol.ContentBlock{
		Type:  protocol.BlockVideo,
		Media: &ref,
	}
}

func (n *Normalizer) normalizeForward(ctx context.Context, data map[string]any, selfID int64, depth int) protocol.ContentBlock {
	if depth >= n.maxForwardDepth {
		return protocol.ContentBlock{
			Type: protocol.BlockText,
			Text: fmt.Sprintf("[合并转发已达最大展开深度 %d]", n.maxForwardDepth),
		}
	}

	var nodes []OneBotForwardNode
	if inline, ok := data["content"]; ok && inline != nil {
		if raw, err := json.Marshal(inline); err == nil {
			_ = json.Unmarshal(raw, &nodes)
		}
	}

	if len(nodes) == 0 {
		forwardID := segString(data, "id")
		if forwardID == "" || n.forwards == nil {
			return protocol.ContentBlock{
				Type: protocol.BlockText,
				Text: "[合并转发]",
			}
		}
		fetched, err := n.forwards.GetForwardMsg(ctx, forwardID)
		if err != nil {
			n.logger.Warn("get_forward_msg failed", "forward_id", forwardID, "err", err)
			return protocol.ContentBlock{
				Type: protocol.BlockText,
				Text: "[合并转发展开失败]",
			}
		}
		nodes = fetched
	}

	out := make([]protocol.ForwardNode, 0, len(nodes))
	for _, node := range nodes {
		senderID, senderName, tsMs, rawSegs := node.fields()
		segs, err := decodeSegments(rawSegs)
		if err != nil {
			n.logger.Warn("decode forward node segments failed", "err", err)
			continue
		}
		childBlocks, _, _ := n.normalizeSegments(ctx, segs, selfID, depth+1)
		out = append(out, protocol.ForwardNode{
			SenderID:    senderID,
			SenderName:  senderName,
			TimestampMs: tsMs,
			Blocks:      childBlocks,
		})
	}
	return protocol.ContentBlock{
		Type:         protocol.BlockForwardTree,
		ForwardNodes: out,
	}
}

func (n *Normalizer) fetchRecordBytes(ctx context.Context, data map[string]any) ([]byte, string, error) {
	if n.media != nil {
		file := segString(data, "file")
		if file != "" {
			res, err := n.media.GetRecord(ctx, file, "wav")
			if err == nil {
				if raw, ct, rerr := n.resolveFileResult(ctx, res); rerr == nil {
					return raw, ct, nil
				}
			}
		}
	}
	return n.fetchSegmentBytes(ctx, "record", data)
}

func (n *Normalizer) fetchSegmentBytes(ctx context.Context, segType string, data map[string]any) ([]byte, string, error) {
	if rawURL := segString(data, "url"); isHTTPURL(rawURL) {
		return n.downloadURL(ctx, rawURL)
	}
	file := segString(data, "file")
	if isHTTPURL(file) {
		return n.downloadURL(ctx, file)
	}
	if b64, ok := strings.CutPrefix(file, "base64://"); ok {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, "", fmt.Errorf("decode %s base64: %w", segType, err)
		}
		return raw, detectAudioOrMIME(raw, ""), nil
	}
	if localPath, ok := strings.CutPrefix(file, "file://"); ok {
		return readLocalMedia(localPath, n.maxMediaBytes)
	}
	if n.media != nil {
		fileID := firstNonEmpty(segString(data, "file_id"), file)
		if fileID != "" {
			res, err := n.media.GetFile(ctx, fileID)
			if err != nil {
				return nil, "", fmt.Errorf("get_file(%q): %w", fileID, err)
			}
			return n.resolveFileResult(ctx, res)
		}
	}
	if file != "" {
		if raw, ct, err := readLocalMedia(file, n.maxMediaBytes); err == nil {
			return raw, ct, nil
		}
	}
	return nil, "", fmt.Errorf("%s segment has no downloadable media source", segType)
}

func (n *Normalizer) resolveFileResult(ctx context.Context, res OneBotFileResult) ([]byte, string, error) {
	if res.Base64 != "" {
		raw, err := base64.StdEncoding.DecodeString(res.Base64)
		if err != nil {
			return nil, "", fmt.Errorf("decode file base64: %w", err)
		}
		return raw, detectAudioOrMIME(raw, ""), nil
	}
	if isHTTPURL(res.URL) {
		return n.downloadURL(ctx, res.URL)
	}
	if res.File != "" {
		localPath := strings.TrimPrefix(res.File, "file://")
		return readLocalMedia(localPath, n.maxMediaBytes)
	}
	return nil, "", errors.New("onebot file result has no readable payload")
}

func (n *Normalizer) downloadURL(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build media request: %w", err)
	}
	resp, err := n.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download media: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download media status %d", resp.StatusCode)
	}
	data, err := readLimited(resp.Body, n.maxMediaBytes)
	if err != nil {
		return nil, "", fmt.Errorf("read media body: %w", err)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return data, detectAudioOrMIME(data, ct), nil
}

// NewFFmpegAudioTranscoder converts QQ SILK/AMR voice bytes to 16kHz mono WAV via ffmpeg when available.
func NewFFmpegAudioTranscoder(ffmpegPath string) AudioTranscodeFunc {
	return func(ctx context.Context, data []byte, srcMIME string) ([]byte, string, error) {
		if !needsAudioTranscode(data, srcMIME) {
			return data, detectAudioOrMIME(data, srcMIME), nil
		}
		bin, err := exec.LookPath(ffmpegPath)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return data, detectAudioOrMIME(data, srcMIME), nil
			}
			return nil, "", fmt.Errorf("lookup ffmpeg: %w", err)
		}
		// Tencent SILK voice files often prepend a 0x02 byte before the standard #!SILK_V3 header.
		input := data
		if len(input) > 10 && input[0] == 0x02 && bytes.HasPrefix(input[1:], []byte("#!SILK_V3")) {
			input = input[1:]
		}
		cmd := exec.CommandContext(ctx, bin,
			"-hide_banner",
			"-loglevel", "error",
			"-i", "pipe:0",
			"-ar", "16000",
			"-ac", "1",
			"-f", "wav",
			"pipe:1",
		)
		cmd.Stdin = bytes.NewReader(input)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, "", fmt.Errorf("ffmpeg transcode: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		if stdout.Len() == 0 {
			return nil, "", errors.New("ffmpeg transcode produced empty output")
		}
		return stdout.Bytes(), "audio/wav", nil
	}
}

func needsAudioTranscode(data []byte, srcMIME string) bool {
	if isSILKOrAMR(data) {
		return true
	}
	lower := strings.ToLower(srcMIME)
	if strings.Contains(lower, "silk") || strings.Contains(lower, "amr") {
		return true
	}
	if isStandardAudioBytes(data) {
		return false
	}
	return strings.HasPrefix(lower, "audio/") &&
		lower != "audio/wav" &&
		lower != "audio/wave" &&
		lower != "audio/x-wav" &&
		lower != "audio/mpeg" &&
		lower != "audio/ogg" &&
		lower != "audio/flac" &&
		lower != "audio/mp4"
}

func isSILKOrAMR(data []byte) bool {
	return bytes.HasPrefix(data, []byte("#!SILK_V3")) ||
		(len(data) > 10 && data[0] == 0x02 && bytes.HasPrefix(data[1:], []byte("#!SILK_V3"))) ||
		bytes.HasPrefix(data, []byte("#!AMR"))
}

func isStandardAudioBytes(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	return (len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE"))) ||
		bytes.HasPrefix(data, []byte("OggS")) ||
		bytes.HasPrefix(data, []byte("fLaC")) ||
		bytes.HasPrefix(data, []byte("ID3")) ||
		(data[0] == 0xff && (data[1]&0xe0) == 0xe0)
}

func detectAudioOrMIME(data []byte, fallback string) string {
	switch {
	case bytes.HasPrefix(data, []byte("#!SILK_V3")) || (len(data) > 10 && data[0] == 0x02 && bytes.HasPrefix(data[1:], []byte("#!SILK_V3"))):
		return "audio/silk"
	case bytes.HasPrefix(data, []byte("#!AMR")):
		return "audio/amr"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")):
		return "audio/wav"
	case bytes.HasPrefix(data, []byte("OggS")):
		return "audio/ogg"
	case bytes.HasPrefix(data, []byte("fLaC")):
		return "audio/flac"
	}
	if fallback != "" && fallback != "application/octet-stream" {
		return fallback
	}
	return http.DetectContentType(data)
}

func unpackJSONCard(raw string) []protocol.ContentBlock {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return []protocol.ContentBlock{{Type: protocol.BlockText, Text: trimmed}}
	}

	prompt := strVal(doc["prompt"])
	topDesc := strVal(doc["desc"])
	meta, _ := doc["meta"].(map[string]any)

	var (
		sourceTag string
		title     string
		desc      string
		rawURL    string
	)

	if detail, ok := meta["detail_1"].(map[string]any); ok {
		sourceTag = firstNonEmpty(strVal(detail["title"]), topDesc)
		title = firstNonEmpty(strVal(detail["desc"]), prompt)
		if title == sourceTag {
			title = strVal(detail["desc"])
		}
		rawURL = pickCardURL(strVal(detail["qqdocurl"]), strVal(detail["url"]))
	} else if news, ok := meta["news"].(map[string]any); ok {
		sourceTag = firstNonEmpty(strVal(news["tag"]), topDesc)
		title = firstNonEmpty(strVal(news["title"]), prompt)
		desc = strVal(news["desc"])
		rawURL = pickCardURL(strVal(news["jumpUrl"]), strVal(news["url"]))
	} else if music, ok := meta["music"].(map[string]any); ok {
		sourceTag = firstNonEmpty(strVal(music["tag"]), topDesc)
		title = firstNonEmpty(strVal(music["title"]), prompt)
		desc = strVal(music["desc"])
		rawURL = pickCardURL(strVal(music["jumpUrl"]), strVal(music["musicUrl"]), strVal(music["url"]))
	} else if len(meta) > 0 {
		for _, k := range slices.Sorted(maps.Keys(meta)) {
			sub, ok := meta[k].(map[string]any)
			if !ok {
				continue
			}
			sourceTag = firstNonEmpty(sourceTag, strVal(sub["tag"]), topDesc)
			title = firstNonEmpty(title, strVal(sub["title"]), strVal(sub["desc"]))
			desc = firstNonEmpty(desc, strVal(sub["desc"]))
			rawURL = firstNonEmpty(rawURL, pickCardURL(strVal(sub["qqdocurl"]), strVal(sub["jumpUrl"]), strVal(sub["url"])))
		}
	}

	if title == "" {
		title = firstNonEmpty(prompt, topDesc)
	}
	if desc == title {
		desc = ""
	}

	bvid := extractBVID(rawURL, title, desc, trimmed)
	cleanURL := normalizeCardURL(rawURL, bvid)

	metaText := formatCardMetadata(sourceTag, title, desc, bvid, cleanURL)
	if cleanURL == "" {
		if metaText == "" {
			return nil
		}
		return []protocol.ContentBlock{{Type: protocol.BlockText, Text: metaText}}
	}

	return []protocol.ContentBlock{{
		Type:        protocol.BlockLink,
		URL:         cleanURL,
		Title:       title,
		Description: desc,
		Text:        metaText,
	}}
}

func extractBVID(candidates ...string) string {
	for _, s := range candidates {
		if m := bvidPattern.FindString(s); m != "" {
			return m
		}
	}
	return ""
}

func pickCardURL(candidates ...string) string {
	fallback := ""
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// Prefer direct target URLs (e.g. bilibili.com / b23.tv) over QQ miniapp redirect shims.
		if isHTTPURL(c) && !strings.Contains(c, "m.q.qq.com/") {
			return c
		}
		if fallback == "" {
			fallback = c
		}
	}
	return fallback
}

func normalizeCardURL(rawURL, bvid string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "//") {
		rawURL = "https:" + rawURL
	}

	if bvid != "" {
		canonical := "https://www.bilibili.com/video/" + bvid
		if parsed, err := url.Parse(rawURL); err == nil {
			if page := parsed.Query().Get("p"); page != "" && page != "1" {
				canonical += "?p=" + url.QueryEscape(page)
			}
		}
		return canonical
	}

	if rawURL == "" {
		return ""
	}
	if !isHTTPURL(rawURL) {
		if strings.Contains(rawURL, "://") {
			return ""
		}
		if strings.Contains(rawURL, ".") {
			rawURL = "https://" + rawURL
		} else {
			return ""
		}
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "b23.tv" || strings.HasSuffix(host, ".bilibili.com") || host == "bilibili.com" {
		q := parsed.Query()
		for k := range bilibiliTrackingParams {
			q.Del(k)
		}
		parsed.RawQuery = q.Encode()
	}
	return parsed.String()
}

func formatCardMetadata(sourceTag, title, desc, bvid, cleanURL string) string {
	var parts []string
	if sourceTag != "" {
		parts = append(parts, "["+sourceTag+"]")
	}
	if title != "" {
		parts = append(parts, title)
	}
	if desc != "" {
		parts = append(parts, desc)
	}
	if bvid != "" && !strings.Contains(title, bvid) && !strings.Contains(desc, bvid) {
		parts = append(parts, bvid)
	}
	if len(parts) == 0 && cleanURL != "" {
		return cleanURL
	}
	return strings.Join(parts, " ")
}

func decodeSegments(raw json.RawMessage) ([]OneBotSegment, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		if text == "" {
			return nil, nil
		}
		return []OneBotSegment{{Type: "text", Data: map[string]any{"text": text}}}, nil
	}
	var segs []OneBotSegment
	if err := json.Unmarshal(trimmed, &segs); err != nil {
		return nil, err
	}
	return segs, nil
}

func mediaFallbackBlock(tag, detail string) protocol.ContentBlock {
	text := tag
	if detail != "" {
		text += " " + detail
	}
	return protocol.ContentBlock{
		Type: protocol.BlockText,
		Text: text,
	}
}

func readLocalMedia(path string, maxBytes int64) ([]byte, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open local media: %w", err)
	}
	defer f.Close()
	data, err := readLimited(f, maxBytes)
	if err != nil {
		return nil, "", fmt.Errorf("read local media: %w", err)
	}
	return data, detectAudioOrMIME(data, ""), nil
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("media exceeds limit of %d bytes", maxBytes)
	}
	if len(data) == 0 {
		return nil, errors.New("empty media payload")
	}
	return data, nil
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func toMillis(ts int64) int64 {
	if ts <= 0 {
		return 0
	}
	// OneBot v11 timestamps are Unix seconds; preserve millisecond values if already scaled.
	if ts < 1_000_000_000_000 {
		return ts * 1000
	}
	return ts
}

func segString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	return strVal(data[key])
}

func strVal(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstRaw(vals ...json.RawMessage) json.RawMessage {
	for _, v := range vals {
		if len(bytes.TrimSpace(v)) > 0 {
			return v
		}
	}
	return nil
}
