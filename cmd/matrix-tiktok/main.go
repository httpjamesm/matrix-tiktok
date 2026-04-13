package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"github.com/httpjamesm/matrix-tiktok/pkg/connector"
)

// These variables are set at build time via -X linker flags in build.sh.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	m := mxmain.BridgeMain{
		Name:        "matrix-tiktok",
		Description: "A Matrix–TikTok DM bridge powered by mautrix-go bridgev2",
		URL:         "https://github.com/httpjamesm/matrix-tiktok",
		Version:     "0.1.0",
		Connector:   &connector.TikTokConnector{},
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
