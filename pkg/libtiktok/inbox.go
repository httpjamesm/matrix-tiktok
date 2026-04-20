package libtiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
	"github.com/rs/zerolog"
)

type Conversation struct {
	ID           string   // 0:1:X:Y
	SourceID     uint64   // 5: from get_by_user_init
	Participants []string // user IDs
	// Name is the explicit conversation title when TikTok provides one.
	// Group chats currently expose this under detail.core.title; DMs leave it empty.
	Name string
	// ConversationType is the wire conversation_type value: 1 for DMs, 2 for group chats.
	ConversationType uint64
}

type Message struct {
	ServerID        uint64
	ConvID          string
	ClientMessageID string
	SenderID        string
	Type            string // "text", "image", "video", "sticker"
	MessageSubtype  string
	Text            string
	MediaURL        string
	ThumbnailURL    string
	// MediaDecryptKey is currently used by private_image messages, whose CDN
	// blobs are encrypted with AES-256-GCM.
	MediaDecryptKey string
	MimeType        string
	MediaWidth      int
	MediaHeight     int
	MediaDurationMs int
	TimestampMs     int64
	Reactions       []Reaction
	// ReplyToServerID is the parent message's server_message_id when this DM is a reply (aweType 703).
	ReplyToServerID uint64
	// ReplyQuotedText is a short plain-text preview of the parent message from message_reply (field 2 JSON), when present.
	ReplyQuotedText string
	// SendChainID is TikTok inner wire field 5; copy into send body field 3 when replying from Matrix.
	SendChainID uint64
	// SenderSecUID is the sender's sec_uid (wire field 14) for building outbound reply reference JSON.
	SenderSecUID string
	// CursorTsUs is field 25 on the wire row; used as parent_cursor_ts_us on outbound replies.
	CursorTsUs uint64
	// RawContentJSON is the original field-8 JSON body bytes (for round-tripping refmsg content on send).
	RawContentJSON []byte
}

// Reaction represents a single emoji reaction on a message and the users who sent it.
// The Emoji field holds the raw emoji string (or text alias) after the "e:" prefix is stripped.
// For example, field-1 value "e:❤️" becomes Emoji "❤️", and "e:love" becomes Emoji "love".
type Reaction struct {
	Emoji   string   // emoji character(s) or text name, e.g. "❤️" or "love"
	UserIDs []string // IDs of users who reacted with this emoji
}

const (
	inboxURL     = "/v2/message/get_by_user_init"
	getByConvURL = "/v1/message/get_by_conversation"
	imAID        = "1988"
)

// isUnicodeEmoji reports whether s contains at least one non-ASCII rune,
// indicating it is a real Unicode emoji glyph rather than a plain-text alias.
func isUnicodeEmoji(s string) bool {
	for _, r := range s {
		if r > 0x7F {
			return true
		}
	}
	return false
}

// deduplicateReactions collapses reaction entries that share an identical set
// of reacting users, keeping the entry whose Emoji contains non-ASCII runes
// (i.e. an actual unicode emoji glyph) over a plain-text alias.
//
// TikTok encodes each reaction twice on the wire – once as the emoji
// character(s) (e.g. "❤️") and once as a text alias (e.g. "love").
// Because both entries carry exactly the same UserIDs, grouping by that
// fingerprint reliably collapses the duplicates without needing an
// alias-to-emoji lookup table.
func deduplicateReactions(in []Reaction) []Reaction {
	if len(in) <= 1 {
		return in
	}

	type slot struct {
		idx     int
		isEmoji bool
	}
	seen := make(map[string]slot, len(in))
	out := make([]Reaction, 0, len(in))

	for _, r := range in {
		key := strings.Join(r.UserIDs, "\x00")
		uni := isUnicodeEmoji(r.Emoji)
		if s, ok := seen[key]; ok {
			if uni && !s.isEmoji {
				out[s.idx] = r
				seen[key] = slot{idx: s.idx, isEmoji: true}
			}
		} else {
			seen[key] = slot{idx: len(out), isEmoji: uni}
			out = append(out, r)
		}
	}
	return out
}

