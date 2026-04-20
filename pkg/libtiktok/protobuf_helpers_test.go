package libtiktok

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func mustMarshalProto(t *testing.T, msg proto.Message) []byte {
	t.Helper()

	data, err := marshalProto(msg)
	if err != nil {
		t.Fatalf("marshal protobuf: %v", err)
	}
	return data
}
