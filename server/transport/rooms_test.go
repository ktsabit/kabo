package transport

import (
	"testing"
	"time"

	"kabo/server/game"
)

func TestClientPushRetainsNewestPayloadWhenBufferIsFull(t *testing.T) {
	client := &Client{send: make(chan []byte, 2)}
	client.push([]byte("first"))
	client.push([]byte("second"))
	client.push([]byte("latest"))

	if got := string(<-client.send); got != "second" {
		t.Fatalf("oldest payload was not evicted: got %q", got)
	}
	if got := string(<-client.send); got != "latest" {
		t.Fatalf("newest payload was not retained: got %q", got)
	}
}

func TestClientPushIgnoresClosedClient(t *testing.T) {
	client := &Client{send: make(chan []byte, 1), closed: true}
	client.push([]byte("ignored"))

	select {
	case payload := <-client.send:
		t.Fatalf("closed client received payload %q", payload)
	default:
	}
}

func TestConfiguredTurnTimeoutAdvancesRoomAndKeepsDeadlineInSync(t *testing.T) {
	manager := NewManager(ManagerConfig{Timeouts: TimeoutConfig{Turn: 20 * time.Millisecond, Reveal: 10 * time.Millisecond}})
	if _, err := manager.Join("room", "a", "Ada", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Join("room", "b", "Ben", nil); err != nil {
		t.Fatal(err)
	}

	manager.mu.Lock()
	room := manager.rooms["room"]
	manager.mu.Unlock()
	room.mu.Lock()
	room.game.Lock()
	for _, id := range []string{"a", "b"} {
		if err := room.game.Apply(id, game.ClientMessage{Type: "set_ready", Ready: true}); err != nil {
			room.game.Unlock()
			room.mu.Unlock()
			t.Fatal(err)
		}
	}
	if err := room.game.Apply("a", game.ClientMessage{Type: "start_game"}); err != nil {
		room.game.Unlock()
		room.mu.Unlock()
		t.Fatal(err)
	}
	room.game.Unlock()
	room.scheduleTimeoutLocked()
	room.broadcastLocked()
	room.mu.Unlock()

	defer func() {
		room.mu.Lock()
		room.stopTimeoutLocked()
		room.mu.Unlock()
	}()

	waitForRoom(t, room, func(g *game.Game) bool {
		return g.Phase == game.PhaseAwaitDraw && g.View("b").CurrentPlayerID == "b"
	})
	room.mu.Lock()
	room.game.Lock()
	deadline := room.game.DeadlineAt
	phase := room.game.Phase
	room.game.Unlock()
	room.mu.Unlock()
	if phase != game.PhaseAwaitDraw || deadline.IsZero() || !deadline.After(time.Now()) {
		t.Fatalf("timeout did not publish the next live deadline: phase=%s deadline=%v", phase, deadline)
	}
}

func waitForRoom(t *testing.T, room *Room, condition func(*game.Game) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		room.mu.Lock()
		room.game.Lock()
		matched := condition(room.game)
		room.game.Unlock()
		room.mu.Unlock()
		if matched {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("room did not reach the expected timed state")
}
