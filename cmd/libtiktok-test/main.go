package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

func main() {
	cookie := flag.String("cookie", "", "Full Cookie header string from an authenticated TikTok browser session")
	flag.Parse()

	if *cookie == "" {
		fmt.Fprintln(os.Stderr, "error: -cookie is required")
		fmt.Fprintln(os.Stderr, "Usage: libtiktok-test -cookie '<your cookie string>'")
		os.Exit(1)
	}

	ctx := context.Background()
	client := libtiktok.NewClient(*cookie)

	// ── GetSelf ──────────────────────────────────────────────────────────────
	fmt.Println("=== GetSelf ===")
	self, err := client.GetSelf(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetSelf error: %v\n", err)
	} else {
		fmt.Printf("  User ID      : %s\n", self.UserID)
		fmt.Printf("  Unique ID    : @%s\n", self.UniqueID)
		fmt.Printf("  Nickname     : %s\n", self.Nickname)
		fmt.Printf("  Avatar URL   : %s\n", self.AvatarURL)
	}
	fmt.Println()

	// ── GetInbox ─────────────────────────────────────────────────────────────
	fmt.Println("=== GetInbox ===")
	convs, err := client.GetInbox(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetInbox error: %v\n", err)
		os.Exit(1)
	}

	if len(convs) == 0 {
		fmt.Println("  (no conversations found)")
		return
	}

	fmt.Printf("  Found %d conversation(s):\n\n", len(convs))
	for i, conv := range convs {
		fmt.Printf("  ── Conversation #%d ──\n", i+1)
		fmt.Printf("    Conv ID      : %s\n", conv.ID)
		if len(conv.Participants) > 0 {
			fmt.Printf("    Participants : %v\n", conv.Participants)
		}
		fmt.Println()
	}
}
