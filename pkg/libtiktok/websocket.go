package libtiktok

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/coder/websocket"
	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
	"github.com/rs/zerolog"
)

// WSMessage is the unit of communication between libtiktok and the connector
// layer. It carries everything the bridge needs to dispatch a single inbound
// chat event without any further API calls (except for video-share URL
// resolution which happens inside parseMessageContent).
type WSMessage struct {
	Conversation Conversation
	Message      Message
}

// ────────────────────────────────────────────────────────────────────────────
// WebSocket URL derivation
// ────────────────────────────────────────────────────────────────────────────

// deriveWSURL assembles the wss://im-ws-sg.tiktok.com/ws/v2 URL that the
// TikTok web client connects to for real-time IM events.
//
// The access_key is derived as:
//
//	MD5("9e1bd35ec9db7b8d846de66ed140b1ad9" + wid + "f8a69f1719916z")
//
// where wid comes from the __UNIVERSAL_DATA_FOR_REHYDRATION__ script tag
// already parsed by getMessagesUniversalData / getAppContext.
func (c *Client) deriveWSURL() (string, error) {
	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return "", fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return "", fmt.Errorf("get app context: %w", err)
	}

	wid, ok := appContext["wid"].(string)
	if !ok || wid == "" {
		return "", fmt.Errorf("wid not found in rehydration app context")
	}

	// access_key = lowercase-hex(MD5("9e1bd35ec9db7b8d846de66ed140b1ad9{wid}f8a69f1719916z"))
	raw := "9e1bd35ec9db7b8d846de66ed140b1ad9" + wid + "f8a69f1719916z"
	sum := md5.Sum([]byte(raw))
	accessKey := hex.EncodeToString(sum[:])

	cookie := c.rIA.Header.Get("Cookie")
	rawTTWid := extractCookie(cookie, "ttwid")
	if rawTTWid == "" {
		return "", fmt.Errorf("ttwid not found in cookie string")
	}
	// The cookie string stores ttwid pre-encoded (e.g. "1%7C...").
	// url.Values.Encode would encode it a second time (%7C → %257C), so we
	// decode it first and let Encode apply a single, correct encoding pass.
	ttwid, err := url.QueryUnescape(rawTTWid)
	if err != nil {
		// Not valid percent-encoding — use the raw value as-is.
		ttwid = rawTTWid
	}

	params := url.Values{
		"device_platform": {"web"},
		"version_code":    {"fws_1.0.0"},
		"access_key":      {accessKey},
		"fpid":            {"9"},
		"aid":             {"1459"},
		"ttwid":           {ttwid},
		"xsack":           {"1"},
		"xaack":           {"1"},
		"xsqos":           {"0"},
	}
	return "wss://im-ws-sg.tiktok.com/ws/v2?" + params.Encode(), nil
}

// ────────────────────────────────────────────────────────────────────────────
// Protobuf frame parser
// ────────────────────────────────────────────────────────────────────────────

