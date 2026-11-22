package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"omni-bot/internal/protocol"
)

const (
	defaultActionTimeout = 15 * time.Second
	defaultWriteTimeout  = 10 * time.Second
	defaultSendBuffer    = 64
	maxOneBotFrameBytes  = 16 << 20
)

const (
	actionSendGroupMsg       = "send_group_msg"
	actionSetMsgEmojiLike    = "set_msg_emoji_like"
	actionGroupPoke          = "group_poke"
	actionGetMsg             = "get_msg"
	actionGetForwardMsg      = "get_forward_msg"
	actionGetRecord          = "get_record"
	actionGetFile            = "get_file"
	actionGetGroupMemberInfo = "get_group_member_info"
)

var ErrOneBotClosed = errors.New("onebot websocket closed")

type OneBotActionError struct {
	Action  string
	Status  string
	Retcode int
	Message string
}

func (e *OneBotActionError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("onebot action %q failed (status=%s retcode=%d): %s", e.Action, e.Status, e.Retcode, e.Message)
	}
	return fmt.Sprintf("onebot action %q failed (status=%s retcode=%d)", e.Action, e.Status, e.Retcode)
}

type OneBotMessageDetail struct {
	Time        int64           `json:"time"`
	MessageType string          `json:"message_type"`
	MessageID   FlexID          `json:"message_id"`
	RealID      FlexID          `json:"real_id,omitempty"`
	GroupID     int64           `json:"group_id,omitempty"`
	UserID      int64           `json:"user_id,omitempty"`
	Sender      OneBotSender    `json:"sender"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message,omitempty"`
}

type oneBotActionRequest struct {
	Action string `json:"action"`
	Params any    `json:"params,omitempty"`
	Echo   string `json:"echo"`
}

type oneBotActionResponse struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message,omitempty"`
	Wording string          `json:"wording,omitempty"`
	Echo    string          `json:"echo,omitempty"`
}

type OneBotClientOptions struct {
	ActionTimeout time.Duration
	WriteTimeout  time.Duration
	SendBuffer    int
	Logger        *slog.Logger
	OnEvent       func(context.Context, OneBotEvent)
}

type OneBotClient struct {
	ws            *websocket.Conn
	actionTimeout time.Duration
	writeTimeout  time.Duration
	logger        *slog.Logger
	onEvent       func(context.Context, OneBotEvent)

	seq       atomic.Uint64
	send      chan []byte
	done      chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	// mu protects pending only; never hold mu across WS I/O.
	mu      sync.Mutex
	pending map[string]chan oneBotActionResponse
}

func NewOneBotClient(ctx context.Context, ws *websocket.Conn, opts OneBotClientOptions) *OneBotClient {
	actionTimeout := opts.ActionTimeout
	if actionTimeout <= 0 {
		actionTimeout = defaultActionTimeout
	}
	writeTimeout := opts.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultWriteTimeout
	}
	sendBuf := opts.SendBuffer
	if sendBuf <= 0 {
		sendBuf = defaultSendBuffer
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	ws.SetReadLimit(maxOneBotFrameBytes)
	runCtx, cancel := context.WithCancel(ctx)
	c := &OneBotClient{
		ws:            ws,
		actionTimeout: actionTimeout,
		writeTimeout:  writeTimeout,
		logger:        logger.With("component", "napcat.onebot"),
		onEvent:       opts.OnEvent,
		send:          make(chan []byte, sendBuf),
		done:          make(chan struct{}),
		cancel:        cancel,
		pending:       make(map[string]chan oneBotActionResponse),
	}

	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		c.writeLoop()
	}()
	go func() {
		defer c.wg.Done()
		c.readLoop(runCtx)
	}()
	return c
}

func (c *OneBotClient) Done() <-chan struct{} {
	return c.done
}

func (c *OneBotClient) Close() {
	c.closeOnce.Do(func() {
		c.cancel()
		close(c.done)
		_ = c.ws.CloseNow()
	})
}

func (c *OneBotClient) Wait() {
	c.wg.Wait()
}

func (c *OneBotClient) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.send:
			wctx, cancel := context.WithTimeout(context.Background(), c.writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				c.logger.Debug("onebot ws write failed", "err", err)
				c.Close()
				return
			}
		}
	}
}

