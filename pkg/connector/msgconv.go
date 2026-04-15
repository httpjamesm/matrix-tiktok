package connector

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

func messageMetaFromLib(msg libtiktok.Message) *MessageMetadata {
	return &MessageMetadata{
		MsgType:      msg.Type,
		SendChainID:  msg.SendChainID,
		SenderSecUID: msg.SenderSecUID,
		CursorTsUs:   msg.CursorTsUs,
		ContentJSON:  string(msg.RawContentJSON),
	}
}

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
		return tc.convertTextMessage(ctx, portal, msg), nil
	case "image":
		return tc.convertImageMessage(ctx, portal, intent, msg)
	case "sticker":
		return tc.convertStickerMessage(ctx, portal, intent, msg)
	case "video":
		return tc.convertVideoMessage(ctx, portal, intent, msg)
	default:
		// Any other unsupported type falls back to a
		// notice so the user knows something arrived even if we can't render it.
		return convertUnknownMessage(msg), nil
	}
}

// convertImageMessage downloads a TikTok DM private image, decrypts it via the
// libtiktok client, and uploads it to Matrix as an m.image event.
func (tc *TikTokClient) convertImageMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg libtiktok.Message,
) (*bridgev2.ConvertedMessage, error) {
	log := zerolog.Ctx(ctx)

	if msg.MediaURL == "" || msg.MediaDecryptKey == "" {
		return convertImageFallback(msg), nil
	}

	data, mime, err := tc.apiClient.DownloadPrivateImage(ctx, msg.MediaURL, msg.MediaDecryptKey)
	if err != nil && msg.ThumbnailURL != "" && msg.ThumbnailURL != msg.MediaURL {
		log.Warn().Err(err).
			Str("url", msg.MediaURL).
			Str("thumbnail_url", msg.ThumbnailURL).
			Msg("Failed to download full TikTok private image; trying thumbnail")
		data, mime, err = tc.apiClient.DownloadPrivateImage(ctx, msg.ThumbnailURL, msg.MediaDecryptKey)
	}
	if err != nil {
		log.Warn().Err(err).
			Str("url", msg.MediaURL).
			Str("thumbnail_url", msg.ThumbnailURL).
			Msg("Failed to download TikTok private image; falling back to text")
		return convertImageFallback(msg), nil
	}

	fileName := imageFileName(mime)
	size := int64(len(data))
	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    fileName,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     int(size),
		},
	}
	if msg.MediaWidth > 0 {
		content.Info.Width = msg.MediaWidth
	}
	if msg.MediaHeight > 0 {
		content.Info.Height = msg.MediaHeight
	}

	mxcURL, encFile, err := intent.UploadMediaStream(
		ctx,
		portal.MXID,
		size,
		false,
		func(dest io.Writer) (*bridgev2.FileStreamResult, error) {
			if _, err := dest.Write(data); err != nil {
				return nil, err
			}
			return &bridgev2.FileStreamResult{
				FileName: fileName,
				MimeType: mime,
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("upload image to Matrix: %w", err)
	}

	content.URL = mxcURL
	content.File = encFile

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:       event.EventMessage,
				Content:    content,
				DBMetadata: messageMetaFromLib(msg),
			},
		},
	}, nil
}

// convertStickerMessage downloads a TikTok DM sticker from the signed CDN URL
// carried in the message payload and uploads it to Matrix as an m.image event.
// If the sticker cannot be fetched, it falls back to a small text notice.
func (tc *TikTokClient) convertStickerMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg libtiktok.Message,
) (*bridgev2.ConvertedMessage, error) {
	log := zerolog.Ctx(ctx)

	if msg.MediaURL == "" {
		return convertStickerFallback(msg), nil
	}

	data, mime, err := tc.apiClient.DownloadSticker(ctx, msg.MediaURL)
	if err != nil {
		log.Warn().Err(err).Str("url", msg.MediaURL).
			Msg("Failed to download TikTok sticker; falling back to text")
		return convertStickerFallback(msg), nil
	}

	fileName := stickerFileName(mime)
	size := int64(len(data))
	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    fileName,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     int(size),
		},
	}

	mxcURL, encFile, err := intent.UploadMediaStream(
		ctx,
		portal.MXID,
		size,
		false,
		func(dest io.Writer) (*bridgev2.FileStreamResult, error) {
			if _, err := dest.Write(data); err != nil {
				return nil, err
			}
			return &bridgev2.FileStreamResult{
				FileName: fileName,
				MimeType: mime,
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("upload sticker to Matrix: %w", err)
	}

	content.URL = mxcURL
	content.File = encFile

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:       event.EventMessage,
				Content:    content,
				DBMetadata: messageMetaFromLib(msg),
			},
		},
	}, nil
}

