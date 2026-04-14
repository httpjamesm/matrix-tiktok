package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
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
			for _, m := range state.messages {
				fmt.Println(fmtMessage(m, selfID))
			}
		}

		fmt.Println()
		hr()

		// ── command bar ──
		var cmds []string
		if state.nextCursor != "" {
			cmds = append(cmds, "[l]oad more")
		}
		cmds = append(cmds, "[s]end", "[b]ack", "[q]uit")
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

	// Main navigation loop: inbox → conversation → back to inbox.
	for {
		idx := showInbox(reader, convs, selfID)
		if idx == -1 {
			fmt.Println("\n  Goodbye!")
			return
		}

		goBack := showConversation(ctx, reader, client, convs[idx], selfID)
		if !goBack {
			fmt.Println("\n  Goodbye!")
			return
		}
	}
}
