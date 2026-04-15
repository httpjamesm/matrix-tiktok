package connector

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

func TestConvertMessage_ignoresPlaceholderCompanion(t *testing.T) {
	tc := &TikTokClient{}
	msg := libtiktok.Message{
		Type:           "",
		MessageSubtype: "",
		RawContentJSON: []byte(`{"hack":"1"}`),
	}
	_, err := tc.convertMessage(context.Background(), nil, nil, msg)
	if !errors.Is(err, bridgev2.ErrIgnoringRemoteEvent) {
		t.Fatalf("convertMessage: err = %v, want ErrIgnoringRemoteEvent", err)
	}
}

func TestConvertMessage_doesNotIgnoreFailedPrivateImage(t *testing.T) {
	tc := &TikTokClient{}
	msg := libtiktok.Message{
		Type:           "",
		MessageSubtype: "private_image",
		RawContentJSON: []byte(`{"hack":"1"}`),
	}
	cm, err := tc.convertMessage(context.Background(), nil, nil, msg)
	if err != nil {
		t.Fatalf("convertMessage: %v", err)
	}
	if cm == nil || len(cm.Parts) != 1 {
		t.Fatalf("expected one converted part, got %+v", cm)
	}
}
