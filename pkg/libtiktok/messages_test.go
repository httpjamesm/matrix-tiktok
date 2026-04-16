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
	payload := buildSendPayload(
		"0:1:alice:bob",
		"",
		"device-id",
		"ms-token",
		"verify-fp",
		"public-key",
		"client-message-id",
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
	payload := buildSendPayload(
		"0:1:alice:bob",
		"",
		"device-id",
		"ms-token",
		"verify-fp",
		"public-key",
		"client-message-id",
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

func TestParseSendResponseStringMessageID(t *testing.T) {
	resp := mustMarshalProto(&tiktokpb.SendResponse{
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
