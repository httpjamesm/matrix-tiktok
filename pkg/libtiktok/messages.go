package libtiktok

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	sendMsgPath    = "/v1/message/send"
	sendMsgFullURL = "https://im-api-sg.tiktok.com/v1/message/send"

	setPropertyPath    = "/v1/message/set_property"
	setPropertyFullURL = "https://im-api-sg.tiktok.com/v1/message/set_property"

	inputStatusPath    = "/v1/client/input_status"
	inputStatusFullURL = "https://im-api-sg.tiktok.com/v1/client/input_status"

	markReadPath = "/v3/conversation/mark_read"

	deleteMsgPath = "/v1/message/delete"
	recallMsgPath = "/v1/message/recall"
)

// SendMessageParams holds the parameters for sending a message.
type SendMessageParams struct {
	ConvID       string
	ConvSourceID uint64
	Text         string
	// IsGroup indicates the target conversation is a group chat, which requires
	// message_kind=2 in the send payload. DMs use message_kind=1.
	IsGroup bool
	// Reply, when non-nil, sends aweType 703 with protobuf field 11 (message_reply).
	Reply *OutgoingMessageReply
	// Image, when non-nil, uploads an encrypted private image and sends the
	// corresponding private_image payload instead of a text body.
	Image *OutgoingImage
	// Video, when non-nil, uploads via VOD and sends a private_video payload.
	// Image and Video must not both be set.
	Video *OutgoingVideo
}

// OutgoingMessageReply carries the TikTok reply envelope for POST /v1/message/send.
type OutgoingMessageReply struct {
	ParentServerMessageID uint64
	ParentSendChainID     uint64
	ParentCursorTsUs      uint64
	ReferencePayloadJSON  []byte
}

// BuildReplyReferenceJSON builds the JSON blob for SendMessageReplyAttachment.reference_payload_json
// (refmsg_uid, refmsg_sec_uid, nested content, etc.), matching captures from TikTok web.
func BuildReplyReferenceJSON(parentContentJSON, refmsgUID, refmsgSecUID string) ([]byte, error) {
	inner := strings.TrimSpace(parentContentJSON)
	if inner == "" {
		inner = `{"aweType":0,"text":""}`
	}
	outer := map[string]any{
		"content":               inner,
		"refmsg_content":        inner,
		"refmsg_sec_uid":        refmsgSecUID,
		"refmsg_type":           7,
		"refmsg_uid":            refmsgUID,
		"refmsg_sub_type":       "",
		"refmsg_template_quote": "",
	}
	return json.Marshal(outer)
}

// SendMessageResponse holds the result of a successful SendMessage call.
type SendMessageResponse struct {
	MessageID string
}

// SendTypingParams holds the parameters for POST /v1/client/input_status.
type SendTypingParams struct {
	ConvID       string
	ConvSourceID uint64
}

// MarkConversationReadParams holds the parameters for POST /v3/conversation/mark_read.
type MarkConversationReadParams struct {
	ConvID           string
	ConvSourceID     uint64 // conversation_short_id on the wire
	ConversationType uint64
	// ReadMessageIndex is ConversationMessageEntry.timestamp_us (proto field 4).
	ReadMessageIndex uint64
	ConvUnreadCount  uint64
	TotalUnreadCount uint64
}

// ---------------------------------------------------------------------------
// EC key helpers
// ---------------------------------------------------------------------------

// generateP256Key generates a fresh secp256r1 (P-256) ECDSA keypair.
func generateP256Key() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// p256UncompressedPoint returns the 65-byte uncompressed public key point
// 0x04 || x || y, each coordinate zero-padded to 32 bytes.
func p256UncompressedPoint(priv *ecdsa.PrivateKey) []byte {
	xRaw := priv.PublicKey.X.Bytes()
	yRaw := priv.PublicKey.Y.Bytes()
	xBytes := make([]byte, 32)
	yBytes := make([]byte, 32)
	copy(xBytes[32-len(xRaw):], xRaw)
	copy(yBytes[32-len(yRaw):], yRaw)
	out := make([]byte, 65)
	out[0] = 0x04
	copy(out[1:33], xBytes)
	copy(out[33:65], yBytes)
	return out
}

// padTo32 zero-pads raw big-endian bytes to exactly 32 bytes.
// Used for P-256 coordinate and signature component encoding.
func padTo32(raw []byte) []byte {
	if len(raw) >= 32 {
		return raw[len(raw)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(raw):], raw)
	return out
}

// b64url returns the standard base64url (no-padding) encoding of b.
func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// DPoP JWT (RFC 9449-style, ES256)
// ---------------------------------------------------------------------------