// parseMessageContent decodes the JSON content blob (field 8 in both the REST
// get-by-conversation response and the WebSocket push frame) and returns the
// (msgType, text, mediaURL, mimeType) fields for a Message.
//
// Known aweType values:
//
//	0, 700 → "text"  (REST API uses 0; WebSocket push uses 700)
//	703    → "text"  (reply; same text field; parent id is protobuf message_reply, field 1)
//	800    → "video" (shared TikTok post)
func parseMessageContent(ctx context.Context, c *Client, contentBytes []byte) (msgType, text, mediaURL, mimeType string) {
	if len(contentBytes) == 0 {
		return
	}
	content, err := parseContentJSONObject(contentBytes)
	if err != nil {
		return
	}
	if stickerURL, stickerText, ok := parseStickerFromContentJSON(content, contentBytes); ok {
		msgType = "sticker"
		text = stickerText
		mediaURL = stickerURL
		mimeType = guessStickerMIMEFromURL(stickerURL)
		if text == "" {
			text = "[sticker]"
		}
		return
	}
	// Media-only rows often ship placeholder JSON like {"hack":"1"} with no aweType.
	// A missing key must not be treated as aweType 0 — that produced Type "text" with
	// an empty body and bridged a stray empty Matrix m.text next to image/video.
	rawAwe, hasAwe := content["aweType"]
	if !hasAwe || rawAwe == nil {
		return "", "", "", ""
	}
	aweTypeF, ok := rawAwe.(float64)
	if !ok {
		return "", "", "", ""
	}
	switch int(aweTypeF) {
	case 0, 700, 703:
		msgType = "text"
		text, _ = content["text"].(string)
	case 800:
		msgType = "video"
		if itemID, _ := content["itemId"].(string); itemID != "" {
			if uid, _ := content["uid"].(string); uid != "" {
				if user, err := c.GetUser(ctx, uid); err == nil && user.UniqueID != "" {
					mediaURL = "https://www.tiktok.com/@" + user.UniqueID + "/video/" + itemID
				}
			}
		}
		text, _ = content["content_title"].(string)
	default:
		msgType = fmt.Sprintf("type_%d", int(aweTypeF))
		text, _ = content["text"].(string)
		zerolog.Ctx(ctx).Warn().
			Int("awe_type", int(aweTypeF)).
			RawJSON("content", contentBytes).
			Msg("Received TikTok message with unrecognised aweType — please open an issue")
	}
	return
}

// parseReplyQuotedTextFromWire extracts the inner chat "text" from TikTok's message_reply
// quoted_context_json blob (field 2): outer JSON with refmsg_content / content holding a nested JSON body.
func parseReplyQuotedTextFromWire(quotedContextJSON []byte) string {
	if len(quotedContextJSON) == 0 {
		return ""
	}
	var outer struct {
		Content       string `json:"content"`
		RefmsgContent string `json:"refmsg_content"`
	}
	if err := json.Unmarshal(quotedContextJSON, &outer); err != nil {
		return ""
	}
	raw := outer.RefmsgContent
	if raw == "" {
		raw = outer.Content
	}
	if raw == "" {
		return ""
	}
	var inner struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &inner); err != nil {
		return ""
	}
	return inner.Text
}

type metaKV struct{ k, v string }

func buildMetadata(deviceID, msToken, verifyFP string) []metaKV {
	pairs := []metaKV{
		{"aid", imAID},
		{"app_name", "tiktok_web"},
		{"channel", "web"},
		{"device_platform", "web_pc"},
		{"device_id", deviceID},
		{"region", "CA"},
		{"priority_region", "CA"},
		{"os", "mac"},
		{"referer", "https://www.tiktok.com/messages"},
		{"root_referer", ""},
		{"cookie_enabled", "true"},
		{"screen_width", "1800"},
		{"screen_height", "1169"},
		{"browser_language", "en-US"},
		{"browser_platform", "MacIntel"},
		{"browser_name", "Mozilla"},
		// The web client mirrors the full UA string here, not just the version token.
		{"browser_version", DEFAULT_USER_AGENT},
		{"browser_online", "true"},
	}
	if verifyFP != "" {
		pairs = append(pairs, metaKV{"verifyFp", verifyFP})
	}
	pairs = append(pairs,
		metaKV{"app_language", "en"},
		metaKV{"webcast_language", "en"},
		metaKV{"tz_name", "America/Toronto"},
		metaKV{"is_page_visible", "true"},
		metaKV{"focus_state", "true"},
		metaKV{"is_fullscreen", "false"},
		metaKV{"history_len", "2"},
		metaKV{"user_is_login", "true"},
		metaKV{"data_collection_enabled", "true"},
		metaKV{"from_appID", imAID},
		metaKV{"locale", "en"},
		metaKV{"user_agent", DEFAULT_USER_AGENT},
	)
	if msToken != "" {
		pairs = append(pairs, metaKV{"Web-Sdk-Ms-Token", msToken})
	}
	return pairs
}