// convertTextMessage converts a plain-text TikTok DM. For replies (ReplyToServerID), it sets
// ConvertedMessage.ReplyTo so bridgev2 can attach m.relates_to, and when the parent message
// is already in the bridge DB it adds Matrix rich-reply fallbacks (plain + mx-reply HTML)
// so clients render the quote UI reliably.
func (*TikTokClient) convertTextMessage(ctx context.Context, portal *bridgev2.Portal, msg libtiktok.Message) *bridgev2.ConvertedMessage {
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    msg.Text,
	}
	meta := messageMetaFromLib(msg)
	cm := &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{Type: event.EventMessage, Content: content, DBMetadata: meta},
		},
	}
	if msg.ReplyToServerID == 0 {
		return cm
	}
	parentRemoteID := networkid.MessageID(strconv.FormatUint(msg.ReplyToServerID, 10))
	cm.ReplyTo = &networkid.MessageOptionalPartID{MessageID: parentRemoteID}

	if portal == nil {
		return cm
	}
	parent, err := portal.Bridge.DB.Message.GetFirstPartByID(ctx, portal.Receiver, parentRemoteID)
	if err != nil || parent == nil || parent.MXID == "" {
		return cm
	}

	quoted := strings.TrimSpace(msg.ReplyQuotedText)
	if quoted == "" {
		quoted = "…"
	}
	replyPlain := msg.Text

	var plainQuote string
	if parent.SenderMXID != "" {
		plainQuote = fmt.Sprintf("> <%s> %s", parent.SenderMXID, quoted)
	} else {
		plainQuote = "> " + quoted
	}
	content.Body = plainQuote + "\n\n" + replyPlain

	eventPermalink := matrixToPermalinkRoomEvent(portal.MXID, parent.MXID)
	var formatted strings.Builder
	formatted.WriteString(`<mx-reply><blockquote>`)
	if parent.SenderMXID != "" {
		userPermalink := matrixToPermalinkUser(parent.SenderMXID)
		fmt.Fprintf(&formatted, `<a href="%s">In reply to</a> <a href="%s">%s</a><br>`,
			html.EscapeString(eventPermalink),
			html.EscapeString(userPermalink),
			html.EscapeString(string(parent.SenderMXID)),
		)
	} else {
		fmt.Fprintf(&formatted, `<a href="%s">In reply to a message</a><br>`,
			html.EscapeString(eventPermalink),
		)
	}
	formatted.WriteString(event.TextToHTML(quoted))
	formatted.WriteString(`</blockquote></mx-reply>`)
	formatted.WriteString(event.TextToHTML(replyPlain))

	content.Format = event.FormatHTML
	content.FormattedBody = formatted.String()
	return cm
}

func matrixToPermalinkRoomEvent(roomID id.RoomID, eventID id.EventID) string {
	return fmt.Sprintf("https://matrix.to/#/%s/%s",
		url.PathEscape(string(roomID)),
		url.PathEscape(string(eventID)),
	)
}

func matrixToPermalinkUser(userID id.UserID) string {
	return fmt.Sprintf("https://matrix.to/#/%s", url.PathEscape(string(userID)))
}