// buildDPoPJWT creates a DPoP proof JWT signed with ES256 (ECDSA P-256 + SHA-256).
// The public JWK is embedded in the JWT header so the server can verify without a
// pre-registered key. htm is the HTTP method and htu is the absolute request URL.
func buildDPoPJWT(priv *ecdsa.PrivateKey, htm, htu string) (string, error) {
	xBytes := padTo32(priv.PublicKey.X.Bytes())
	yBytes := padTo32(priv.PublicKey.Y.Bytes())

	// Public key as a JWK embedded in the JWT header (P-256 / ES256).
	jwk := map[string]string{
		"crv": "P-256",
		"kty": "EC",
		"x":   b64url(xBytes),
		"y":   b64url(yBytes),
	}
	headerMap := map[string]any{
		"alg": "ES256",
		"typ": "dpop+jwt",
		"jwk": jwk,
	}
	headerJSON, err := json.Marshal(headerMap)
	if err != nil {
		return "", fmt.Errorf("marshal DPoP header: %w", err)
	}

	jtiRaw := make([]byte, 32)
	if _, err := rand.Read(jtiRaw); err != nil {
		return "", fmt.Errorf("generate jti: %w", err)
	}
	payloadMap := map[string]any{
		"jti": b64url(jtiRaw),
		"htm": htm,
		"htu": htu,
		"iat": time.Now().Unix(),
	}
	payloadJSON, err := json.Marshal(payloadMap)
	if err != nil {
		return "", fmt.Errorf("marshal DPoP payload: %w", err)
	}

	signingInput := b64url(headerJSON) + "." + b64url(payloadJSON)
	h := sha256.Sum256([]byte(signingInput))

	// Sign and produce a raw r||s signature (IEEE P1363 / JWS ES256 format).
	r, s, err := ecdsa.Sign(rand.Reader, priv, h[:])
	if err != nil {
		return "", fmt.Errorf("sign DPoP JWT: %w", err)
	}
	sig := make([]byte, 64)
	rRaw := r.Bytes()
	sRaw := s.Bytes()
	copy(sig[32-len(rRaw):32], rRaw)
	copy(sig[64-len(sRaw):64], sRaw)

	return signingInput + "." + b64url(sig), nil
}

// ---------------------------------------------------------------------------
// Metadata builder for the send endpoint
// ---------------------------------------------------------------------------

// buildSendMetadata returns the metadata key-value pairs for the send endpoint.
// It reuses buildMetadata from inbox.go and inserts the five tt-ticket-guard
// fields immediately before the trailing browser_version entry, matching the
// order the web client uses.
func buildSendMetadata(deviceID, msToken, verifyFP, publicKeyB64 string) []metaKV {
	base := buildMetadata(deviceID, msToken, verifyFP)

	// base always ends with browser_version; save it, trim it, then reattach
	// after the ticket-guard entries so the ordering matches the web client.
	last := base[len(base)-1]
	pairs := make([]metaKV, len(base)-1, len(base)+5)
	copy(pairs, base[:len(base)-1])

	pairs = append(pairs,
		metaKV{"tt-ticket-guard-public-key", publicKeyB64},
		metaKV{"tt-ticket-guard-client-data", ""},
		metaKV{"tt-ticket-guard-version", "2"},
		metaKV{"tt-ticket-guard-iteration-version", "0"},
		metaKV{"tt-ticket-guard-web-version", "1"},
		last, // browser_version goes last
	)
	return pairs
}

// buildInputStatusMetadata returns the metadata key-value pairs for the typing
// heartbeat endpoint. This order mirrors the observed web capture more closely
// than the shared inbox/send helper.
func buildInputStatusMetadata(deviceID, msToken, verifyFP string) []metaKV {
	pairs := []metaKV{
		{"aid", imAID},
		{"app_name", "tiktok_web"},
		{"channel", "web"},
		{"device_platform", "web_pc"},
		{"device_id", deviceID},
		{"region", "CA"},
		{"priority_region", "CA"},
		{"os", "mac"},
		{"referer", "https://www.tiktok.com/messages?lang=en"},
		{"root_referer", ""},
		{"cookie_enabled", "true"},
		{"screen_width", "1512"},
		{"screen_height", "982"},
		{"browser_language", "en-US"},
		{"browser_platform", "MacIntel"},
		{"browser_name", "Mozilla"},
		{"browser_online", "true"},
		{"app_language", "en"},
		{"webcast_language", "en"},
		{"tz_name", "America/Toronto"},
		{"is_page_visible", "true"},
		{"focus_state", "true"},
		{"is_fullscreen", "false"},
		{"history_len", "2"},
		{"user_is_login", "true"},
		{"data_collection_enabled", "true"},
		{"from_appID", imAID},
		{"locale", "en"},
		{"user_agent", DefaultUserAgent},
	}
	if verifyFP != "" {
		pairs = append(pairs, metaKV{"verifyFp", verifyFP})
	}
	if msToken != "" {
		pairs = append(pairs, metaKV{"Web-Sdk-Ms-Token", msToken})
	}
	pairs = append(pairs, metaKV{"browser_version", DefaultUserAgent})
	return pairs
}

// ---------------------------------------------------------------------------
// Protobuf payload
// ---------------------------------------------------------------------------