func buildInboxPayload(deviceID, msToken, verifyFP string, subCommand uint64) []byte {
	reserved6 := uint64(1)
	if subCommand == 10006 {
		reserved6 = 0
	}

	msg := &tiktokpb.InboxRequest{
		MessageType:    protoUint64(203),
		SubCommand:     protoUint64(subCommand),
		ClientVersion:  protoString("1.6.0"),
		Options:        emptyProtoMessage(),
		PlatformFlag:   protoUint64(3),
		Reserved_6:     protoUint64(reserved6),
		GitHash:        protoString(""),
		DeviceId:       protoString(deviceID),
		ClientPlatform: protoString("web"),
		Metadata:       metadataKVsToProto(buildMetadata(deviceID, msToken, verifyFP)),
		FinalFlag:      protoUint64(1),
		Payload: &tiktokpb.InboxRequestPayload{
			// Only field 1 = 0 matches the pre-schema bridge (uint64 reserved_1).
			// Sending explicit limit=0 / con_type / cursor changed server behavior
			// (small truncated lists); omit those fields so the server uses defaults.
			UserInitList: &tiktokpb.GetUserConversationListRequestBody{
				SortType: protoInt32(0),
			},
		},
	}

	return mustMarshalProto(msg)
}

func mergeInboxConversations(existing, incoming []Conversation) []Conversation {
	indexByID := make(map[string]int, len(existing))
	for i, conv := range existing {
		indexByID[conv.ID] = i
	}

	for _, conv := range incoming {
		if idx, ok := indexByID[conv.ID]; ok {
			if existing[idx].SourceID == 0 {
				existing[idx].SourceID = conv.SourceID
			}
			if existing[idx].Name == "" && conv.Name != "" {
				existing[idx].Name = conv.Name
			}
			if conv.ConversationType != 0 {
				existing[idx].ConversationType = conv.ConversationType
			}
			if len(conv.Participants) > 0 {
				seenParticipants := make(map[string]struct{}, len(existing[idx].Participants))
				for _, participant := range existing[idx].Participants {
					seenParticipants[participant] = struct{}{}
				}
				for _, participant := range conv.Participants {
					if _, seen := seenParticipants[participant]; seen {
						continue
					}
					existing[idx].Participants = append(existing[idx].Participants, participant)
					seenParticipants[participant] = struct{}{}
				}
			}
			continue
		}

		indexByID[conv.ID] = len(existing)
		existing = append(existing, conv)
	}

	return existing
}

// ---------------------------------------------------------------------------
// Response parser
// ---------------------------------------------------------------------------

func parseInboxResponse(body []byte) ([]Conversation, error) {
	var resp tiktokpb.InboxResponse
	if err := unmarshalProto(body, &resp); err != nil {
		return nil, fmt.Errorf("decode top-level response: %w", err)
	}

	userInit := resp.GetPayload().GetUserInitList()
	convs := make([]Conversation, 0, len(userInit.GetConversations())+len(userInit.GetEntries()))

	details := userInit.GetConversations()
	if len(details) > 0 {
		detailConvs := make([]Conversation, 0, len(details))
		for _, detail := range details {
			conv, err := parseConversationDetailProto(detail)
			if err != nil {
				continue
			}
			detailConvs = append(detailConvs, conv)
		}
		convs = mergeInboxConversations(convs, detailConvs)
	}

	entries := userInit.GetEntries()
	if len(entries) == 0 {
		if len(convs) == 0 {
			return nil, nil // empty inbox
		}
		return convs, nil
	}

	seen := make(map[string]struct{}, len(entries))
	entryConvs := make([]Conversation, 0, len(entries))
	for _, entry := range entries {
		if !hasRealMessageProto(entry) {
			continue
		}
		conv, err := parseConversationEntryProto(entry)
		if err != nil {
			continue
		}
		if _, dup := seen[conv.ID]; dup {
			continue
		}
		seen[conv.ID] = struct{}{}
		entryConvs = append(entryConvs, conv)
	}
	return mergeInboxConversations(convs, entryConvs), nil
}

