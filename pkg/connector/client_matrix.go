package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

// HandleMatrixMessage forwards a Matrix message to the TikTok conversation.
func (tc *TikTokClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "connector-matrix-outbound").Logger()
	ctx = log.WithContext(ctx)

	content := *msg.Content
	content.RemoveReplyFallback()

	reply := tc.buildOutgoingReply(log, msg)

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return nil, err
	}
	log.Debug().
		Str("conversation_id", conv.ID).
		Uint64("source_id", conv.SourceID).
		Uint64("conversation_type", conv.ConversationType).
		Str("msgtype", string(content.MsgType)).
		Bool("has_reply", reply != nil).
		Msg("Classifying outbound Matrix message")

	if content.MsgType == event.MsgImage {
		log.Debug().Str("conversation_id", conv.ID).Msg("Routing outbound Matrix message as image")
		return tc.handleMatrixImageMessage(ctx, msg, conv, &content, reply)
	}
	if content.MsgType == event.MsgVideo {
		log.Debug().Str("conversation_id", conv.ID).Msg("Routing outbound Matrix message as video")
		return tc.handleMatrixVideoMessage(ctx, msg, conv, &content, reply)
	}
	if content.MsgType == event.MsgFile {
		mimeType := ""
		if content.Info != nil {
			mimeType = content.Info.MimeType
		}
		switch {
		case strings.HasPrefix(mimeType, "image/"):
			log.Debug().
				Str("conversation_id", conv.ID).
				Str("mime_type", mimeType).
				Msg("Routing outbound Matrix file as image")
			return tc.handleMatrixImageMessage(ctx, msg, conv, &content, reply)
		case strings.HasPrefix(mimeType, "video/"):
			log.Debug().
				Str("conversation_id", conv.ID).
				Str("mime_type", mimeType).
				Msg("Routing outbound Matrix file as video")
			return tc.handleMatrixVideoMessage(ctx, msg, conv, &content, reply)
		case mimeType == "":
			log.Debug().
				Str("conversation_id", conv.ID).
				Msg("Outbound Matrix file has no MIME type; trying image then video handlers")
			if resp, err := tc.handleMatrixImageMessage(ctx, msg, conv, &content, reply); err == nil {
				return resp, nil
			} else if !errors.Is(err, bridgev2.ErrUnsupportedMessageType) {
				return nil, err
			}
			log.Debug().
				Str("conversation_id", conv.ID).
				Msg("Image handler rejected MIME-less file; trying video handler")
			return tc.handleMatrixVideoMessage(ctx, msg, conv, &content, reply)
		default:
			return nil, bridgev2.ErrUnsupportedMessageType
		}
	}

	text, err := matrixToTikTok(&content)
	if err != nil {
		return nil, err
	}

	resp, err := tc.apiClient.SendMessage(ctx, libtiktok.SendMessageParams{
		ConvID:       conv.ID,
		ConvSourceID: conv.SourceID,
		Text:         text,
		IsGroup:      conv.ConversationType == 2,
		Reply:        reply,
	})
	if err != nil {
		return nil, fmt.Errorf("send TikTok message: %w", err)
	}
	log.Debug().
		Str("conversation_id", conv.ID).
		Str("message_id", resp.MessageID).
		Msg("Sent outbound TikTok text message")

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       networkid.MessageID(resp.MessageID),
			SenderID: makeUserID(tc.meta.UserID),
		},
	}, nil
}

func (tc *TikTokClient) handleMatrixImageMessage(
	ctx context.Context,
	_ *bridgev2.MatrixMessage,
	conv *libtiktok.Conversation,
	content *event.MessageEventContent,
	reply *libtiktok.OutgoingMessageReply,
) (*bridgev2.MatrixMessageResponse, error) {
	log := zerolog.Ctx(ctx)
	if strings.TrimSpace(content.GetCaption()) != "" {
		return nil, fmt.Errorf("image captions are not yet supported on TikTok")
	}

	matrix := tc.userLogin.Bridge.Matrix.BotIntent()
	data, err := matrix.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}

	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	log.Debug().
		Str("conversation_id", conv.ID).
		Str("mime_type", mimeType).
		Int("bytes", len(data)).
		Bool("has_reply", reply != nil).
		Msg("Prepared outbound image payload for TikTok")

	resp, err := tc.apiClient.SendMessage(ctx, libtiktok.SendMessageParams{
		ConvID:       conv.ID,
		ConvSourceID: conv.SourceID,
		IsGroup:      conv.ConversationType == 2,
		Reply:        reply,
		Image: &libtiktok.OutgoingImage{
			Data:     data,
			FileName: content.GetFileName(),
			MimeType: mimeType,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("send TikTok image: %w", err)
	}
	log.Debug().
		Str("conversation_id", conv.ID).
		Str("message_id", resp.MessageID).
		Msg("Sent outbound TikTok image message")

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       networkid.MessageID(resp.MessageID),
			SenderID: makeUserID(tc.meta.UserID),
		},
	}, nil
}