// buildSendPayload constructs the protobuf request body for POST /v1/message/send.
// The structure mirrors the send.json typedef observed in the TikTok web client:
//
//	top-level (type 100 / sub-cmd 10007)
//	  └─ field 8 → { field 100 → inner chat message }
//	       field 1  conversation ID
//	       field 2  msg type (1 = text)
//	       field 3  0
//	       field 4  JSON body {"aweType":0,"text":"..."}
//	       field 5  s:mentioned_users   (repeated, first occurrence)
//	       field 5  s:client_message_id (repeated, second occurrence)
//	       field 6  7
//	       field 7  "deprecated"
//	       field 8  client message UUID
//	       field 11 message_reply when aweType=703 (reply)
//	  └─ field 15  repeated metadata k/v pairs (including ticket-guard)
func buildSendPayload(convID string, convSourceID uint64, text, deviceID, msToken, verifyFP, publicKeyB64, clientMsgID string, isGroup bool, reply *OutgoingMessageReply, image *uploadedPrivateImage, video *uploadedPrivateVideo) ([]byte, error) {
	messageKind := uint64(1)
	if isGroup {
		messageKind = 2
	}
	sendBody := &tiktokpb.SendMessageBody{
		ConversationId:  protoString(convID),
		MessageKind:     protoUint64(messageKind),
		Reserved_3:      protoUint64(convSourceID),
		Deprecated:      protoString("deprecated"),
		ClientMessageId: protoString(clientMsgID),
		Tags: []*tiktokpb.MetadataTag{
			{Key: protoString("s:mentioned_users"), Value: nil},
			{Key: protoString("s:client_message_id"), Value: []byte(clientMsgID)},
		},
	}
	var textJSON []byte
	var err error
	if reply != nil {
		sendBody.Reserved_3 = protoUint64(reply.ParentSendChainID)
		sendBody.MessageReply = &tiktokpb.SendMessageReplyAttachment{
			ReferencedServerMessageId:          protoUint64(reply.ParentServerMessageID),
			ReferencePayloadJson:               reply.ReferencePayloadJSON,
			DuplicateReferencedServerMessageId: protoUint64(reply.ParentServerMessageID),
			ParentCursorTsUs:                   protoUint64(reply.ParentCursorTsUs),
		}
	}
	if image != nil {
		sendBody.ContentJson = []byte{}
		sendBody.Reserved_6 = protoUint64(1802)
		sendBody.MessageSubtype = protoString("private_image")
		sendBody.Attachment = &tiktokpb.SendAttachmentPayload{
			PrivateImage: &tiktokpb.SendPrivateImageAttachmentPayload{
				Assets: []*tiktokpb.SendPrivateImageAsset{
					{
						Path: protoString(image.URI),
						Size: &tiktokpb.SendMediaSize{
							Width:  protoUint64(uint64(image.Width)),
							Height: protoUint64(uint64(image.Height)),
						},
						DecryptKey: protoString(image.DecryptKey),
						Reserved_7: protoUint64(0),
					},
				},
				DisplayTexts: &tiktokpb.SendPrivateImageDisplayTexts{
					SenderPreview:    &tiktokpb.LocalizedText{Text: protoString("You sent a 📷")},
					RecipientPreview: &tiktokpb.LocalizedText{Text: protoString("sent a 📷")},
					BracketedPreview: &tiktokpb.LocalizedText{Text: protoString("[Photo]")},
				},
				Metadata: &tiktokpb.SendPrivateImageMetadataList{
					Entries: []*tiktokpb.SendPrivateImageMetadataEntry{
						{
							Path: protoString(image.URI),
							Properties: metadataKVsToProto([]metaKV{
								{"image_width", strconv.Itoa(image.Width)},
								{"image_height", strconv.Itoa(image.Height)},
								{"decrypt_key", image.DecryptKey},
								{"quote_preview", "dm_cam_preview_photo"},
								{"sender_preview", "sender_preview"},
							}),
						},
					},
				},
			},
		}
		sendBody.PrivateImage = &tiktokpb.SendPrivateImageAttachmentSummary{
			Reserved_1: protoUint64(1),
			Path:       protoString(image.URI),
			DecryptKey: protoString(image.DecryptKey),
			FileInfo: &tiktokpb.SendPrivateImageFileInfo{
				Width:    protoUint64(uint64(image.Width)),
				Height:   protoUint64(uint64(image.Height)),
				Size:     protoUint64(uint64(image.Size)),
				FileName: protoString(image.FileName),
			},
		}
	} else if video != nil {
		sendBody.ContentJson = []byte{}
		sendBody.Reserved_6 = protoUint64(1803)
		sendBody.MessageSubtype = protoString("private_video")
		sendBody.Attachment = &tiktokpb.SendAttachmentPayload{
			PrivateVideo: &tiktokpb.SendPrivateVideoAttachmentPayload{
				Primary: &tiktokpb.SendPrivateVideoPrimaryAsset{
					Vid:        protoString(video.Vid),
					Reserved_2: protoUint64(0),
					Poster: &tiktokpb.SendPrivateVideoPoster{
						Uri: protoString(video.PosterURI),
					},
					DisplaySize: &tiktokpb.SendMediaSize{
						Width:  protoUint64(uint64(video.Width)),
						Height: protoUint64(uint64(video.Height)),
					},
				},
				Metadata: &tiktokpb.SendPrivateVideoMetadataList{
					Entries: []*tiktokpb.SendPrivateVideoMetadataEntry{
						{Inner: &tiktokpb.SendPrivateVideoMetadataInner{Vid: protoString(video.Vid)}},
					},
				},
			},
		}
		// Field 17 reuses the private-image summary shape for private_video traffic.
		sendBody.PrivateImage = &tiktokpb.SendPrivateImageAttachmentSummary{
			Reserved_1: protoUint64(2),
			Path:       protoString(video.Vid),
			DecryptKey: protoString(""),
			FileInfo: &tiktokpb.SendPrivateImageFileInfo{
				Width:      protoUint64(uint64(video.Width)),
				Height:     protoUint64(uint64(video.Height)),
				Reserved_3: protoUint64(uint64(video.DurationMs)),
				Size:       protoUint64(uint64(video.Size)),
				FileName:   protoString(video.FileName),
				VideoCodec: protoString(video.Codec),
			},
		}
	} else {
		textJSON, err = json.Marshal(map[string]any{"aweType": 0, "text": text})
		if err != nil {
			return nil, fmt.Errorf("marshal text payload: %w", err)
		}
		if reply != nil {
			textJSON, err = json.Marshal(map[string]any{"aweType": 703, "text": text})
			if err != nil {
				return nil, fmt.Errorf("marshal reply payload: %w", err)
			}
		}
		sendBody.Reserved_6 = protoUint64(7)
		sendBody.ContentJson = textJSON
	}

	subCommand := protoUint64(10009)
	reserved6 := protoUint64(0)
	if isGroup {
		subCommand = protoUint64(10014)
		reserved6 = protoUint64(1)
	}

	msg := &tiktokpb.SendRequest{
		MessageType:    protoUint64(100),
		SubCommand:     subCommand,
		ClientVersion:  protoString("1.6.0"),
		Options:        emptyProtoMessage(),
		PlatformFlag:   protoUint64(3),
		Reserved_6:     reserved6,
		GitHash:        protoString(""),
		DeviceId:       protoString(deviceID),
		ClientPlatform: protoString("web"),
		Metadata:       metadataKVsToProto(buildSendMetadata(deviceID, msToken, verifyFP, publicKeyB64)),
		FinalFlag:      protoUint64(1),
		Payload: &tiktokpb.SendRequestPayload{
			Send: sendBody,
		},
	}

	return marshalProto(msg)
}

