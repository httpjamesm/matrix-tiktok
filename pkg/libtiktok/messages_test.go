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