func (tc *TikTokClient) handleMatrixVideoMessage(
	ctx context.Context,
	_ *bridgev2.MatrixMessage,
	conv *libtiktok.Conversation,
	content *event.MessageEventContent,
	reply *libtiktok.OutgoingMessageReply,
) (*bridgev2.MatrixMessageResponse, error) {
	log := zerolog.Ctx(ctx)
	if strings.TrimSpace(content.GetCaption()) != "" {
		return nil, fmt.Errorf("video captions are not yet supported on TikTok")
	}

	matrix := tc.userLogin.Bridge.Matrix.BotIntent()
	data, err := matrix.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}

	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "video/") {
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	log.Debug().
		Str("conversation_id", conv.ID).
		Str("mime_type", mimeType).
		Int("bytes", len(data)).
		Bool("has_reply", reply != nil).
		Msg("Prepared outbound video payload for TikTok")

	resp, err := tc.apiClient.SendMessage(ctx, libtiktok.SendMessageParams{
		ConvID:       conv.ID,
		ConvSourceID: conv.SourceID,
		IsGroup:      conv.ConversationType == 2,
		Reply:        reply,
		Video: &libtiktok.OutgoingVideo{
			Data:     data,
			FileName: content.GetFileName(),
			MimeType: mimeType,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("send TikTok video: %w", err)
	}
	log.Debug().
		Str("conversation_id", conv.ID).
		Str("message_id", resp.MessageID).
		Msg("Sent outbound TikTok video message")

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       networkid.MessageID(resp.MessageID),
			SenderID: makeUserID(tc.meta.UserID),
		},
	}, nil
}

func (tc *TikTokClient) buildOutgoingReply(log zerolog.Logger, msg *bridgev2.MatrixMessage) *libtiktok.OutgoingMessageReply {
	if msg.ReplyTo == nil {
		return nil
	}
	if msg.ReplyTo.Room != msg.Portal.PortalKey {
		log.Debug().Msg("Matrix reply target is in another portal; sending without TikTok reply envelope")
		return nil
	}

	parentID, perr := strconv.ParseUint(string(msg.ReplyTo.ID), 10, 64)
	if perr != nil {
		log.Debug().Err(perr).Str("parent_id", string(msg.ReplyTo.ID)).
			Msg("Matrix reply target is not a TikTok server message id; sending without TikTok reply envelope")
		return nil
	}
	if parentID == 0 {
		log.Debug().Str("parent_id", string(msg.ReplyTo.ID)).
			Msg("Matrix reply target has zero id; sending without TikTok reply envelope")
		return nil
	}

	var pm *MessageMetadata
	if raw, ok := msg.ReplyTo.Metadata.(*MessageMetadata); ok {
		pm = raw
	} else if msg.ReplyTo.Metadata != nil {
		log.Debug().
			Str("reply_metadata_type", fmt.Sprintf("%T", msg.ReplyTo.Metadata)).
			Msg("Matrix reply target metadata is not TikTok message metadata; using fallback reply fields")
	}
	refUID := string(msg.ReplyTo.SenderID)
	refSec := ""
	chainID := uint64(0)
	cursorUs := uint64(0)
	contentJSON := ""
	if pm != nil {
		refSec = pm.SenderSecUID
		chainID = pm.SendChainID
		cursorUs = pm.CursorTsUs
		contentJSON = pm.ContentJSON
	}
	if cursorUs == 0 {
		cursorUs = uint64(msg.ReplyTo.Timestamp.UnixMicro())
		log.Debug().
			Uint64("parent_id", parentID).
			Uint64("cursor_ts_us", cursorUs).
			Msg("Matrix reply missing TikTok cursor; using Matrix timestamp fallback")
	}

	refBytes, err := libtiktok.BuildReplyReferenceJSON(contentJSON, refUID, refSec)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to build TikTok reply reference JSON; sending as non-reply")
		return nil
	}
	log.Debug().
		Uint64("parent_id", parentID).
		Bool("has_sender_sec_uid", refSec != "").
		Bool("has_content_json", contentJSON != "").
		Msg("Built TikTok reply envelope from Matrix reply target")
	return &libtiktok.OutgoingMessageReply{
		ParentServerMessageID: parentID,
		ParentSendChainID:     chainID,
		ParentCursorTsUs:      cursorUs,
		ReferencePayloadJSON:  refBytes,
	}
}