func (c *OneBotClient) readLoop(ctx context.Context) {
	defer c.Close()

	for {
		msgType, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if msgType != websocket.MessageText {
			continue
		}

		var probe struct {
			Echo     string `json:"echo"`
			PostType string `json:"post_type"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			c.logger.Warn("decode onebot frame failed", "err", err)
			continue
		}

		if probe.Echo != "" && probe.PostType == "" {
			var resp oneBotActionResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				c.logger.Warn("decode onebot action response failed", "echo", probe.Echo, "err", err)
				continue
			}
			c.deliverResponse(resp)
			continue
		}

		if probe.PostType != "" && c.onEvent != nil {
			var ev OneBotEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				c.logger.Warn("decode onebot event failed", "post_type", probe.PostType, "err", err)
				continue
			}
			// Run event callbacks off the read loop so handlers can call get_forward_msg/get_record without deadlocking.
			c.wg.Add(1)
			go func(event OneBotEvent) {
				defer c.wg.Done()
				c.onEvent(ctx, event)
			}(ev)
		}
	}
}

func (c *OneBotClient) registerPending(echo string) chan oneBotActionResponse {
	ch := make(chan oneBotActionResponse, 1)
	c.mu.Lock()
	c.pending[echo] = ch
	c.mu.Unlock()
	return ch
}

func (c *OneBotClient) unregisterPending(echo string) {
	c.mu.Lock()
	delete(c.pending, echo)
	c.mu.Unlock()
}

func (c *OneBotClient) deliverResponse(resp oneBotActionResponse) {
	c.mu.Lock()
	ch := c.pending[resp.Echo]
	c.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

func (c *OneBotClient) Call(ctx context.Context, action string, params any, dst any) error {
	echo := fmt.Sprintf("ob-%d", c.seq.Add(1))
	frame, err := json.Marshal(oneBotActionRequest{
		Action: action,
		Params: params,
		Echo:   echo,
	})
	if err != nil {
		return fmt.Errorf("marshal onebot action %q: %w", action, err)
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && c.actionTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, c.actionTimeout)
		defer cancel()
	}

	respCh := c.registerPending(echo)
	defer c.unregisterPending(echo)

	select {
	case <-c.done:
		return fmt.Errorf("onebot action %q: %w", action, ErrOneBotClosed)
	case <-callCtx.Done():
		return fmt.Errorf("onebot action %q: %w", action, callCtx.Err())
	case c.send <- frame:
	}

	select {
	case resp := <-respCh:
		if resp.Status != "ok" || resp.Retcode != 0 {
			return &OneBotActionError{
				Action:  action,
				Status:  resp.Status,
				Retcode: resp.Retcode,
				Message: firstNonEmpty(resp.Wording, resp.Message),
			}
		}
		if dst != nil && len(resp.Data) > 0 && string(resp.Data) != "null" {
			if err := json.Unmarshal(resp.Data, dst); err != nil {
				return fmt.Errorf("decode onebot action %q response: %w", action, err)
			}
		}
		return nil
	case <-callCtx.Done():
		return fmt.Errorf("onebot action %q: %w", action, callCtx.Err())
	case <-c.done:
		return fmt.Errorf("onebot action %q: %w", action, ErrOneBotClosed)
	}
}

func (c *OneBotClient) SendGroupMsg(ctx context.Context, groupID int64, segments []OneBotSegment) (string, error) {
	if groupID <= 0 {
		return "", fmt.Errorf("send_group_msg: invalid group_id %d", groupID)
	}
	if len(segments) == 0 {
		return "", errors.New("send_group_msg: empty segments")
	}
	var res struct {
		MessageID FlexID `json:"message_id"`
	}
	err := c.Call(ctx, actionSendGroupMsg, map[string]any{
		"group_id": groupID,
		"message":  segments,
	}, &res)
	if err != nil {
		return "", err
	}
	return string(res.MessageID), nil
}

func (c *OneBotClient) SetMsgEmojiLike(ctx context.Context, msgID, emojiID string) error {
	if strings.TrimSpace(msgID) == "" {
		return errors.New("set_msg_emoji_like: message_id is required")
	}
	if strings.TrimSpace(emojiID) == "" {
		return errors.New("set_msg_emoji_like: emoji_id is required")
	}
	var idParam any = msgID
	if parsed, err := strconv.ParseInt(msgID, 10, 64); err == nil {
		idParam = parsed
	}
	return c.Call(ctx, actionSetMsgEmojiLike, map[string]any{
		"message_id": idParam,
		"emoji_id":   emojiID,
		"set":        true,
	}, nil)
}

func (c *OneBotClient) GroupPoke(ctx context.Context, groupID, userID int64) error {
	if groupID <= 0 || userID <= 0 {
		return fmt.Errorf("group_poke: invalid group_id=%d user_id=%d", groupID, userID)
	}
	return c.Call(ctx, actionGroupPoke, map[string]any{
		"group_id": groupID,
		"user_id":  userID,
	}, nil)
}

func (c *OneBotClient) GetMsg(ctx context.Context, msgID string) (OneBotMessageDetail, error) {
	if strings.TrimSpace(msgID) == "" {
		return OneBotMessageDetail{}, errors.New("get_msg: message_id is required")
	}
	var idParam any = msgID
	if parsed, err := strconv.ParseInt(msgID, 10, 64); err == nil {
		idParam = parsed
	}
	var detail OneBotMessageDetail
	if err := c.Call(ctx, actionGetMsg, map[string]any{"message_id": idParam}, &detail); err != nil {
		return OneBotMessageDetail{}, err
	}
	return detail, nil
}

func (c *OneBotClient) GetForwardMsg(ctx context.Context, forwardID string) ([]OneBotForwardNode, error) {
	if strings.TrimSpace(forwardID) == "" {
		return nil, errors.New("get_forward_msg: id is required")
	}
	var res struct {
		Messages []OneBotForwardNode `json:"messages"`
		Message  []OneBotForwardNode `json:"message"`
	}
	if err := c.Call(ctx, actionGetForwardMsg, map[string]any{"id": forwardID}, &res); err != nil {
		return nil, err
	}
	if len(res.Messages) > 0 {
		return res.Messages, nil
	}
	return res.Message, nil
}

func (c *OneBotClient) GetRecord(ctx context.Context, file, outFormat string) (OneBotFileResult, error) {
	if strings.TrimSpace(file) == "" {
		return OneBotFileResult{}, errors.New("get_record: file is required")
	}
	if outFormat == "" {
		outFormat = "wav"
	}
	var res OneBotFileResult
	err := c.Call(ctx, actionGetRecord, map[string]any{
		"file":       file,
		"out_format": outFormat,
	}, &res)
	if err != nil {
		return OneBotFileResult{}, err
	}
	return res, nil
}

func (c *OneBotClient) GetFile(ctx context.Context, fileID string) (OneBotFileResult, error) {
	if strings.TrimSpace(fileID) == "" {
		return OneBotFileResult{}, errors.New("get_file: file_id is required")
	}
	var res OneBotFileResult
	if err := c.Call(ctx, actionGetFile, map[string]any{"file_id": fileID}, &res); err != nil {
		return OneBotFileResult{}, err
	}
	return res, nil
}

func (c *OneBotClient) GetGroupMemberInfo(ctx context.Context, groupID, userID int64, noCache bool) (OneBotGroupMember, error) {
	if groupID <= 0 || userID <= 0 {
		return OneBotGroupMember{}, fmt.Errorf("get_group_member_info: invalid group_id=%d user_id=%d", groupID, userID)
	}
	var member OneBotGroupMember
	err := c.Call(ctx, actionGetGroupMemberInfo, map[string]any{
		"group_id": groupID,
		"user_id":  userID,
		"no_cache": noCache,
	}, &member)
	if err != nil {
		return OneBotGroupMember{}, err
	}
	return member, nil
}

type BlobDownloader interface {
	Download(ctx context.Context, blobID string) ([]byte, string, error)
}

type ActionExecutor struct {
	client *OneBotClient
	blobs  BlobDownloader
	logger *slog.Logger
}

func NewActionExecutor(client *OneBotClient, blobs BlobDownloader, logger *slog.Logger) *ActionExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &ActionExecutor{
		client: client,
		blobs:  blobs,
		logger: logger.With("component", "napcat.actions"),
	}
}

func (e *ActionExecutor) ExecuteOutbound(
	ctx context.Context,
	roomID string,
	actions []protocol.OutboundAction,
) (protocol.ChannelOutboundResult, error) {
	groupID, err := parseGroupID(roomID)
	if err != nil {
		return protocol.ChannelOutboundResult{}, err
	}

	var result protocol.ChannelOutboundResult
	for _, act := range actions {
		switch act.Type {
		case protocol.ActionSendText:
			segs := buildReplyPrefix(act.ReplyToMsgID)
			segs = append(segs, OneBotSegment{
				Type: "text",
				Data: map[string]any{"text": act.Text},
			})
			msgID, err := e.client.SendGroupMsg(ctx, groupID, segs)
			if err != nil {
				return result, fmt.Errorf("execute send_text: %w", err)
			}
			result.Delivered++
			if msgID != "" {
				result.MsgIDs = append(result.MsgIDs, msgID)
			}

		case protocol.ActionSendMeme:
			fileSpec, err := e.resolveOutboundMedia(ctx, act.Media, act.MemeRef)
			if err != nil {
				return result, fmt.Errorf("resolve send_meme media: %w", err)
			}
			segs := buildReplyPrefix(act.ReplyToMsgID)
			segs = append(segs, OneBotSegment{
				Type: "image",
				Data: map[string]any{
					"file":    fileSpec,
					"subType": 1,
				},
			})
			msgID, err := e.client.SendGroupMsg(ctx, groupID, segs)
			if err != nil {
				return result, fmt.Errorf("execute send_meme: %w", err)
			}
			result.Delivered++
			if msgID != "" {
				result.MsgIDs = append(result.MsgIDs, msgID)
			}

		case protocol.ActionSendVoice:
			if act.Media != nil && act.Media.BlobID != "" {
				fileSpec, err := e.resolveOutboundMedia(ctx, act.Media, "")
				if err != nil {
					return result, fmt.Errorf("resolve send_voice media: %w", err)
				}
				segs := []OneBotSegment{{
					Type: "record",
					Data: map[string]any{"file": fileSpec},
				}}
				msgID, err := e.client.SendGroupMsg(ctx, groupID, segs)
				if err != nil {
					return result, fmt.Errorf("execute send_voice: %w", err)
				}
				result.Delivered++
				if msgID != "" {
					result.MsgIDs = append(result.MsgIDs, msgID)
				}
				continue
			}
			if strings.TrimSpace(act.Text) == "" {
				return result, errors.New("execute send_voice: missing voice media blob and fallback text")
			}
			e.logger.Warn("send_voice missing audio blob; degrading to text bubble")
			segs := buildReplyPrefix(act.ReplyToMsgID)
			segs = append(segs, OneBotSegment{
				Type: "text",
				Data: map[string]any{"text": act.Text},
			})
			msgID, err := e.client.SendGroupMsg(ctx, groupID, segs)
			if err != nil {
				return result, fmt.Errorf("execute send_voice text fallback: %w", err)
			}
			result.Delivered++
			if msgID != "" {
				result.MsgIDs = append(result.MsgIDs, msgID)
			}

		case protocol.ActionReact:
			if err := e.client.SetMsgEmojiLike(ctx, act.MsgID, act.Emoji); err != nil {
				return result, fmt.Errorf("execute react: %w", err)
			}
			result.Delivered++

		case protocol.ActionPoke:
			userID, err := strconv.ParseInt(strings.TrimSpace(act.UserID), 10, 64)
			if err != nil || userID <= 0 {
				return result, fmt.Errorf("execute poke: invalid user_id %q", act.UserID)
			}
			if err := e.client.GroupPoke(ctx, groupID, userID); err != nil {
				return result, fmt.Errorf("execute poke: %w", err)
			}
			result.Delivered++

		default:
			return result, fmt.Errorf("unsupported outbound action type %q", act.Type)
		}
	}
	return result, nil
}

func (e *ActionExecutor) resolveOutboundMedia(ctx context.Context, media *protocol.MediaRef, ref string) (string, error) {
	blobID := ""
	if media != nil && media.BlobID != "" {
		blobID = media.BlobID
	} else if ref != "" {
		if isHTTPURL(ref) {
			return ref, nil
		}
		blobID = ref
	}
	if blobID == "" {
		return "", errors.New("missing blob_id or media reference")
	}
	if e.blobs == nil {
		return "", errors.New("blob downloader not configured")
	}
	data, _, err := e.blobs.Download(ctx, blobID)
	if err != nil {
		return "", fmt.Errorf("download blob %q: %w", blobID, err)
	}
	// OneBot v11 accepts base64:// on the NapCat WS leg so NapCat does not need direct HTTP access to Kernel.
	return "base64://" + base64.StdEncoding.EncodeToString(data), nil
}

func buildReplyPrefix(replyToMsgID string) []OneBotSegment {
	if strings.TrimSpace(replyToMsgID) == "" {
		return nil
	}
	return []OneBotSegment{{
		Type: "reply",
		Data: map[string]any{"id": replyToMsgID},
	}}
}

func parseGroupID(roomID string) (int64, error) {
	trimmed := strings.TrimSpace(roomID)
	trimmed = strings.TrimPrefix(trimmed, "group:")
	id, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid group room_id %q", roomID)
	}
	return id, nil
}
