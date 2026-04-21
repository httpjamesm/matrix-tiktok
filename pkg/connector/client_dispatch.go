package connector

import (
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

// dispatchMessage queues a single TikTok message into the bridgev2 pipeline,
// followed immediately by a ReactionSync event when the message carries reactions.
func (tc *TikTokClient) dispatchMessage(conv *libtiktok.Conversation, msg libtiktok.Message) {
	log := tc.userLogin.Log.With().
		Str("component", "connector-dispatch").
		Str("conversation_id", conv.ID).
		Uint64("message_id", msg.ServerID).
		Logger()
	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.Message[libtiktok.Message]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", conv.ID).
					Uint64("message_id", msg.ServerID).
					Str("sender_id", msg.SenderID)
			},
			PortalKey: networkid.PortalKey{
				ID:       makePortalID(conv.ID),
				Receiver: tc.userLogin.ID,
			},
			CreatePortal: true,
			Sender: bridgev2.EventSender{
				IsFromMe: msg.SenderID == tc.meta.UserID,
				Sender:   makeUserID(msg.SenderID),
			},
			Timestamp: time.UnixMilli(msg.TimestampMs),
		},
		ID:                 networkid.MessageID(strconv.FormatUint(msg.ServerID, 10)),
		Data:               msg,
		ConvertMessageFunc: tc.convertMessage,
	})
	tc.mu.Lock()
	if msg.TimestampMs > tc.lastSeen[conv.ID] {
		tc.lastSeen[conv.ID] = msg.TimestampMs
	}
	tc.mu.Unlock()
	log.Debug().
		Str("sender_id", msg.SenderID).
		Str("message_type", msg.Type).
		Msg("Queued remote message event")
	tc.dispatchReactions(conv, msg)
}

// dispatchReactions queues a ReactionSync event for all reactions on a message.
//
// QueueRemoteEvent processes events FIFO per portal, so queuing this immediately
// after the parent message guarantees the bridge has already stored the message
// by the time handleRemoteReactionSync looks it up by ID.
//
// The wire gives us reactions indexed as emoji → []userID, but ReactionSyncData
// wants the inverse: userID → []BackfillReaction. We pivot here.
func (tc *TikTokClient) dispatchReactions(conv *libtiktok.Conversation, msg libtiktok.Message) {
	if len(msg.Reactions) == 0 {
		return
	}
	log := tc.userLogin.Log.With().
		Str("component", "connector-dispatch").
		Str("conversation_id", conv.ID).
		Uint64("message_id", msg.ServerID).
		Logger()

	users := make(map[networkid.UserID]*bridgev2.ReactionSyncUser, len(msg.Reactions))
	for _, r := range msg.Reactions {
		emojiID := networkid.EmojiID(r.Emoji)
		for _, uid := range r.UserIDs {
			userID := makeUserID(uid)
			if users[userID] == nil {
				users[userID] = &bridgev2.ReactionSyncUser{HasAllReactions: true}
			}
			users[userID].Reactions = append(users[userID].Reactions, &bridgev2.BackfillReaction{
				Sender: bridgev2.EventSender{
					IsFromMe: uid == tc.meta.UserID,
					Sender:   userID,
				},
				EmojiID: emojiID,
				Emoji:   r.Emoji,
			})
		}
	}

	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventReactionSync,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", conv.ID).
					Uint64("message_id", msg.ServerID).
					Int("reaction_count", len(msg.Reactions))
			},
			PortalKey: networkid.PortalKey{
				ID:       makePortalID(conv.ID),
				Receiver: tc.userLogin.ID,
			},
			Sender: bridgev2.EventSender{
				IsFromMe: true,
				Sender:   makeUserID(tc.meta.UserID),
			},
			Timestamp:   time.UnixMilli(msg.TimestampMs),
			StreamOrder: 1,
		},
		TargetMessage: networkid.MessageID(strconv.FormatUint(msg.ServerID, 10)),
		Reactions: &bridgev2.ReactionSyncData{
			Users:       users,
			HasAllUsers: true,
		},
	})
	log.Debug().Int("reaction_count", len(msg.Reactions)).Msg("Queued reaction sync event")
}

