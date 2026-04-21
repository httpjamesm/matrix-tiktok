package libtiktok

import (
	"encoding/json"
	"testing"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestBuildReplyReferenceJSON(t *testing.T) {
	raw, err := BuildReplyReferenceJSON(`{"aweType":0,"text":"hello"}`, "123456789", "MS4wTestSecUid")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["refmsg_uid"] != "123456789" {
		t.Fatalf("refmsg_uid = %v", m["refmsg_uid"])
	}
	if m["refmsg_sec_uid"] != "MS4wTestSecUid" {
		t.Fatalf("refmsg_sec_uid = %v", m["refmsg_sec_uid"])
	}
	if m["content"] != `{"aweType":0,"text":"hello"}` {
		t.Fatalf("content = %q", m["content"])
	}
	if m["refmsg_content"] != m["content"] {
		t.Fatal("refmsg_content should match content")
	}
	if int(m["refmsg_type"].(float64)) != 7 {
		t.Fatalf("refmsg_type = %v", m["refmsg_type"])
	}
}

func TestBuildSendPayloadPrivateImage(t *testing.T) {
	payload, err := buildSendPayload(
		"0:1:alice:bob",
		0,
		"",
		"device-id",
		"ms-token",
		"verify-fp",
		"public-key",
		"client-message-id",
		false,
		nil,
		&uploadedPrivateImage{
			URI:        "tos-alisg-i/example/image",
			DecryptKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Width:      1518,
			Height:     1140,
			Size:       211515,
			FileName:   "example.png",
		},
		nil,
	)

	if err != nil {
		t.Fatalf("buildSendPayload: %v", err)
	}
	var req tiktokpb.SendRequest
	if err := unmarshalProto(payload, &req); err != nil {
		t.Fatalf("unmarshal send payload: %v", err)
	}

	send := req.GetPayload().GetSend()
	if send.GetMessageSubtype() != "private_image" {
		t.Fatalf("message_subtype = %q", send.GetMessageSubtype())
	}
	if send.GetReserved_6() != 1802 {
		t.Fatalf("reserved_6 = %d", send.GetReserved_6())
	}
	if got := send.GetPrivateImage().GetPath(); got != "tos-alisg-i/example/image" {
		t.Fatalf("private_image.path = %q", got)
	}
	if got := send.GetPrivateImage().GetDecryptKey(); got == "" {
		t.Fatal("private_image.decrypt_key should be present")
	}
	if got := send.GetAttachment().GetPrivateImage().GetAssets(); len(got) != 1 {
		t.Fatalf("attachment.private_image.assets len = %d", len(got))
	}
	if got := send.GetAttachment().GetPrivateImage().GetMetadata().GetEntries(); len(got) != 1 {
		t.Fatalf("attachment.private_image.metadata len = %d", len(got))
	}
}

func TestBuildSendPayloadPrivateVideo(t *testing.T) {
	payload, err := buildSendPayload(
		"0:1:alice:bob",
		0,
		"",
		"device-id",
		"ms-token",
		"verify-fp",
		"public-key",
		"client-message-id",
		false,
		nil,
		nil,
		&uploadedPrivateVideo{
			Vid:        "vid123",
			PosterURI:  "tos-alisg-p-50234-sg/poster",
			Width:      1360,
			Height:     1200,
			DurationMs: 14100,
			Size:       1780826,
			FileName:   "output.mp4",
			Codec:      "h264",
		},
	)

	if err != nil {
		t.Fatalf("buildSendPayload: %v", err)
	}
	var req tiktokpb.SendRequest
	if err := unmarshalProto(payload, &req); err != nil {
		t.Fatalf("unmarshal send payload: %v", err)
	}

	send := req.GetPayload().GetSend()
	if send.GetMessageSubtype() != "private_video" {
		t.Fatalf("message_subtype = %q", send.GetMessageSubtype())
	}
	if send.GetReserved_6() != 1803 {
		t.Fatalf("reserved_6 = %d", send.GetReserved_6())
	}
	if send.GetPrivateImage().GetReserved_1() != 2 {
		t.Fatalf("summary.reserved_1 = %d", send.GetPrivateImage().GetReserved_1())
	}
	if got := send.GetPrivateImage().GetPath(); got != "vid123" {
		t.Fatalf("summary.path = %q", got)
	}
	if send.GetPrivateImage().GetDecryptKey() != "" {
		t.Fatal("summary.decrypt_key should be empty for video")
	}
	fi := send.GetPrivateImage().GetFileInfo()
	if fi.GetReserved_3() != 14100 || fi.GetSize() != 1780826 || fi.GetVideoCodec() != "h264" {
		t.Fatalf("file_info = (%d,%d,%q)", fi.GetReserved_3(), fi.GetSize(), fi.GetVideoCodec())
	}
	pv := send.GetAttachment().GetPrivateVideo()
	if pv.GetPrimary().GetVid() != "vid123" {
		t.Fatalf("attachment.private_video.primary.vid = %q", pv.GetPrimary().GetVid())
	}
	if pv.GetPrimary().GetPoster().GetUri() != "tos-alisg-p-50234-sg/poster" {
		t.Fatalf("poster uri = %q", pv.GetPrimary().GetPoster().GetUri())
	}
	if len(pv.GetMetadata().GetEntries()) != 1 || pv.GetMetadata().GetEntries()[0].GetInner().GetVid() != "vid123" {
		t.Fatal("metadata vid mismatch")
	}
}

func TestBuildSendPayloadMessageKind(t *testing.T) {
	// DM: isGroup=false → message_kind should be 1
	dmPayload, err := buildSendPayload(
		"0:1:alice:bob",
		0,
		"hello",
		"device-id",
		"ms-token",
		"verify-fp",
		"public-key",
		"client-message-id",
		false,
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("buildSendPayload DM: %v", err)
	}
	var dmReq tiktokpb.SendRequest
	if err := unmarshalProto(dmPayload, &dmReq); err != nil {
		t.Fatalf("unmarshal DM payload: %v", err)
	}
	if dmReq.GetPayload().GetSend().GetMessageKind() != 1 {
		t.Fatalf("DM message_kind = %d, want 1", dmReq.GetPayload().GetSend().GetMessageKind())
	}

	// Group: isGroup=true → message_kind should be 2
	groupPayload, err := buildSendPayload(
		"7587998693467750664",
		0,
		"hello group",
		"device-id",
		"ms-token",
		"verify-fp",
		"public-key",
		"client-message-id",
		true,
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("buildSendPayload group: %v", err)
	}
	var groupReq tiktokpb.SendRequest
	if err := unmarshalProto(groupPayload, &groupReq); err != nil {
		t.Fatalf("unmarshal group payload: %v", err)
	}
	if groupReq.GetPayload().GetSend().GetMessageKind() != 2 {
		t.Fatalf("group message_kind = %d, want 2", groupReq.GetPayload().GetSend().GetMessageKind())
	}
}

func TestParseSendResponseStringMessageID(t *testing.T) {
	resp := mustMarshalProto(t, &tiktokpb.SendResponse{
		Primary: &tiktokpb.SendResponsePayload{
			Send: &tiktokpb.SendResponseBody{
				MessageId: protoString("0471405060961842560"),
			},
		},
	})
	id, err := parseSendResponse(resp)
	if err != nil {
		t.Fatalf("parseSendResponse: %v", err)
	}
	if id != "0471405060961842560" {
		t.Fatalf("message ID = %q", id)
	}
}

func TestParseSendResponseVarintMessageID(t *testing.T) {
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.VarintType)
	inner = protowire.AppendVarint(inner, 9331376168411224064)
	inner = protowire.AppendTag(inner, 4, protowire.BytesType)
	inner = protowire.AppendString(inner, "8ca3f601-7de6-49d9-83a7-185bf61a9b04")

	var payload []byte
	payload = protowire.AppendTag(payload, 100, protowire.BytesType)
	payload = protowire.AppendBytes(payload, inner)

	var resp []byte
	resp = protowire.AppendTag(resp, 6, protowire.BytesType)
	resp = protowire.AppendBytes(resp, payload)

	id, err := parseSendResponse(resp)
	if err != nil {
		t.Fatalf("parseSendResponse: %v", err)
	}
	if id != "9331376168411224064" {
		t.Fatalf("message ID = %q", id)
	}
}

