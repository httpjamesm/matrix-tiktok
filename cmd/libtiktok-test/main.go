package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

// ─── config ──────────────────────────────────────────────────────────────────

type config struct {
	Auth authConfig `toml:"auth"`
}

type authConfig struct {
	Cookie string `toml:"cookie"`
}

func loadConfig() (config, error) {
	var cfg config
	if _, err := toml.DecodeFile("config.toml", &cfg); err != nil {
		return cfg, fmt.Errorf("read config.toml: %w", err)
	}
	if cfg.Auth.Cookie == "" {
		return cfg, fmt.Errorf("config.toml: [auth] cookie is empty")
	}
	return cfg, nil
}

// ─── terminal helpers ────────────────────────────────────────────────────────

func clearScreen() { fmt.Print("\033[H\033[2J") }

func hr() { fmt.Println(strings.Repeat("─", 62)) }

func readLine(r *bufio.Reader, prompt string) string {
	fmt.Print(prompt)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func detectImageMIME(data []byte, fileName string) string {
	mimeType := http.DetectContentType(data)
	if strings.HasPrefix(mimeType, "image/") {
		return mimeType
	}
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return mimeType
	}
}

// ─── formatting ──────────────────────────────────────────────────────────────

func fmtTimestamp(tsMs int64) string {
	if tsMs <= 0 {
		return "??:??:??"
	}
	return time.UnixMilli(tsMs).UTC().Format("2006-01-02 15:04:05")
}

func fmtMessage(m libtiktok.Message, selfID string) string {
	who := m.SenderID
	if who == selfID {
		who = "you"
	}
	// right-pad to keep columns aligned (cap at 20 chars)
	if len(who) > 20 {
		who = who[:17] + "..."
	}

	body := m.Text
	switch m.Type {
	case "video":
		if m.MediaURL != "" {
			body = fmt.Sprintf("[video] %s  <%s>", m.Text, m.MediaURL)
		} else {
			body = "[video]"
		}
	case "image":
		if m.MediaURL != "" {
			body = fmt.Sprintf("[image] <%s>", m.MediaURL)
		} else {
			body = "[image]"
		}
	case "":
		if body == "" {
			body = "(empty)"
		}
	default:
		if body == "" {
			body = fmt.Sprintf("[%s]", m.Type)
		} else {
			body = fmt.Sprintf("[%s] %s", m.Type, body)
		}
	}

	return fmt.Sprintf("  [%s]  %-20s  %s", fmtTimestamp(m.TimestampMs), who, body)
}

// convLabel returns a short human-readable label for a conversation.
// It shows the "other" participant when selfID is known.
func convLabel(conv libtiktok.Conversation, selfID string) string {
	other := ""
	for _, p := range conv.Participants {
		if p != selfID {
			other = p
			break
		}
	}
	if other == "" && len(conv.Participants) > 0 {
		other = conv.Participants[0]
	}
	if other != "" {
		return fmt.Sprintf("with %s", other)
	}
	return conv.ID
}

// ─── inbox screen ────────────────────────────────────────────────────────────

// showInbox renders the conversation list and returns the chosen 0-based index,
// or -1 if the user wants to quit.
func showInbox(r *bufio.Reader, convs []libtiktok.Conversation, selfID string) int {
	for {
		clearScreen()
		fmt.Println("  TikTok Messages — Inbox")
		hr()
		fmt.Println()

		if len(convs) == 0 {
			fmt.Println("  (no conversations)")
			fmt.Println()
			readLine(r, "  Press Enter to exit… ")
			return -1
		}

		for i, conv := range convs {
			label := convLabel(conv, selfID)
			fmt.Printf("  %3d.  %s\n", i+1, label)
			// Show the full conv ID in a slightly dimmer line for reference.
			fmt.Printf("        %s\n\n", conv.ID)
		}

		hr()
		input := readLine(r, "  Select [1-"+strconv.Itoa(len(convs))+"] or [q]uit: ")

		switch strings.ToLower(input) {
		case "q", "quit", "exit":
			return -1
		}

		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > len(convs) {
			fmt.Printf("\n  Invalid selection %q.\n", input)
			readLine(r, "  Press Enter to try again… ")
			continue
		}
		return n - 1
	}
}