// dispatchWSReaction queues individual RemoteEventReaction / RemoteEventReactionRemove
// events for each modification in a WebSocket property-update (type 705) event.
func (tc *TikTokClient) dispatchWSReaction(evt *libtiktok.WSReactionEvent) {
	log := tc.userLogin.Log.With().
		Str("component", "reaction-dispatch").
		Str("conversation_id", evt.ConversationID).
		Uint64("server_message_id", evt.ServerMessageID).
		Logger()

	msgID := networkid.MessageID(strconv.FormatUint(evt.ServerMessageID, 10))
	senderUID := evt.SenderUserID
	if senderUID == "" {
		senderUID = tc.meta.UserID
	}

	for _, mod := range evt.Modifications {
		evtType := bridgev2.RemoteEventReaction
		if mod.Op == 1 {
			evtType = bridgev2.RemoteEventReactionRemove
		}

		log.Debug().
			Str("emoji", mod.Emoji).
			Int("op", mod.Op).
			Str("sender_id", senderUID).
			Str("target_message_id", string(msgID)).
			Str("portal_id", string(makePortalID(evt.ConversationID))).
			Str("event_type", evtType.String()).
			Msg("Queuing remote reaction event")

		tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.Reaction{
			EventMeta: simplevent.EventMeta{
				Type: evtType,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.
						Str("conversation_id", evt.ConversationID).
						Uint64("message_id", evt.ServerMessageID).
						Str("emoji", mod.Emoji).
						Str("sender_id", senderUID).
						Int("op", mod.Op)
				},
				PortalKey: networkid.PortalKey{
					ID:       makePortalID(evt.ConversationID),
					Receiver: tc.userLogin.ID,
				},
				Sender: bridgev2.EventSender{
					IsFromMe: senderUID == tc.meta.UserID,
					Sender:   makeUserID(senderUID),
				},
				Timestamp: time.Now(),
			},
			TargetMessage: msgID,
			EmojiID:       networkid.EmojiID(mod.Emoji),
			Emoji:         mod.Emoji,
		})
		log.Debug().
			Str("emoji", mod.Emoji).
			Int("op", mod.Op).
			Str("sender_id", senderUID).
			Str("target_message_id", string(msgID)).
			Str("event_type", evtType.String()).
			Msg("Queued remote reaction event")
	}
}

// dispatchWSMessageDeletion redacts the bridged Matrix event when a message is
// removed on TikTok, either as a local hide/delete-for-self or a global recall.
func (tc *TikTokClient) dispatchWSMessageDeletion(d *libtiktok.WSMessageDeletion) {
	log := tc.userLogin.Log.With().
		Str("component", "connector-dispatch").
		Str("conversation_id", d.ConversationID).
		Uint64("message_id", d.DeletedMessageID).
		Logger()
	deleterUID := d.DeleterUserID
	if deleterUID == "" {
		deleterUID = tc.meta.UserID
	}
	msgID := networkid.MessageID(strconv.FormatUint(d.DeletedMessageID, 10))
	ts := time.UnixMilli(d.TimestampMs)
	if d.TimestampMs == 0 {
		ts = time.Now()
	}
	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.MessageRemove{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessageRemove,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", d.ConversationID).
					Uint64("deleted_message_id", d.DeletedMessageID).
					Str("deleter_user_id", deleterUID)
			},
			PortalKey: networkid.PortalKey{
				ID:       makePortalID(d.ConversationID),
				Receiver: tc.userLogin.ID,
			},
			Sender: bridgev2.EventSender{
				IsFromMe: deleterUID == tc.meta.UserID,
				Sender:   makeUserID(deleterUID),
			},
			Timestamp: ts,
		},
		TargetMessage: msgID,
		OnlyForMe:     d.OnlyForMe,
	})
	log.Debug().
		Str("sender_id", deleterUID).
		Bool("only_for_me", d.OnlyForMe).
		Msg("Queued remote message deletion event")
}

// dispatchWSReadReceipt bridges a TikTok read receipt to Matrix when the wire
// includes a non-zero peer_or_inbox_id (candidate reader). Otherwise receipts
// are skipped to avoid attributing reads to the wrong user.
func (tc *TikTokClient) dispatchWSReadReceipt(rr *libtiktok.WSReadReceipt) {
	log := tc.userLogin.Log.With().
		Str("component", "connector-dispatch").
		Str("conversation_id", rr.ConversationID).
		Uint64("read_server_message_id", rr.ReadServerMessageID).
		Logger()

	if rr.ReaderUserID == "" {
		log.Debug().Msg("Skipping read receipt: no reader user id (peer_or_inbox_id)")
		return
	}

	msgID := networkid.MessageID(strconv.FormatUint(rr.ReadServerMessageID, 10))
	readUpTo := time.Now()
	if rr.ReadTimestampUs > 0 {
		readUpTo = time.UnixMicro(int64(rr.ReadTimestampUs))
	}
	readerUID := rr.ReaderUserID

	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventReadReceipt,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", rr.ConversationID).
					Str("reader_user_id", readerUID).
					Uint64("message_id", rr.ReadServerMessageID)
			},
			PortalKey: networkid.PortalKey{
				ID:       makePortalID(rr.ConversationID),
				Receiver: tc.userLogin.ID,
			},
			Sender: bridgev2.EventSender{
				IsFromMe: readerUID == tc.meta.UserID,
				Sender:   makeUserID(readerUID),
			},
			Timestamp: readUpTo,
		},
		LastTarget: msgID,
		Targets:    []networkid.MessageID{msgID},
		ReadUpTo:   readUpTo,
	})
	log.Debug().Str("reader_id", readerUID).Msg("Queued remote read receipt")
}
