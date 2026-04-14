package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

// convertMessage converts a libtiktok.Message into a bridgev2.ConvertedMessage
// that the central bridge module will forward to the Matrix room.
//
// This function is used as the ConvertMessageFunc on simplevent.Message[libtiktok.Message].
func (tc *TikTokClient) convertMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg libtiktok.Message,
) (*bridgev2.ConvertedMessage, error) {
	switch msg.Type {
	case "text":
		return convertTextMessage(msg), nil
	case "video":
		return convertVideoMessage(ctx, intent, msg)
	default:
		// Any other aweType (images, stickers, likes, etc.) falls back to a
		// notice so the user knows something arrived even if we can't render it.
		return convertUnknownMessage(msg), nil
	}
}

// convertTextMessage converts a plain-text TikTok DM.
func convertTextMessage(msg libtiktok.Message) *bridgev2.ConvertedMessage {
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

// convertVideoMessage converts a TikTok video share message.
// TikTok video shares carry a tiktok.com/video/<id> URL in msg.MediaURL and a
// caption in msg.Text. We render them as an m.text with the URL + caption, since
// downloading and re-uploading TikTok videos requires additional auth work.
//
// TODO: download from msg.MediaURL, re-upload via intent.UploadMedia, and switch
// to event.MsgVideo with proper URL/info fields once auth is sorted.
func convertVideoMessage(_ context.Context, _ bridgev2.MatrixAPI, msg libtiktok.Message) (*bridgev2.ConvertedMessage, error) {
	body := msg.MediaURL
	if msg.Text != "" {
		body = msg.Text + "\n" + msg.MediaURL
	}
	if body == "" {
		body = "[video]"
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:       event.MsgText,
					Body:          body,
					Format:        event.FormatHTML,
					FormattedBody: fmt.Sprintf(`<a href="%s">%s</a>`, msg.MediaURL, body),
				},
			},
		},
	}, nil
}

// convertUnknownMessage produces a fallback m.notice for message types that
// libtiktok does not yet decode (images, stickers, likes, reactions, etc.).
func convertUnknownMessage(msg libtiktok.Message) *bridgev2.ConvertedMessage {
	body := fmt.Sprintf("[unsupported message type: %s]", msg.Type)
	if msg.Text != "" {
		body = fmt.Sprintf("[%s] %s", msg.Type, msg.Text)
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    body,
				},
			},
		},
	}
}

// matrixToTikTok converts the body of a Matrix message into the plain-text
// string that TikTok's send API expects. Extend this as needed for formatted
// bodies, replies, mentions, etc.
func matrixToTikTok(content *event.MessageEventContent) (string, error) {
	switch content.MsgType {
	case event.MsgText, event.MsgNotice:
		return content.Body, nil
	case event.MsgEmote:
		return "* " + content.Body, nil
	default:
		return "", fmt.Errorf("unsupported Matrix message type for TikTok: %s", content.MsgType)
	}
}