// ─── user lookup screen ──────────────────────────────────────────────────────

// lookupUser prompts for a user ID, calls GetUser, and prints the result.
// Returns true to stay in the main menu, false to quit the program.
func lookupUser(ctx context.Context, r *bufio.Reader, client *libtiktok.Client) bool {
	for {
		clearScreen()
		fmt.Println("  TikTok Messages — User Lookup")
		hr()
		fmt.Println()

		userID := readLine(r, "  Enter user ID (empty to go back): ")
		if userID == "" {
			return true
		}

		fmt.Println("\n  Looking up user…")
		user, err := client.GetUser(ctx, userID)
		if err != nil {
			fmt.Printf("\n  Error: %v\n", err)
			readLine(r, "\n  Press Enter to try again… ")
			continue
		}

		fmt.Println()
		hr()
		fmt.Printf("  ID:        %s\n", user.ID)
		fmt.Printf("  UniqueID:  @%s\n", user.UniqueID)
		fmt.Printf("  Nickname:  %s\n", user.Nickname)
		if user.AvatarURL != "" {
			fmt.Printf("  Avatar:    %s\n", user.AvatarURL)
		}
		hr()
		fmt.Println()

		input := strings.ToLower(readLine(r, "  [a]nother lookup   [b]ack to menu   [q]uit: "))
		switch input {
		case "q", "quit", "exit":
			return false
		case "b", "back":
			return true
		}
		// "a" or anything else: loop for another lookup
	}
}

// ─── main menu ───────────────────────────────────────────────────────────────

// showMainMenu renders the top-level action picker and returns "inbox",
// "lookup", "download", "quit", or "" (unrecognised input — caller should loop).
func showMainMenu(r *bufio.Reader) string {
	clearScreen()
	fmt.Println("  TikTok Messages")
	hr()
	fmt.Println()
	fmt.Println("  [i]nbox")
	fmt.Println("  [u]ser lookup")
	fmt.Println("  [d]ownload video")
	fmt.Println("  [q]uit")
	fmt.Println()
	hr()

	switch strings.ToLower(readLine(r, "  > ")) {
	case "i", "inbox":
		return "inbox"
	case "u", "user", "lookup":
		return "lookup"
	case "d", "download":
		return "download"
	case "q", "quit", "exit":
		return "quit"
	}
	return ""
}

// ─── download video screen ───────────────────────────────────────────────────

// downloadVideo prompts for a TikTok video URL, calls DownloadVideo, writes
// the result to disk, and prints verbose debug info at every step so that JSON
// extraction failures can be diagnosed quickly.
// Returns true to stay in the main menu, false to quit the program.
func downloadVideo(ctx context.Context, r *bufio.Reader, client *libtiktok.Client) bool {
	for {
		clearScreen()
		fmt.Println("  TikTok Messages — Download Video")
		hr()
		fmt.Println()

		videoURL := readLine(r, "  Enter TikTok video URL (empty to go back): ")
		if videoURL == "" {
			return true
		}

		fmt.Println()
		fmt.Println("  ── Step 1: downloading video…")
		data, mime, err := client.DownloadVideo(ctx, videoURL)
		if err != nil {
			fmt.Printf("\n  ✗ DownloadVideo failed: %v\n", err)
			fmt.Println()
			fmt.Println("  ── Verbose scope dump for diagnosis ──────────────")
			scope, scopeErr := client.FetchVideoScope(ctx, videoURL)
			if scopeErr != nil {
				fmt.Printf("  ✗ FetchVideoScope failed: %v\n", scopeErr)
			} else {
				dumpVideoScope(scope)
			}
			readLine(r, "\n  Press Enter to try another URL… ")
			continue
		}

		fmt.Printf("  ✓ Downloaded %d bytes  MIME: %s\n", len(data), mime)

		outPath := "video_debug.mp4"
		if strings.HasPrefix(mime, "audio/") {
			outPath = "video_debug_audio.mp3"
		}
		if writeErr := os.WriteFile(outPath, data, 0o644); writeErr != nil {
			fmt.Printf("  ✗ Could not write %s: %v\n", outPath, writeErr)
		} else {
			fmt.Printf("  ✓ Saved to %s\n", outPath)
		}

		input := strings.ToLower(readLine(r, "\n  [a]nother URL   [b]ack to menu   [q]uit: "))
		switch input {
		case "q", "quit", "exit":
			return false
		case "b", "back":
			return true
		}
		// "a" or anything else: loop for another URL
	}
}