// buildInputStatusPayload constructs the protobuf request body for
// POST /v1/client/input_status.
func buildInputStatusPayload(p SendTypingParams, deviceID, msToken, verifyFP string) ([]byte, error) {
	msg := &tiktokpb.InputStatusRequest{
		MessageType:    protoUint64(411),
		SubCommand:     protoUint64(10100),
		ClientVersion:  protoString("1.6.3"),
		Options:        emptyProtoMessage(),
		PlatformFlag:   protoUint64(3),
		Reserved_6:     protoUint64(0),
		Reserved_7:     emptyProtoMessage(),
		DeviceId:       protoString(deviceID),
		ClientPlatform: protoString("web"),
		Metadata:       metadataKVsToProto(buildInputStatusMetadata(deviceID, msToken, verifyFP)),
		FinalFlag:      protoUint64(1),
		Payload: &tiktokpb.InputStatusRequestPayload{
			InputStatus: &tiktokpb.InputStatusBody{
				ConversationId: protoString(p.ConvID),
				TypingStatus:   protoUint64(1),
				SourceId:       protoUint64(p.ConvSourceID),
				Reserved_4:     protoUint64(3),
			},
		},
	}

	return marshalProto(msg)
}

// buildMarkConversationReadPayload constructs the protobuf request body for
// POST /v3/conversation/mark_read.
func buildMarkConversationReadPayload(p MarkConversationReadParams, deviceID, msToken, verifyFP string) ([]byte, error) {
	msg := &tiktokpb.MarkConversationReadRequest{
		MessageType:    protoUint64(604),
		SubCommand:     protoUint64(1),
		ClientVersion:  protoString("1.6.0"),
		Options:        emptyProtoMessage(),
		PlatformFlag:   protoUint64(3),
		Reserved_6:     protoUint64(0),
		GitHash:        protoString(""),
		DeviceId:       protoString(deviceID),
		ClientPlatform: protoString("web"),
		Metadata:       metadataKVsToProto(buildInputStatusMetadata(deviceID, msToken, verifyFP)),
		FinalFlag:      protoUint64(1),
		Payload: &tiktokpb.MarkConversationReadRequestPayload{
			MarkConversationRead: &tiktokpb.MarkConversationReadRequestBody{
				ConversationId:       protoString(p.ConvID),
				ConversationShortId:    protoUint64(p.ConvSourceID),
				ConversationType:     protoUint64(p.ConversationType),
				ReadMessageIndex:     protoUint64(p.ReadMessageIndex),
				ConvUnreadCount:      protoUint64(p.ConvUnreadCount),
				TotalUnreadCount:     protoUint64(p.TotalUnreadCount),
			},
		},
	}

	return marshalProto(msg)
}