func (c *Client) fetchInbox(ctx context.Context, deviceID, msToken, verifyFP string, subCommand uint64) ([]Conversation, error) {
	payload := buildInboxPayload(deviceID, msToken, verifyFP, subCommand)

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetHeader("Referer", "https://www.tiktok.com/messages").
		SetQueryParams(map[string]string{
			"aid":             imAID,
			"version_code":    "1.0.0",
			"app_name":        "tiktok_web",
			"device_platform": "web_pc",
			"msToken":         msToken,
			"X-Bogus":         randomBogus(),
		}).
		SetBody(payload).
		Post(inboxURL)
	if err != nil {
		return nil, fmt.Errorf("post inbox subcommand %d: %w", subCommand, err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("inbox API subcommand %d returned %d: %s", subCommand, resp.StatusCode(), resp.String())
	}

	convs, err := parseInboxResponse(resp.Body())
	if err != nil {
		return nil, fmt.Errorf("parse inbox subcommand %d response: %w", subCommand, err)
	}
	return convs, nil
}

// ---------------------------------------------------------------------------
// GetInbox
// ---------------------------------------------------------------------------

// GetInbox fetches the authenticated user's conversation list from the TikTok
// IM API. It returns one Conversation per thread, with the other participant's
// user ID in Participants.
func (c *Client) GetInbox(ctx context.Context) ([]Conversation, error) {
	// Extract cookie values we need for the request.
	// rIA already has the full cookie header set at construction time.
	cookie := c.rIA.Header.Get("Cookie")
	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return nil, fmt.Errorf("failed to get universal data: %w", err)
	}

	appContext, err := universalData.getAppContext()
	if err != nil {
		return nil, fmt.Errorf("failed to get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return nil, fmt.Errorf("failed to access wid: %w", err)
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	groupConvs, err := c.fetchInbox(ctx, deviceID, msToken, verifyFP, 10002)
	if err != nil {
		return nil, err
	}
	normalConvs, err := c.fetchInbox(ctx, deviceID, msToken, verifyFP, 10006)
	if err != nil {
		return nil, err
	}
	return mergeInboxConversations(groupConvs, normalConvs), nil
}

// ---------------------------------------------------------------------------
// GetMessages
// ---------------------------------------------------------------------------

// buildGetByConversationPayload constructs the type-301 protobuf request body
// for the get_by_conversation endpoint. sourceID is the uint64 from field 5 of
// the conversation entry (Conversation.SourceID); cursorTsUs is the microsecond
// timestamp cursor from field 25 of the last seen message (0 for the first page).
func buildGetByConversationPayload(deviceID, msToken, verifyFP, convID string, sourceID uint64, count int, cursorTsUs uint64) []byte {
	msg := &tiktokpb.GetByConversationRequest{
		MessageType:    protoUint64(301),
		SubCommand:     protoUint64(1),
		ClientVersion:  protoString("1.6.0"),
		Options:        emptyProtoMessage(),
		PlatformFlag:   protoUint64(3),
		Reserved_6:     protoUint64(0),
		GitHash:        protoString(""),
		ClientPlatform: protoString("web"),
		Metadata:       metadataKVsToProto(buildMetadata(deviceID, msToken, verifyFP)),
		FinalFlag:      protoUint64(1),
		Payload: &tiktokpb.GetByConversationRequestPayload{
			Query: &tiktokpb.GetByConversationQuery{
				ConversationId: protoString(convID),
				Direction:      protoUint64(1),
				SourceId:       protoUint64(sourceID),
				Reserved_4:     protoUint64(1),
				CursorTsUs:     protoUint64(cursorTsUs),
				Count:          protoUint64(uint64(count)),
			},
		},
	}

	return mustMarshalProto(msg)
}

// parseMessageEntry decodes a single message entry from the response.
// It returns the Message and the raw field-25 timestamp (µs) used as the
// pagination cursor.
func parseMessageEntry(ctx context.Context, c *Client, entry *tiktokpb.ConversationMessageEntry) (Message, uint64, error) {
	convID := entry.GetConversationId()
	senderID := strconv.FormatUint(entry.GetSenderUserId(), 10)
	tsMicros := entry.GetTimestampUs()
	cursorTs := entry.GetCursorTsUs()
	serverID := entry.GetServerMessageId()
	msgID := extractClientMsgIDFromTags(entry.GetTags())
	contentJSON := entry.GetContentJson()
	msgType, text, mediaURL, mimeType := parseMessageContent(ctx, c, contentJSON)
	messageSubtype := entry.GetMessageSubtype()
	thumbURL := ""
	decryptKey := ""
	mediaWidth := 0
	mediaHeight := 0
	mediaDurationMs := 0
	if privateType, assetURL, assetThumbURL, assetDecryptKey, width, height, durationMs, ok := parsePrivateMediaFromConversationEntryProto(entry); ok {
		msgType = privateType
		mediaURL = assetURL
		thumbURL = assetThumbURL
		decryptKey = assetDecryptKey
		mimeType = ""
		mediaWidth = width
		mediaHeight = height
		mediaDurationMs = durationMs
		if text == "" {
			if privateType == "video" {
				text = "[video]"
			} else {
				text = "[photo]"
			}
		}
	}
	if stickerURL, stickerText, stickerMIME, ok := parseStickerFromConversationEntryProto(entry); ok {
		msgType = "sticker"
		text = stickerText
		mediaURL = stickerURL
		thumbURL = ""
		decryptKey = ""
		mimeType = stickerMIME
		mediaWidth = 0
		mediaHeight = 0
	}
	replyTo := uint64(0)
	replyQuoted := ""
	if ref := entry.GetMessageReply(); ref != nil {
		replyTo = ref.GetReferencedServerMessageId()
		replyQuoted = parseReplyQuotedTextFromWire(ref.GetQuotedContextJson())
	}
	rawJSON := append([]byte(nil), contentJSON...)

	return Message{
		ServerID:        serverID,
		ClientMessageID: msgID,
		ConvID:          convID,
		SenderID:        senderID,
		Type:            msgType,
		MessageSubtype:  messageSubtype,
		Text:            text,
		MediaURL:        mediaURL,
		ThumbnailURL:    thumbURL,
		MediaDecryptKey: decryptKey,
		MimeType:        mimeType,
		MediaWidth:      mediaWidth,
		MediaHeight:     mediaHeight,
		MediaDurationMs: mediaDurationMs,
		TimestampMs:     int64(tsMicros) / 1000,
		Reactions:       parseReactionsProto(entry.GetReactions()),
		ReplyToServerID: replyTo,
		ReplyQuotedText: replyQuoted,
		SendChainID:     entry.GetSendChainId(),
		SenderSecUID:    entry.GetSenderSecUid(),
		CursorTsUs:      entry.GetCursorTsUs(),
		RawContentJSON:  rawJSON,
	}, cursorTs, nil
}

// parseGetByConversationResponse decodes the protobuf response body.
// Returns the list of messages and the next-page cursor (field-25 timestamp of
// the oldest/last returned message, as a decimal string).
func parseGetByConversationResponse(ctx context.Context, c *Client, body []byte) ([]Message, string, error) {
	var resp tiktokpb.GetByConversationResponse
	if err := unmarshalProto(body, &resp); err != nil {
		return nil, "", fmt.Errorf("decode top-level response: %w", err)
	}

	entries := resp.GetPayload().GetConversation().GetEntries()
	if len(entries) == 0 {
		return nil, "", nil
	}

	messages := make([]Message, 0, len(entries))
	var lastCursorTs uint64
	for i, entry := range entries {
		m, cursorTs, err := parseMessageEntry(ctx, c, entry)
		if err != nil {
			// Log instead of silently dropping so parse regressions are visible.
			fmt.Printf("libtiktok: parseMessageEntry entry %d/%d: %v\n", i+1, len(entries), err)
			continue
		}
		messages = append(messages, m)
		if cursorTs != 0 {
			lastCursorTs = cursorTs
		}
	}

	// Reverse so messages are in chronological order (oldest first).
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	nextCursor := ""
	if lastCursorTs != 0 {
		nextCursor = strconv.FormatUint(lastCursorTs, 10)
	}
	return messages, nextCursor, nil
}

// GetMessages fetches up to 20 messages for the given conversation.
// Pass an empty cursor for the first page; subsequent pages use the cursor
// string returned by the previous call (the field-25 µs timestamp of the
// oldest message in the last batch).
// Returns the messages and the next-page cursor (empty string when exhausted).
func (c *Client) GetMessages(ctx context.Context, conv *Conversation, cursor string) ([]Message, string, error) {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get universal data: %w", err)
	}

	appContext, err := universalData.getAppContext()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return nil, "", fmt.Errorf("failed to access wid from appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	var cursorTsUs uint64
	if cursor != "" {
		cursorTsUs, _ = strconv.ParseUint(cursor, 10, 64)
	}

	payload := buildGetByConversationPayload(deviceID, msToken, verifyFP, conv.ID, conv.SourceID, 20, cursorTsUs)

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetBody(payload).
		Post(getByConvURL)
	if err != nil {
		return nil, "", fmt.Errorf("post get_by_conversation: %w", err)
	}
	if resp.IsError() {
		return nil, "", fmt.Errorf("get_by_conversation API returned %d: %s", resp.StatusCode(), resp.String())
	}

	messages, nextCursor, err := parseGetByConversationResponse(ctx, c, resp.Body())
	if err != nil {
		return nil, "", fmt.Errorf("parse get_by_conversation response: %w", err)
	}
	return messages, nextCursor, nil
}