// dumpVideoScope walks the expected JSON path step by step and prints what is
// present (✓) or missing (✗) at each level, along with the sibling keys so
// the caller can see what TikTok actually returned.
func dumpVideoScope(scope map[string]any) {
	check := func(m map[string]any, key string) (map[string]any, bool) {
		v, ok := m[key]
		if !ok {
			return nil, false
		}
		sub, ok := v.(map[string]any)
		return sub, ok
	}

	printKeys := func(label string, m map[string]any) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("    %s keys: %s\n", label, strings.Join(keys, ", "))
	}

	fmt.Println()

	defaultScope, ok := check(scope, "__DEFAULT_SCOPE__")
	if !ok {
		fmt.Println("  ✗ __DEFAULT_SCOPE__ — not found or not an object")
		return
	}
	fmt.Println("  ✓ __DEFAULT_SCOPE__")
	printKeys("", defaultScope)

	videoDetail, ok := check(defaultScope, "webapp.video-detail")
	if !ok {
		fmt.Println("  ✗ webapp.video-detail — not found or not an object")
		return
	}
	fmt.Println("  ✓ webapp.video-detail")
	printKeys("", videoDetail)

	itemInfo, ok := check(videoDetail, "itemInfo")
	if !ok {
		fmt.Println("  ✗ itemInfo — not found or not an object")
		return
	}
	fmt.Println("  ✓ itemInfo")
	printKeys("", itemInfo)

	itemStruct, ok := check(itemInfo, "itemStruct")
	if !ok {
		fmt.Println("  ✗ itemStruct — not found or not an object")
		return
	}
	fmt.Println("  ✓ itemStruct")
	printKeys("", itemStruct)

	video, ok := check(itemStruct, "video")
	if !ok {
		fmt.Println("  ✗ video — not found or not an object")
		return
	}
	fmt.Println("  ✓ video")
	printKeys("", video)

	playAddr, ok := video["playAddr"].(string)
	if !ok || playAddr == "" {
		fmt.Printf("  ✗ playAddr — raw value: %#v\n", video["playAddr"])
		return
	}
	fmt.Printf("  ✓ playAddr: %s\n", playAddr)
}

// ─── conversation screen ─────────────────────────────────────────────────────

type convState struct {
	conv       libtiktok.Conversation
	messages   []libtiktok.Message // chronological: oldest first
	nextCursor string              // empty string = no older messages
}