// ---------------------------------------------------------------------------
// Response parser
// ---------------------------------------------------------------------------

// parseSendResponse attempts to extract the server-assigned message ID from the
// send response protobuf.
//
// The response envelope mirrors the inbox/get_by_conversation shape: the inner
// payload sits at field 6, which in turn nests the echoed message at field 100
// → field 1 (message ID string). A second probe at field 8 → 100 → 1 is tried
// as a fallback.
//
// Returns an error when no server-assigned ID can be located.
func parseSendResponse(body []byte) (string, error) {
	if len(body) == 0 {
		return "", fmt.Errorf("empty response body")
	}

	if id, ok := extractSendResponseMessageID(body); ok {
		return id, nil
	}

	var resp tiktokpb.SendResponse
	if err := unmarshalProto(body, &resp); err != nil {
		return "", fmt.Errorf("decode send response: %w", err)
	}

	if id := resp.GetPrimary().GetSend().GetMessageId(); id != "" {
		return id, nil
	}
	if id := resp.GetFallback().GetSend().GetMessageId(); id != "" {
		return id, nil
	}

	return "", fmt.Errorf("server-assigned message ID not found in response")
}

func extractSendResponseMessageID(body []byte) (string, bool) {
	for _, outerField := range []protowire.Number{6, 8} {
		if payload, ok := extractLengthDelimitedField(body, outerField); ok {
			if sendPayload, ok := extractLengthDelimitedField(payload, 100); ok {
				if id, ok := extractStringOrVarintField(sendPayload, 1); ok {
					return id, true
				}
			}
		}
	}
	return "", false
}

func extractLengthDelimitedField(data []byte, target protowire.Number) ([]byte, bool) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, false
		}
		data = data[n:]
		switch typ {
		case protowire.BytesType:
			value, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return nil, false
			}
			if num == target {
				return value, true
			}
			data = data[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return nil, false
			}
			data = data[m:]
		}
	}
	return nil, false
}

func extractStringOrVarintField(data []byte, target protowire.Number) (string, bool) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return "", false
		}
		data = data[n:]
		switch typ {
		case protowire.BytesType:
			value, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return "", false
			}
			if num == target {
				return string(value), true
			}
			data = data[m:]
		case protowire.VarintType:
			value, m := protowire.ConsumeVarint(data)
			if m < 0 {
				return "", false
			}
			if num == target {
				return strconv.FormatUint(value, 10), true
			}
			data = data[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return "", false
			}
			data = data[m:]
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// SendMessage
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Reaction types
// ---------------------------------------------------------------------------

// ReactionAction indicates whether to add or remove a reaction.
type ReactionAction uint64

const (
	// ReactionAdd adds the emoji reaction to a message.
	ReactionAdd ReactionAction = 0
	// ReactionRemove removes the emoji reaction from a message.
	ReactionRemove ReactionAction = 1
)

// SendReactionParams holds the parameters for reacting to a message.
type SendReactionParams struct {
	// ConvID is the conversation ID (e.g. "0:1:X:Y").
	ConvID string
	// IsGroup indicates a group chat; the set_property envelope must set
	// reserved_6=1 (DMs use 0).
	IsGroup bool
	// Emoji is the raw emoji character to react with; the "e:" prefix is added
	// internally. Example: "❤️"
	Emoji string
	// Action is ReactionAdd or ReactionRemove.
	Action ReactionAction
	// SelfUserID is the numeric user ID of the person sending the reaction.
	SelfUserID      string
	ConvoSourceID   uint64
	ServerMessageID uint64
}

// DeleteMessageParams identifies a message for POST /v1/message/delete
// (delete for self / local hide) or POST /v1/message/recall (delete for everyone).
type DeleteMessageParams struct {
	ConvID          string
	ConvoSourceID   uint64
	ServerMessageID uint64
}

// ---------------------------------------------------------------------------
// Reaction protobuf payload
// ---------------------------------------------------------------------------

