// Package console (TEST FIXTURE) is the simplest possible top layer: it only
// uses the router (capabilities) and the bus (events), nothing about any core.
package console

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"example.com/project/engine/base/bus"
	"example.com/project/engine/base/capability"
	"example.com/project/engine/verify/fixtures/contracts/events"
	"example.com/project/engine/verify/fixtures/contracts/profile"
)

const (
	chatID int64 = 1
	userID int64 = 7
)

// Run reads lines: "msg <text>" publishes a message event, "/profile" calls the
// profile capability, "quit" stops.
func Run(ctx context.Context, r *capability.Router, b *bus.Bus, in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "quit":
			return
		case line == "":
		case strings.HasPrefix(line, "msg"):
			b.Publish(ctx, events.TopicMessageSeen, events.MessageSeen{
				ChatID: chatID, UserID: userID, Text: strings.TrimSpace(strings.TrimPrefix(line, "msg")),
			})
		case line == "/profile":
			p, err := profile.Get.Call(ctx, r, profile.Query{ChatID: chatID, UserID: userID})
			if err != nil {
				fmt.Fprintln(out, "error:", err)
				continue
			}
			name, msgs := fmt.Sprint(p.UserID), "n/a"
			if p.Name != "" {
				name = p.Name
			}
			if p.Messages != nil {
				msgs = fmt.Sprint(*p.Messages)
			}
			fmt.Fprintf(out, "%s | messages in this chat: %s\n", name, msgs)
		default:
			fmt.Fprintln(out, "unknown input")
		}
	}
}