func TestBuildInputStatusPayload(t *testing.T) {
	fakeConvID := "0:1:7000000000000000001:7000000000000000002"
	fakeSourceID := uint64(7000000000000000003)
	fakeDeviceID := "fake-device-id-1234567890abcdef"

	payload, err := buildInputStatusPayload(
		SendTypingParams{
			ConvID:       fakeConvID,
			ConvSourceID: fakeSourceID,
		},
		fakeDeviceID,
		"ms-token",
		"verify-fp",
	)
	if err != nil {
		t.Fatalf("buildInputStatusPayload: %v", err)
	}

	var req tiktokpb.InputStatusRequest
	if err := unmarshalProto(payload, &req); err != nil {
		t.Fatalf("unmarshal input_status payload: %v", err)
	}

	if req.GetMessageType() != 411 {
		t.Fatalf("message_type = %d", req.GetMessageType())
	}
	if req.GetSubCommand() != 10100 {
		t.Fatalf("sub_command = %d", req.GetSubCommand())
	}
	if req.GetClientVersion() != "1.6.3" {
		t.Fatalf("client_version = %q", req.GetClientVersion())
	}
	if req.GetPlatformFlag() != 3 {
		t.Fatalf("platform_flag = %d", req.GetPlatformFlag())
	}
	if req.GetReserved_6() != 0 {
		t.Fatalf("reserved_6 = %d", req.GetReserved_6())
	}
	if req.GetReserved_7() == nil {
		t.Fatal("reserved_7 should be present")
	}
	if req.GetDeviceId() != fakeDeviceID {
		t.Fatalf("device_id = %q", req.GetDeviceId())
	}
	if req.GetClientPlatform() != "web" {
		t.Fatalf("client_platform = %q", req.GetClientPlatform())
	}
	if req.GetFinalFlag() != 1 {
		t.Fatalf("final_flag = %d", req.GetFinalFlag())
	}

	body := req.GetPayload().GetInputStatus()
	if body.GetConversationId() != fakeConvID {
		t.Fatalf("conversation_id = %q", body.GetConversationId())
	}
	if body.GetTypingStatus() != 1 {
		t.Fatalf("typing_status = %d", body.GetTypingStatus())
	}
	if body.GetSourceId() != fakeSourceID {
		t.Fatalf("source_id = %d", body.GetSourceId())
	}
	if body.GetReserved_4() != 3 {
		t.Fatalf("reserved_4 = %d", body.GetReserved_4())
	}

	keys := make([]string, 0, len(req.GetMetadata()))
	values := make(map[string]string, len(req.GetMetadata()))
	for _, entry := range req.GetMetadata() {
		keys = append(keys, entry.GetKey())
		values[entry.GetKey()] = entry.GetValue()
	}
	if len(keys) < 4 {
		t.Fatalf("metadata len = %d", len(keys))
	}
	if keys[0] != "aid" || keys[1] != "app_name" || keys[2] != "channel" || keys[3] != "device_platform" {
		t.Fatalf("metadata prefix = %v", keys[:4])
	}
	if got := values["referer"]; got != "https://www.tiktok.com/messages?lang=en" {
		t.Fatalf("referer = %q", got)
	}
	if got := values["screen_width"]; got != "1512" {
		t.Fatalf("screen_width = %q", got)
	}
	if got := values["screen_height"]; got != "982" {
		t.Fatalf("screen_height = %q", got)
	}
	if got := values["verifyFp"]; got != "verify-fp" {
		t.Fatalf("verifyFp = %q", got)
	}
	if got := values["Web-Sdk-Ms-Token"]; got != "ms-token" {
		t.Fatalf("Web-Sdk-Ms-Token = %q", got)
	}
	last := keys[len(keys)-3:]
	if last[0] != "verifyFp" || last[1] != "Web-Sdk-Ms-Token" || last[2] != "browser_version" {
		t.Fatalf("metadata suffix = %v", last)
	}
}