// convertVideoMessage downloads a shared TikTok video and uploads it to Matrix
// as an m.video event, alongside an m.text part containing the original URL.
// If the download or upload fails for any reason, it falls back to a plain-text
// message with the URL so the user can still open it manually.
//
// Download path:
//  1. GET the TikTok video page URL (msg.MediaURL).
//  2. Capture the Set-Cookie headers from that response.
//  3. Parse the embedded __DEFAULT_SCOPE__ JSON to extract:
//     __DEFAULT_SCOPE__["webapp.video-detail"].itemInfo.itemStruct.video.playAddr
//  4. GET the play address using the captured cookies.
//  5. Stream the resulting bytes to Matrix via intent.UploadMediaStream.
func (tc *TikTokClient) convertVideoMessage(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	msg libtiktok.Message,
) (*bridgev2.ConvertedMessage, error) {
	log := zerolog.Ctx(ctx)

	data, mime, err := tc.apiClient.DownloadVideo(ctx, msg.MediaURL)
	if err != nil {
		log.Warn().Err(err).Str("url", msg.MediaURL).
			Msg("Failed to download TikTok video; falling back to text")
		return convertVideoFallback(msg), nil
	}

	// Pick a sensible filename and Matrix message type from the MIME type.
	msgType, fileName := msgTypeAndName(mime)

	size := int64(len(data))
	content := &event.MessageEventContent{
		MsgType: msgType,
		Body:    fileName,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     int(size),
		},
	}

	mxcURL, encFile, err := intent.UploadMediaStream(
		ctx,
		portal.MXID,
		size,
		false, // requireFile — an in-memory buffer is fine
		func(dest io.Writer) (*bridgev2.FileStreamResult, error) {
			if _, err := dest.Write(data); err != nil {
				return nil, err
			}
			return &bridgev2.FileStreamResult{
				FileName: fileName,
				MimeType: mime,
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("upload media to Matrix: %w", err)
	}

	// content.URL holds the unencrypted MXC URI; content.File is populated
	// instead when the room is encrypted (bridgev2 handles the distinction).
	content.URL = mxcURL
	content.File = encFile

	linkBody := msg.MediaURL
	if msg.Text != "" {
		linkBody = msg.Text + "\n" + msg.MediaURL
	}

	vmeta := messageMetaFromLib(msg)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type:       event.EventMessage,
				Content:    content,
				DBMetadata: vmeta,
			},
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:       event.MsgText,
					Body:          linkBody,
					Format:        event.FormatHTML,
					FormattedBody: fmt.Sprintf(`<a href="%s">%s</a>`, msg.MediaURL, linkBody),
				},
				DBMetadata: vmeta,
			},
		},
	}, nil
}

// convertVideoFallback renders a TikTok video share as a plain-text message
// containing the URL when media download is unavailable.
func convertVideoFallback(msg libtiktok.Message) *bridgev2.ConvertedMessage {
	body := msg.MediaURL
	if msg.Text != "" {
		body = msg.Text + "\n" + msg.MediaURL
	}
	if body == "" {
		body = "[video]"
	}

	fmeta := messageMetaFromLib(msg)
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
				DBMetadata: fmeta,
			},
		},
	}
}

// convertStickerFallback renders a sticker as a plain-text notice when the
// CDN asset could not be downloaded or the payload was missing a usable URL.
func convertStickerFallback(msg libtiktok.Message) *bridgev2.ConvertedMessage {
	body := msg.Text
	if body == "" {
		body = "[sticker]"
	}
	if msg.MediaURL != "" {
		body += "\n" + msg.MediaURL
	}

	meta := messageMetaFromLib(msg)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    body,
				},
				DBMetadata: meta,
			},
		},
	}
}

// convertImageFallback renders a private image as a plain-text notice when the
// encrypted CDN payload could not be fetched or decrypted.
func convertImageFallback(msg libtiktok.Message) *bridgev2.ConvertedMessage {
	body := msg.Text
	if body == "" {
		body = "[photo]"
	}

	meta := messageMetaFromLib(msg)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    body,
				},
				DBMetadata: meta,
			},
		},
	}
}

// convertUnknownMessage produces a fallback m.notice for message types that
// libtiktok does not yet decode (images, stickers, likes, reactions, etc.).
func convertUnknownMessage(msg libtiktok.Message) *bridgev2.ConvertedMessage {
	body := fmt.Sprintf("[unsupported message type: %s]", msg.Type)
	if msg.Text != "" {
		body = fmt.Sprintf("[%s] %s", msg.Type, msg.Text)
	}

	umeta := messageMetaFromLib(msg)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    body,
				},
				DBMetadata: umeta,
			},
		},
	}
}

// matrixToTikTok converts the body of a Matrix message into the plain-text
// string that TikTok's send API expects. Callers should strip Matrix reply
// fallbacks (e.g. content.RemoveReplyFallback()) before calling when the event
// is a rich reply.
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

// msgTypeAndName returns the Matrix message type and a default filename for a
// given MIME type. We always download video.playAddr so the result is MsgVideo
// in practice; the MIME type from the server is used only to pick the extension.
func msgTypeAndName(mime string) (event.MessageType, string) {
	switch mime {
	case "video/webm":
		return event.MsgVideo, "video.webm"
	case "video/quicktime":
		return event.MsgVideo, "video.mov"
	default:
		return event.MsgVideo, "video.mp4"
	}
}

func stickerFileName(mime string) string {
	switch mime {
	case "image/png":
		return "sticker.png"
	case "image/gif":
		return "sticker.gif"
	case "image/jpeg":
		return "sticker.jpg"
	default:
		return "sticker.webp"
	}
}

func imageFileName(mime string) string {
	switch mime {
	case "image/png":
		return "image.png"
	case "image/gif":
		return "image.gif"
	case "image/webp":
		return "image.webp"
	case "image/jpeg":
		return "image.jpg"
	default:
		return "image.bin"
	}
}