// showConversation manages the message-view loop for a single conversation.
// Returns true to go back to the inbox, false to quit the program.
func showConversation(
	ctx context.Context,
	r *bufio.Reader,
	client *libtiktok.Client,
	conv libtiktok.Conversation,
	selfID string,
) bool {
	clearScreen()
	fmt.Printf("  Loading messages for %s…\n", convLabel(conv, selfID))

	msgs, cursor, err := client.GetMessages(ctx, &conv, "")
	if err != nil {
		fmt.Printf("\n  Error loading messages: %v\n", err)
		readLine(r, "\n  Press Enter to go back… ")
		return true
	}

	state := &convState{
		conv:       conv,
		messages:   msgs,
		nextCursor: cursor,
	}

	for {
		clearScreen()

		label := convLabel(conv, selfID)
		fmt.Printf("  Conversation %s\n", label)
		fmt.Printf("  (%d messages loaded)\n", len(state.messages))
		hr()
		fmt.Println()

		// ── "load more" affordance at the top ──
		if state.nextCursor != "" {
			fmt.Println("  ^ ^ ^  [l]oad earlier messages  ^ ^ ^")
		} else {
			fmt.Println("  ^ ^ ^  (beginning of conversation)  ^ ^ ^")
		}
		fmt.Println()

		// ── messages ──
		if len(state.messages) == 0 {
			fmt.Println("  (no messages)")
		} else {
			for i, m := range state.messages {
				fmt.Printf("  %3d. %s\n", i+1, strings.TrimPrefix(fmtMessage(m, selfID), "  "))
			}
		}

		fmt.Println()
		hr()

		// ── command bar ──
		var cmds []string
		if state.nextCursor != "" {
			cmds = append(cmds, "[l]oad more")
		}
		cmds = append(cmds, "[s]end", "[i]mage", "[r]eact", "[b]ack", "[q]uit")
		fmt.Printf("  %s\n", strings.Join(cmds, "   "))

		input := strings.ToLower(readLine(r, "  > "))

		switch input {
		case "q", "quit", "exit":
			return false

		case "b", "back":
			return true

		case "l", "load", "load more":
			if state.nextCursor == "" {
				fmt.Println("\n  Already at the beginning of the conversation.")
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			fmt.Println("\n  Loading earlier messages…")
			older, newCursor, err := client.GetMessages(ctx, &conv, state.nextCursor)
			if err != nil {
				fmt.Printf("  Error: %v\n", err)
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			// Prepend older messages so the slice remains chronological.
			state.messages = append(older, state.messages...)
			state.nextCursor = newCursor

		case "s", "send":
			fmt.Println()
			msgText := readLine(r, "  Message (empty to cancel): ")
			if msgText == "" {
				continue
			}
			fmt.Println("  Sending…")
			res, err := client.SendMessage(ctx, libtiktok.SendMessageParams{
				ConvID: conv.ID,
				Text:   msgText,
			})
			if err != nil {
				fmt.Printf("  Error: %v\n", err)
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			fmt.Printf("  Sent! (ID: %s)\n  Refreshing…\n", res.MessageID)
			fresh, newCursor, err := client.GetMessages(ctx, &conv, "")
			if err != nil {
				fmt.Printf("  Warning: could not refresh: %v\n", err)
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			state.messages = fresh
			state.nextCursor = newCursor

		case "i", "image":
			fmt.Println()
			imagePath := readLine(r, "  Image path (empty to cancel): ")
			if imagePath == "" {
				continue
			}
			imageData, err := os.ReadFile(imagePath)
			if err != nil {
				fmt.Printf("  Error reading file: %v\n", err)
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			mimeType := detectImageMIME(imageData, imagePath)
			if !strings.HasPrefix(mimeType, "image/") {
				fmt.Printf("  %s does not look like an image (detected %q).\n", imagePath, mimeType)
				readLine(r, "  Press Enter to continue… ")
				continue
			}

			fmt.Println("  Uploading image…")
			res, err := client.SendMessage(ctx, libtiktok.SendMessageParams{
				ConvID: conv.ID,
				Image: &libtiktok.OutgoingImage{
					Data:     imageData,
					FileName: filepath.Base(imagePath),
					MimeType: mimeType,
				},
			})
			if err != nil {
				fmt.Printf("  Error: %v\n", err)
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			fmt.Printf("  Image sent! (ID: %s)\n  Refreshing…\n", res.MessageID)
			fresh, newCursor, err := client.GetMessages(ctx, &conv, "")
			if err != nil {
				fmt.Printf("  Warning: could not refresh: %v\n", err)
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			state.messages = fresh
			state.nextCursor = newCursor

		case "r", "react":
			if len(state.messages) == 0 {
				fmt.Println("\n  No messages to react to.")
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			fmt.Println()
			numStr := readLine(r, fmt.Sprintf("  Message number [1-%d] (empty to cancel): ", len(state.messages)))
			if numStr == "" {
				continue
			}
			n, err := strconv.Atoi(numStr)
			if err != nil || n < 1 || n > len(state.messages) {
				fmt.Printf("\n  Invalid message number %q.\n", numStr)
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			target := state.messages[n-1]

			emoji := readLine(r, "  Emoji to react with (empty to cancel): ")
			if emoji == "" {
				continue
			}

			actionStr := strings.ToLower(readLine(r, "  [a]dd or [r]emove reaction: "))
			var action libtiktok.ReactionAction
			switch actionStr {
			case "a", "add":
				action = libtiktok.ReactionAdd
			case "r", "remove":
				action = libtiktok.ReactionRemove
			default:
				fmt.Printf("\n  Unknown action %q — use 'a' or 'r'.\n", actionStr)
				readLine(r, "  Press Enter to continue… ")
				continue
			}

			fmt.Println("  Sending reaction…")
			err = client.SendReaction(ctx, libtiktok.SendReactionParams{
				ConvID:          conv.ID,
				Emoji:           emoji,
				Action:          action,
				SelfUserID:      selfID,
				ConvoSourceID:   conv.SourceID,
				ServerMessageID: target.ServerID,
			})
			if err != nil {
				fmt.Printf("  Error: %v\n", err)
				readLine(r, "  Press Enter to continue… ")
				continue
			}
			fmt.Printf("  Reaction sent! (target message ID: %d)\n", target.ServerID)
			readLine(r, "  Press Enter to continue… ")
		}
	}
}

// ─── entry point ─────────────────────────────────────────────────────────────

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintln(os.Stderr, "Create a config.toml next to the binary with:")
		fmt.Fprintln(os.Stderr, "  [auth]")
		fmt.Fprintln(os.Stderr, `  cookie = "<your TikTok cookie string>"`)
		os.Exit(1)
	}

	ctx := context.Background()
	client := libtiktok.NewClient(cfg.Auth.Cookie)
	reader := bufio.NewReader(os.Stdin)

	clearScreen()
	fmt.Println("  TikTok Messages")
	hr()
	fmt.Println()
	fmt.Println("  Signing in and fetching inbox…")

	// GetSelf — best-effort; we use selfID only for display labels.
	selfID := ""
	self, err := client.GetSelf(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: GetSelf failed: %v\n", err)
	} else {
		selfID = self.UserID
		fmt.Printf("  Signed in as @%s (%s)\n", self.UniqueID, self.UserID)
	}

	convs, err := client.GetInbox(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n  GetInbox error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Found %d conversation(s).\n", len(convs))

	// Main navigation loop.
	for {
		switch showMainMenu(reader) {
		case "quit":
			fmt.Println("\n  Goodbye!")
			return

		case "lookup":
			if !lookupUser(ctx, reader, client) {
				fmt.Println("\n  Goodbye!")
				return
			}

		case "download":
			if !downloadVideo(ctx, reader, client) {
				fmt.Println("\n  Goodbye!")
				return
			}

		case "inbox":
		inboxLoop:
			for {
				idx := showInbox(reader, convs, selfID)
				if idx == -1 {
					break inboxLoop
				}
				if !showConversation(ctx, reader, client, convs[idx], selfID) {
					fmt.Println("\n  Goodbye!")
					return
				}
			}
		}
		// unrecognised input: fall through and redraw the menu
	}
}