// HandleMatrixMessageRemove recalls the message on TikTok (delete for everyone)
// when the redacted Matrix event corresponds to a message we sent. Other
// messages are left untouched on TikTok (Matrix redaction still completes on
// the bridge side).
func (tc *TikTokClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if msg.TargetMessage == nil {
		return fmt.Errorf("nil redaction target message")
	}
	if msg.TargetMessage.SenderID != makeUserID(tc.meta.UserID) {
		zerolog.Ctx(ctx).Debug().
			Str("target_sender", string(msg.TargetMessage.SenderID)).
			Msg("Skipping TikTok delete: redacted message was not sent by this login")
		return nil
	}

	serverMessageID, err := strconv.ParseUint(string(msg.TargetMessage.ID), 10, 64)
	if err != nil {
		return fmt.Errorf("cannot delete on TikTok: bridged message id %q is not a numeric server message id: %w", msg.TargetMessage.ID, err)
	}

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return err
	}

	err = tc.apiClient.RecallMessage(ctx, libtiktok.DeleteMessageParams{
		ConvID:          conv.ID,
		ConvoSourceID:   conv.SourceID,
		ServerMessageID: serverMessageID,
	})
	if err != nil {
		return fmt.Errorf("recall TikTok message: %w", err)
	}
	return nil
}

// PreHandleMatrixReaction extracts the Matrix reaction key and maps it to the
// current TikTok login so bridgev2 can deduplicate outgoing reactions.
func (tc *TikTokClient) PreHandleMatrixReaction(_ context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	emoji := msg.Content.RelatesTo.GetAnnotationKey()
	if emoji == "" {
		return bridgev2.MatrixReactionPreResponse{}, fmt.Errorf("missing Matrix reaction annotation key")
	}

	return bridgev2.MatrixReactionPreResponse{
		SenderID: makeUserID(tc.meta.UserID),
		EmojiID:  networkid.EmojiID(emoji),
		Emoji:    emoji,
	}, nil
}

// HandleMatrixReaction forwards a Matrix reaction to TikTok using the existing
// add-reaction API.
func (tc *TikTokClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	serverMessageID, err := strconv.ParseUint(string(msg.TargetMessage.ID), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse TikTok message ID %q: %w", msg.TargetMessage.ID, err)
	}

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return nil, err
	}

	emoji := msg.PreHandleResp.Emoji
	if emoji == "" {
		emoji = msg.Content.RelatesTo.GetAnnotationKey()
	}

	err = tc.apiClient.SendReaction(ctx, libtiktok.SendReactionParams{
		ConvID:          conv.ID,
		IsGroup:         conv.ConversationType == 2,
		Emoji:           emoji,
		Action:          libtiktok.ReactionAdd,
		SelfUserID:      tc.meta.UserID,
		ConvoSourceID:   conv.SourceID,
		ServerMessageID: serverMessageID,
	})
	if err != nil {
		return nil, fmt.Errorf("send TikTok reaction: %w", err)
	}

	return &database.Reaction{
		SenderID: makeUserID(tc.meta.UserID),
		EmojiID:  networkid.EmojiID(emoji),
		Emoji:    emoji,
	}, nil
}

// HandleMatrixReactionRemove removes a previously bridged reaction from TikTok.
func (tc *TikTokClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	serverMessageID, err := strconv.ParseUint(string(msg.TargetReaction.MessageID), 10, 64)
	if err != nil {
		return fmt.Errorf("parse TikTok message ID %q: %w", msg.TargetReaction.MessageID, err)
	}

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return err
	}

	emoji := msg.TargetReaction.Emoji
	if emoji == "" {
		emoji = string(msg.TargetReaction.EmojiID)
	}
	if emoji == "" {
		return fmt.Errorf("reaction %s has no TikTok emoji key", msg.TargetReaction.MXID)
	}

	err = tc.apiClient.SendReaction(ctx, libtiktok.SendReactionParams{
		ConvID:          conv.ID,
		IsGroup:         conv.ConversationType == 2,
		Emoji:           emoji,
		Action:          libtiktok.ReactionRemove,
		SelfUserID:      tc.meta.UserID,
		ConvoSourceID:   conv.SourceID,
		ServerMessageID: serverMessageID,
	})
	if err != nil {
		return fmt.Errorf("remove TikTok reaction: %w", err)
	}
	return nil
}