// parseWSFrame decodes one binary WebSocket frame and returns a *WSMessage
// when the frame carries a chat event (inner type 500).  Returns (nil, nil)
// for heartbeat / control frames that carry no chat payload.
//
// Protobuf path (confirmed from live traffic captures):
//
//	top
//	  └─ [8]  content envelope
//	       [1]   inner type  (must be 500 for chat)
//	       [6]   command container
//	               [500]  chat container
//	                        [2]  conversation ID  (bytes → string)
//	                        [5]  message detail
//	                               [3]  numeric message ID     (varint uint64)
//	                               [4]  send timestamp         (varint, microseconds)
//	                               [7]  sender user ID         (varint uint64)
//	                               [8]  JSON content payload   (bytes)
//	                               [9]  repeated KV tag pairs  (bytes, repeated)
func (c *Client) parseWSFrame(ctx context.Context, data []byte) (*WSMessage, error) {
	var frame tiktokpb.WebsocketFrame
	if err := unmarshalProto(data, &frame); err != nil {
		return nil, fmt.Errorf("decode top frame: %w", err)
	}

	env := frame.GetEnvelope()
	if env == nil {
		return nil, nil // heartbeat or control frame — no content
	}

	innerType := env.GetInnerType()
	if innerType != 500 {
		if innerType != 0 {
			zerolog.Ctx(ctx).Debug().
				Uint64("inner_type", innerType).
				Int("frame_bytes", len(data)).
				Msg("Unhandled WS inner type — skipping")
		}
		return nil, nil
	}

	chat := env.GetCommands().GetChat()
	if chat == nil {
		return nil, nil
	}

	convID := chat.GetConversationId()
	if convID == "" {
		return nil, fmt.Errorf("conversation ID missing from chat container")
	}

	detail := chat.GetDetail()
	if detail == nil {
		return nil, fmt.Errorf("message detail (chat.5) missing")
	}

	numericMsgID := detail.GetServerMessageId()
	tsUs := int64(detail.GetTimestampUs())
	senderID := strconv.FormatUint(detail.GetSenderUserId(), 10)
	msgID := extractClientMsgIDFromTags(detail.GetTags())
	msgType, text, mediaURL, _ := parseMessageContent(ctx, c, detail.GetContentJson())

	msg := Message{
		ServerID:        numericMsgID,
		ClientMessageID: msgID,
		ConvID:          convID,
		SenderID:        senderID,
		Type:            msgType,
		Text:            text,
		MediaURL:        mediaURL,
		TimestampMs:     tsUs / 1000, // µs → ms
		// Reactions arrive through a different WS event type.
	}
	conv := Conversation{
		ID: convID,
		// Participants carries only the sender here; the connector layer
		// supplements the other participant from its otherUsers cache.
		Participants: []string{senderID},
	}
	return &WSMessage{Conversation: conv, Message: msg}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Public API
// ────────────────────────────────────────────────────────────────────────────

// ConnectWebSocket dials the TikTok IM WebSocket, starts a background read
// pump goroutine, and returns a channel that emits one WSMessage per inbound
// chat event.
//
// The caller owns the channel lifetime: it is closed when ctx is cancelled or
// the server closes the connection, which is the signal the connector uses to
// trigger a reconnect.  Non-chat frames (heartbeats, ACKs, etc.) are silently
// discarded; parse errors are logged but do not terminate the pump.
//
// Usage in the connector:
//
//	ch, err := c.apiClient.ConnectWebSocket(ctx)
//	if err != nil { /* handle dial error */ }
//	for wsMsg := range ch {
//	    tc.dispatchMessage(&wsMsg.Conversation, wsMsg.Message)
//	}
//	// channel closed → reconnect
func (c *Client) ConnectWebSocket(ctx context.Context) (<-chan WSMessage, error) {
	log := zerolog.Ctx(ctx)

	wsURL, err := c.deriveWSURL()
	if err != nil {
		return nil, fmt.Errorf("derive WS URL: %w", err)
	}
	log.Debug().Str("url", wsURL).Msg("Dialling TikTok IM WebSocket")

	cookie := c.rIA.Header.Get("Cookie")
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Cookie":     {cookie},
			"User-Agent": {DEFAULT_USER_AGENT},
			"Origin":     {"https://www.tiktok.com"},
			"Referer":    {"https://www.tiktok.com/"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("WS dial: %w", err)
	}

	ch := make(chan WSMessage, 32)
	go func() {
		defer close(ch)
		defer conn.CloseNow()

		log.Info().Msg("TikTok IM WebSocket connected")

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				if ctx.Err() == nil {
					// Unexpected disconnect — log and let close(ch) signal the caller.
					log.Err(err).Msg("TikTok IM WebSocket read error")
				}
				return
			}

			wsMsg, err := c.parseWSFrame(ctx, data)
			if err != nil {
				log.Warn().Err(err).Int("frame_bytes", len(data)).Msg("Failed to parse WS frame")
				continue
			}
			if wsMsg == nil {
				// Non-chat frame (heartbeat, ACK, etc.) — discard silently.
				continue
			}

			select {
			case ch <- *wsMsg:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}
