package libtiktok

import (
	"encoding/json"
	"testing"
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