// buildReactionPayload constructs the protobuf request body for
// POST /v1/message/set_property.
//
// Wire shape (field numbers mirror the captured typedef):
//
//	top-level (type 705 / sub-cmd 10008)
//	  └─ field 8 → { field 705 → reaction wrapper }
//	       field 1  → inner reaction message
//	           field 1  conversation ID
//	           field 2  action_flag: 1 (DM), 2 (group)
//	           field 3  ConvoSourceID
//	           field 4  ServerMessageID
//	           field 5  s:client_message_id UUID
//	           field 6  repeated reaction entry { 1:action  2:"e:<emoji>"  4:userID }
//	       field 2  "deprecated"
//	  └─ field 15  repeated metadata k/v pairs (including ticket-guard)
func buildReactionPayload(p SendReactionParams, deviceID, msToken, verifyFP, publicKeyB64, clientMsgID string) ([]byte, error) {
	reserved6 := protoUint64(0)
	if p.IsGroup {
		reserved6 = protoUint64(1)
	}

	actionFlag := protoUint64(1)
	if p.IsGroup {
		actionFlag = protoUint64(2)
	}

	msg := &tiktokpb.ReactionRequest{
		MessageType:    protoUint64(705),
		SubCommand:     protoUint64(10008),
		ClientVersion:  protoString("1.6.0"),
		Options:        emptyProtoMessage(),
		PlatformFlag:   protoUint64(3),
		Reserved_6:     reserved6,
		GitHash:        protoString(""),
		DeviceId:       protoString(deviceID),
		ClientPlatform: protoString("web"),
		Metadata:       metadataKVsToProto(buildSendMetadata(deviceID, msToken, verifyFP, publicKeyB64)),
		FinalFlag:      protoUint64(1),
		Payload: &tiktokpb.ReactionRequestPayload{
			Wrapper: &tiktokpb.ReactionWrapper{
				Deprecated: protoString("deprecated"),
				Body: &tiktokpb.ReactionBody{
					ConversationId:  protoString(p.ConvID),
					ActionFlag:      actionFlag,
					SourceId:        protoUint64(p.ConvoSourceID),
					ServerMessageId: protoUint64(p.ServerMessageID),
					ClientMessageId: protoString(clientMsgID),
					Reactions: []*tiktokpb.ReactionMutation{
						{
							Action:      protoUint64(uint64(p.Action)),
							ReactionKey: protoString("e:" + p.Emoji),
							SelfUserId:  protoString(p.SelfUserID),
						},
					},
				},
			},
		},
	}

	return marshalProto(msg)
}

// buildDeletePayload constructs the protobuf request body for POST /v1/message/delete.
//
// Wire shape:
//
//	top-level (type 701 / sub-cmd 10007)
//	  └─ field 8 → { field 701 → delete target }
//	       field 1  conversation ID
//	       field 2  source_id
//	       field 3  1
//	       field 4  server_message_id
//	  └─ field 15  metadata (same style as inbox / get_by_conversation)
func buildDeletePayload(convID string, sourceID, serverMsgID uint64, deviceID, msToken, verifyFP string) ([]byte, error) {
	msg := &tiktokpb.DeleteRequest{
		MessageType:    protoUint64(701),
		SubCommand:     protoUint64(10007),
		ClientVersion:  protoString("1.6.0"),
		Options:        emptyProtoMessage(),
		PlatformFlag:   protoUint64(3),
		Reserved_6:     protoUint64(0),
		GitHash:        protoString(""),
		DeviceId:       protoString(deviceID),
		ClientPlatform: protoString("web"),
		Metadata:       metadataKVsToProto(buildMetadata(deviceID, msToken, verifyFP)),
		FinalFlag:      protoUint64(1),
		Payload: &tiktokpb.DeleteRequestPayload{
			Delete: &tiktokpb.DeleteMessageBody{
				ConversationId:  protoString(convID),
				SourceId:        protoUint64(sourceID),
				Reserved_3:      protoUint64(1),
				ServerMessageId: protoUint64(serverMsgID),
			},
		},
	}

	return marshalProto(msg)
}

// buildRecallPayload constructs the protobuf request body for POST /v1/message/recall.
//
// Wire shape:
//
//	top-level (type 702 / sub-cmd 10025)
//	  └─ field 8 → { field 702 → recall target (same fields as delete-for-self) }
//	       field 1  conversation ID
//	       field 2  source_id
//	       field 3  1
//	       field 4  server_message_id
//	  └─ field 15  metadata (same style as delete)
func buildRecallPayload(convID string, sourceID, serverMsgID uint64, deviceID, msToken, verifyFP string) ([]byte, error) {
	msg := &tiktokpb.RecallRequest{
		MessageType:    protoUint64(702),
		SubCommand:     protoUint64(10025),
		ClientVersion:  protoString("1.6.0"),
		Options:        emptyProtoMessage(),
		PlatformFlag:   protoUint64(3),
		Reserved_6:     protoUint64(0),
		GitHash:        protoString(""),
		DeviceId:       protoString(deviceID),
		ClientPlatform: protoString("web"),
		Metadata:       metadataKVsToProto(buildMetadata(deviceID, msToken, verifyFP)),
		FinalFlag:      protoUint64(1),
		Payload: &tiktokpb.RecallRequestPayload{
			Recall: &tiktokpb.DeleteMessageBody{
				ConversationId:  protoString(convID),
				SourceId:        protoUint64(sourceID),
				Reserved_3:      protoUint64(1),
				ServerMessageId: protoUint64(serverMsgID),
			},
		},
	}

	return marshalProto(msg)
}

// ---------------------------------------------------------------------------
// SendReaction
// ---------------------------------------------------------------------------

