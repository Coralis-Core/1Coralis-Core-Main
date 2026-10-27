package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/kernel"
	"example.com/project/engine/verify/fixtures/app/console"
	"example.com/project/engine/verify/fixtures/contracts"
	"example.com/project/engine/verify/fixtures/contracts/events"
	"example.com/project/engine/verify/fixtures/contracts/stats"
	"example.com/project/engine/verify/fixtures/cores/memory"
)

func eventContract(t *testing.T) *contract.Contract {
	t.Helper()
	set, err := contract.Load(contracts.FS)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := set.Get(events.TopicMessageSeen)
	if !ok {
		t.Fatal("event contract missing")
	}
	return c
}

// PUBLISHER side: what the console frontend really puts on the bus must
// satisfy the language-neutral payload schema.
func TestFrontendPublishesConformantEvents(t *testing.T) {
	c := eventContract(t)
	p := kernel.New(config.Config{}, nil)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var emitted [][]byte
	p.Host().Bus.Tap(events.TopicMessageSeen, func(_ context.Context, raw []byte) {
		mu.Lock()
		emitted = append(emitted, raw)
		mu.Unlock()
	})
	console.Run(context.Background(), p.Host().Router, p.Host().Bus, strings.NewReader("msg hello\nmsg again\nquit\n"), &bytes.Buffer{})
	mu.Lock()
	defer mu.Unlock()
	if problems := contract.CheckEmitted(c, emitted); len(problems) > 0 {
		t.Fatal(problems)
	}
	if len(emitted) != 2 {
		t.Fatalf("expected 2 events, got %d", len(emitted))
	}
}

// The harness has teeth: a publisher that emits garbage (or nothing) is caught.
func TestCheckEmittedCatchesBadPublisher(t *testing.T) {
	c := eventContract(t)
	if len(contract.CheckEmitted(c, nil)) == 0 {
		t.Fatal("silent publisher not detected")
	}
	if len(contract.CheckEmitted(c, [][]byte{[]byte(`{"chat_id":"1","user_id":7}`)})) == 0 {
		t.Fatal("invalid payload not detected")
	}
}

// SUBSCRIBER side: the counter core must accept every valid example, reject
// every negative one, and actually react to the valid ones.
func TestCounterSubscriberConformsToEventContract(t *testing.T) {
	c := eventContract(t)
	p := kernel.New(config.Config{}, nil)
	counter := memory.NewCounter("counter")
	if err := p.AddCore(counter); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())

	deliver := func(ctx context.Context, payload []byte) error {
		if err := c.ValidatePayload(payload); err != nil { // boundary check
			return err
		}
		var ev events.MessageSeen
		if err := json.Unmarshal(payload, &ev); err != nil {
			return err
		}
		p.Host().Bus.Publish(ctx, events.TopicMessageSeen, ev)
		return nil
	}
	if problems := contract.CheckSubscriber(context.Background(), c, deliver); len(problems) > 0 {
		t.Fatal(problems)
	}
	// 2 of the 3 valid examples are in chat 1; the third is in the negative-id chat.
	for chat, want := range map[int64]int{1: 2, -1001234567890: 1} {
		got, err := stats.MessageCount.Call(context.Background(), p.Host().Router, stats.CountQuery{ChatID: chat})
		if err != nil || got.Messages != want {
			t.Fatalf("chat %d: got %v err %v, want %d", chat, got, err, want)
		}
	}
	// a subscriber that accepts everything must be caught
	if len(contract.CheckSubscriber(context.Background(), c, func(context.Context, []byte) error { return nil })) == 0 {
		t.Fatal("subscriber accepting forbidden payloads not detected")
	}
}