func TestBuildMarkConversationReadPayload(t *testing.T) {
	fakeConvID := "0:1:7000000000000000001:7000000000000000002"
	fakeSourceID := uint64(7630978718240522503)
	fakeDeviceID := "e3f69a57c34ecffc9e4e92466f43b6c7"
	readIndex := uint64(1776744035049552)

	payload, err := buildMarkConversationReadPayload(
		MarkConversationReadParams{
			ConvID:           fakeConvID,
			ConvSourceID:     fakeSourceID,
			ConversationType: 1,
			ReadMessageIndex: readIndex,
		},
		fakeDeviceID,
		"ms-token",
		"verify-fp",
	)
	if err != nil {
		t.Fatalf("buildMarkConversationReadPayload: %v", err)
	}

	var req tiktokpb.MarkConversationReadRequest
	if err := unmarshalProto(payload, &req); err != nil {
		t.Fatalf("unmarshal mark_read payload: %v", err)
	}

	if req.GetMessageType() != 604 {
		t.Fatalf("message_type = %d", req.GetMessageType())
	}
	if req.GetSubCommand() != 1 {
		t.Fatalf("sub_command = %d", req.GetSubCommand())
	}
	if req.GetClientVersion() != "1.6.0" {
		t.Fatalf("client_version = %q", req.GetClientVersion())
	}
	if req.GetPlatformFlag() != 3 {
		t.Fatalf("platform_flag = %d", req.GetPlatformFlag())
	}
	if req.GetReserved_6() != 0 {
		t.Fatalf("reserved_6 = %d", req.GetReserved_6())
	}
	if req.GetGitHash() != "" {
		t.Fatalf("git_hash = %q", req.GetGitHash())
	}
	if req.GetDeviceId() != fakeDeviceID {
		t.Fatalf("device_id = %q", req.GetDeviceId())
	}
	if req.GetClientPlatform() != "web" {
		t.Fatalf("client_platform = %q", req.GetClientPlatform())
	}
	if req.GetFinalFlag() != 1 {
		t.Fatalf("final_flag = %d", req.GetFinalFlag())
	}

	body := req.GetPayload().GetMarkConversationRead()
	if body.GetConversationId() != fakeConvID {
		t.Fatalf("conversation_id = %q", body.GetConversationId())
	}
	if body.GetConversationShortId() != fakeSourceID {
		t.Fatalf("conversation_short_id = %d", body.GetConversationShortId())
	}
	if body.GetConversationType() != 1 {
		t.Fatalf("conversation_type = %d", body.GetConversationType())
	}
	if body.GetReadMessageIndex() != readIndex {
		t.Fatalf("read_message_index = %d", body.GetReadMessageIndex())
	}
	if body.GetConvUnreadCount() != 0 {
		t.Fatalf("conv_unread_count = %d", body.GetConvUnreadCount())
	}
	if body.GetTotalUnreadCount() != 0 {
		t.Fatalf("total_unread_count = %d", body.GetTotalUnreadCount())
	}
}
