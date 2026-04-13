package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// TikTokMessage is a minimal representation of a TikTok DM message as returned
// by the TikTok API / your PoC client.  Replace or extend this struct to match
// whatever your pkg/tiktok client actually returns.
type TikTokMessage struct {
	// MessageID is the unique identifier for this message.
	MessageID string
	// ConversationID is the conversation (portal) this message belongs to.
	ConversationID string
	// SenderID is the numeric TikTok user-ID of the sender.
	SenderID string
	// Type describes the kind of payload: "text", "image", "video", "sticker", …
	Type string
	// Text is the plain-text body (when Type == "text").
	Text string
	// MediaURL is the remote URL of the attachment (when Type != "text").
	// TODO: download & re-upload to Matrix via intent.UploadMedia.
	MediaURL string
	// MimeType is the MIME type of MediaURL (e.g. "image/jpeg").
	MimeType string
	// TimestampMs is the Unix timestamp in milliseconds.
	TimestampMs int64
}

// convertMessage converts a TikTokMessage into a bridgev2.ConvertedMessage
// that the central bridge module will forward to the Matrix room.
//
// This function is used as the ConvertMessageFunc on simplevent.Message[*TikTokMessage].
func (tc *TikTokClient) convertMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg *TikTokMessage,
) (*bridgev2.ConvertedMessage, error) {
	switch msg.Type {
	case "text":
		return convertTextMessage(msg), nil
	case "image":
		return convertImageMessage(ctx, intent, msg)
	case "video":
		return convertVideoMessage(ctx, intent, msg)
	case "sticker":
		return convertStickerMessage(ctx, intent, msg)
	default:
		// Fall back to a notice so the user knows something arrived.
		return convertUnknownMessage(msg), nil
	}
}

// convertTextMessage converts a plain-text TikTok message.
func convertTextMessage(msg *TikTokMessage) *bridgev2.ConvertedMessage {
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    msg.Text,
				},
			},
		},
	}
}

// convertImageMessage converts a TikTok image message.
// TODO: download the image from msg.MediaURL, re-upload it via intent.UploadMedia,
// and populate the URL / encrypted file fields properly.
func convertImageMessage(ctx context.Context, intent bridgev2.MatrixAPI, msg *TikTokMessage) (*bridgev2.ConvertedMessage, error) {
	// Placeholder — replace with actual download + upload logic.
	_ = ctx
	_ = intent
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgImage,
					Body:    "image",
					// URL:  mxcURI,   // set after uploading
				},
			},
		},
	}, nil
}

// convertVideoMessage converts a TikTok video message.
// TODO: same as convertImageMessage — download, re-upload, then set URL.
func convertVideoMessage(ctx context.Context, intent bridgev2.MatrixAPI, msg *TikTokMessage) (*bridgev2.ConvertedMessage, error) {
	_ = ctx
	_ = intent
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgVideo,
					Body:    "video",
					// URL:  mxcURI,
				},
			},
		},
	}, nil
}

// convertStickerMessage converts a TikTok sticker / emoji message.
// Stickers are sent as m.sticker events on Matrix.
// TODO: download + re-upload the sticker image.
func convertStickerMessage(ctx context.Context, intent bridgev2.MatrixAPI, msg *TikTokMessage) (*bridgev2.ConvertedMessage, error) {
	_ = ctx
	_ = intent
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventSticker,
				Content: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "sticker",
					// URL:  mxcURI,
				},
			},
		},
	}, nil
}

// convertUnknownMessage produces a fallback m.notice for unrecognised message types.
func convertUnknownMessage(msg *TikTokMessage) *bridgev2.ConvertedMessage {
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    fmt.Sprintf("[unsupported message type: %s]", msg.Type),
				},
			},
		},
	}
}

// matrixToTikTok converts the body of a Matrix m.text message into whatever
// payload your TikTok API client expects to send.
//
// Extend this to handle formatted bodies (HTML → plain-text stripping), replies,
// mentions, etc. as needed.
func matrixToTikTok(content *event.MessageEventContent) (string, error) {
	switch content.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		text := content.Body
		if content.MsgType == event.MsgEmote {
			text = "* " + text
		}
		return text, nil
	default:
		return "", fmt.Errorf("unsupported Matrix message type: %s", content.MsgType)
	}
}
