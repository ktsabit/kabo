package transport

import "testing"

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