// SendReaction adds or removes an emoji reaction on a message.
//
// Protocol summary:
//  1. Fetch the device ID (wid) from /messages universal data.
//  2. Generate a fresh P-256 keypair for tt-ticket-guard.
//  3. Build a DPoP proof JWT (ES256) bound to POST setPropertyFullURL.
//  4. Construct the type-705 protobuf request body.
//  5. POST to /v1/message/set_property with ztca-dpop in the query string.
func (c *Client) SendReaction(ctx context.Context, p SendReactionParams) error {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return fmt.Errorf("get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return fmt.Errorf("wid not found in appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	priv, err := generateP256Key()
	if err != nil {
		return fmt.Errorf("generate P-256 key: %w", err)
	}
	publicKeyB64 := base64.StdEncoding.EncodeToString(p256UncompressedPoint(priv))

	dpopToken, err := buildDPoPJWT(priv, "POST", setPropertyFullURL)
	if err != nil {
		return fmt.Errorf("build DPoP JWT: %w", err)
	}

	clientMsgID := uuid.New().String()

	payload, err := buildReactionPayload(p, deviceID, msToken, verifyFP, publicKeyB64, clientMsgID)
	if err != nil {
		return fmt.Errorf("build reaction payload: %w", err)
	}

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetHeader("Pragma", "no-cache").
		SetHeader("Priority", "u=1, i").
		SetHeader("tt-ticket-guard-iteration-version", "0").
		SetHeader("tt-ticket-guard-public-key", publicKeyB64).
		SetHeader("tt-ticket-guard-version", "2").
		SetHeader("tt-ticket-guard-web-version", "1").
		SetQueryParams(map[string]string{
			"aid":             imAID,
			"version_code":    "1.0.0",
			"app_name":        "tiktok_web",
			"device_platform": "web_pc",
			"ztca-version":    "1",
			"ztca-dpop":       dpopToken,
			"msToken":         msToken,
			"X-Bogus":         randomBogus(),
		}).
		SetBody(payload).
		Post(setPropertyPath)
	if err != nil {
		return fmt.Errorf("POST set_property: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("set_property API returned %d: %s", resp.StatusCode(), resp.String())
	}

	return nil
}

// SendTyping posts a typing heartbeat to TikTok for the specified conversation.
func (c *Client) SendTyping(ctx context.Context, p SendTypingParams) error {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return fmt.Errorf("get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return fmt.Errorf("wid not found in appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	payload, err := buildInputStatusPayload(p, deviceID, msToken, verifyFP)
	if err != nil {
		return fmt.Errorf("build input_status payload: %w", err)
	}

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetHeader("Pragma", "no-cache").
		SetHeader("Priority", "u=1, i").
		SetQueryParams(map[string]string{
			"aid":             imAID,
			"version_code":    "1.0.0",
			"app_name":        "tiktok_web",
			"device_platform": "web_pc",
			"msToken":         msToken,
			"X-Bogus":         randomBogus(),
		}).
		SetBody(payload).
		Post(inputStatusPath)
	if err != nil {
		return fmt.Errorf("POST input_status: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("input_status API returned %d: %s", resp.StatusCode(), resp.String())
	}

	return nil
}

// MarkConversationRead marks a conversation as read on TikTok (POST /v3/conversation/mark_read).
func (c *Client) MarkConversationRead(ctx context.Context, p MarkConversationReadParams) error {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return fmt.Errorf("get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return fmt.Errorf("wid not found in appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	payload, err := buildMarkConversationReadPayload(p, deviceID, msToken, verifyFP)
	if err != nil {
		return fmt.Errorf("build mark_read payload: %w", err)
	}

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetHeader("Pragma", "no-cache").
		SetHeader("Priority", "u=1, i").
		SetQueryParams(map[string]string{
			"aid":             imAID,
			"version_code":    "1.0.0",
			"app_name":        "tiktok_web",
			"device_platform": "web_pc",
			"msToken":         msToken,
			"X-Bogus":         randomBogus(),
		}).
		SetBody(payload).
		Post(markReadPath)
	if err != nil {
		return fmt.Errorf("POST mark_read: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("mark_read API returned %d: %s", resp.StatusCode(), resp.String())
	}

	return nil
}

// SendMessage sends a text message to the specified conversation and returns the
// acknowledged message ID.
//
// Protocol summary (matching the TikTok web IM client):
//  1. Fetch the device ID (wid) from /messages universal data.
//  2. Generate a fresh P-256 keypair — the uncompressed public key is embedded
//     both in the protobuf metadata (tt-ticket-guard-public-key) and the HTTP
//     request header.
//  3. Build a DPoP proof JWT (ES256) bound to POST sendMsgFullURL.
//  4. Construct the type-100 protobuf request body.
//  5. POST to /v1/message/send with ztca-dpop in the query string and all
//     ticket-guard headers set.
//  6. Parse the response for the server-assigned message ID (required).
func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (*SendMessageResponse, error) {
	cookie := c.rIA.Header.Get("Cookie")
	if p.Image != nil && p.Video != nil {
		return nil, fmt.Errorf("image and video cannot be sent in the same message")
	}
	if p.Image != nil && strings.TrimSpace(p.Text) != "" {
		return nil, fmt.Errorf("image captions are not supported")
	}
	if p.Video != nil && strings.TrimSpace(p.Text) != "" {
		return nil, fmt.Errorf("video captions are not supported")
	}

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return nil, fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return nil, fmt.Errorf("get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return nil, fmt.Errorf("wid not found in appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	// Generate a fresh P-256 keypair for tt-ticket-guard authentication.
	priv, err := generateP256Key()
	if err != nil {
		return nil, fmt.Errorf("generate P-256 key: %w", err)
	}
	publicKeyB64 := base64.StdEncoding.EncodeToString(p256UncompressedPoint(priv))

	// Build a DPoP proof JWT bound to this specific POST request.
	dpopToken, err := buildDPoPJWT(priv, "POST", sendMsgFullURL)
	if err != nil {
		return nil, fmt.Errorf("build DPoP JWT: %w", err)
	}

	// Client-generated UUID v4, echoed inside the protobuf as s:client_message_id
	// and field 8 of the inner message.
	clientMsgID := uuid.New().String()

	var uploadedImage *uploadedPrivateImage
	var uploadedVideo *uploadedPrivateVideo
	if p.Image != nil {
		uploadedImage, err = c.uploadImage(ctx, p.Image)
		if err != nil {
			return nil, fmt.Errorf("upload image: %w", err)
		}
	}
	if p.Video != nil {
		uploadedVideo, err = c.uploadVideo(ctx, p.Video)
		if err != nil {
			return nil, fmt.Errorf("upload video: %w", err)
		}
	}

	payload, err := buildSendPayload(p.ConvID, p.ConvSourceID, p.Text, deviceID, msToken, verifyFP, publicKeyB64, clientMsgID, p.IsGroup, p.Reply, uploadedImage, uploadedVideo)
	if err != nil {
		return nil, fmt.Errorf("build send payload: %w", err)
	}

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetHeader("Pragma", "no-cache").
		SetHeader("Priority", "u=1, i").
		SetHeader("tt-ticket-guard-iteration-version", "0").
		SetHeader("tt-ticket-guard-public-key", publicKeyB64).
		SetHeader("tt-ticket-guard-version", "2").
		SetHeader("tt-ticket-guard-web-version", "1").
		SetQueryParams(map[string]string{
			"aid":             imAID,
			"version_code":    "1.0.0",
			"app_name":        "tiktok_web",
			"device_platform": "web_pc",
			"ztca-version":    "1",
			"ztca-dpop":       dpopToken,
			"msToken":         msToken,
			"X-Bogus":         randomBogus(),
		}).
		SetBody(payload).
		Post(sendMsgPath)
	if err != nil {
		return nil, fmt.Errorf("POST send message: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("send API returned %d: %s", resp.StatusCode(), resp.String())
	}

	msgID, err := parseSendResponse(resp.Body())
	if err != nil {
		return nil, fmt.Errorf("parse send response: %w", err)
	}

	return &SendMessageResponse{MessageID: msgID}, nil
}

// RecallMessage recalls (delete-for-everyone) a single chat message on TikTok's IM API.
//
// Like delete-for-self, the web client posts to this endpoint with no URL query
// parameters; authentication relies on the session cookies on the client.
func (c *Client) RecallMessage(ctx context.Context, p DeleteMessageParams) error {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return fmt.Errorf("get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return fmt.Errorf("wid not found in appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	payload, err := buildRecallPayload(p.ConvID, p.ConvoSourceID, p.ServerMessageID, deviceID, msToken, verifyFP)
	if err != nil {
		return fmt.Errorf("build recall payload: %w", err)
	}

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetBody(payload).
		Post(recallMsgPath)
	if err != nil {
		return fmt.Errorf("POST recall message: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("recall message API returned %d: %s", resp.StatusCode(), resp.String())
	}

	return nil
}

// DeleteMessage hides a message for the current account only (POST /v1/message/delete).
// For delete-for-everyone after sending a message, use RecallMessage.
//
// Unlike send and set_property, the web client posts to this endpoint with no
// URL query parameters (no ztca-dpop / X-Bogus); authentication relies on the
// session cookies already configured on the client.
func (c *Client) DeleteMessage(ctx context.Context, p DeleteMessageParams) error {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return fmt.Errorf("get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return fmt.Errorf("wid not found in appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	payload, err := buildDeletePayload(p.ConvID, p.ConvoSourceID, p.ServerMessageID, deviceID, msToken, verifyFP)
	if err != nil {
		return fmt.Errorf("build delete payload: %w", err)
	}

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetBody(payload).
		Post(deleteMsgPath)
	if err != nil {
		return fmt.Errorf("POST delete message: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("delete message API returned %d: %s", resp.StatusCode(), resp.String())
	}

	return nil
}
