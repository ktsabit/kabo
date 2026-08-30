package transport

import (
	"encoding/json"
	"log"
	"math/rand"
	"sync"

	"cambio-activity/server/game"
	"github.com/gorilla/websocket"
)

type Client struct {
	playerID string
	room     *Room
	conn     *websocket.Conn
	send     chan []byte
	close    sync.Once
}

type Room struct {
	mu      sync.Mutex
	game    *game.Game
	clients map[string]*Client
}

type Manager struct {
	mu    sync.Mutex
	rooms map[string]*Room
}

func NewManager() *Manager {
	return &Manager{rooms: map[string]*Room{}}
}

func (m *Manager) Join(roomID, playerID, name string, conn *websocket.Conn) (*Client, error) {
	m.mu.Lock()
	room := m.rooms[roomID]
	if room == nil {
		room = &Room{game: game.New(roomID, rand.New(rand.NewSource(rand.Int63()))), clients: map[string]*Client{}}
		m.rooms[roomID] = room
	}
	m.mu.Unlock()

	room.mu.Lock()
	room.game.Lock()
	_, err := room.game.AddOrReconnect(playerID, name)
	room.game.Unlock()
	if err != nil {
		room.mu.Unlock()
		return nil, err
	}
	if old := room.clients[playerID]; old != nil {
		_ = old.conn.Close()
	}
	client := &Client{playerID: playerID, room: room, conn: conn, send: make(chan []byte, 16)}
	room.clients[playerID] = client
	room.mu.Unlock()
	room.broadcast()
	return client, nil
}

func (c *Client) Run() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for payload := range c.send {
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		}
	}()

	c.conn.SetReadLimit(16 << 10)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		var msg game.ClientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.push(game.EncodeError("invalid_json", "Could not read that action."))
			continue
		}
		c.room.game.Lock()
		err = c.room.game.Apply(c.playerID, msg)
		c.room.game.Unlock()
		if err != nil {
			code := "invalid_action"
			if coded, ok := err.(interface{ ErrorCode() string }); ok {
				code = coded.ErrorCode()
			}
			c.push(game.EncodeError(code, err.Error()))
		}
		c.room.broadcast()
	}

	c.room.remove(c)
	_ = c.conn.Close()
	<-done
}

func (r *Room) remove(client *Client) {
	r.mu.Lock()
	if r.clients[client.playerID] == client {
		delete(r.clients, client.playerID)
		r.game.Lock()
		r.game.Disconnect(client.playerID)
		r.game.Unlock()
	}
	client.close.Do(func() { close(client.send) })
	r.mu.Unlock()
	r.broadcast()
}

func (r *Room) broadcast() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.game.Lock()
	defer r.game.Unlock()
	for id, client := range r.clients {
		payload, err := json.Marshal(r.game.View(id))
		if err != nil {
			log.Printf("encode snapshot: %v", err)
			continue
		}
		client.push(payload)
	}
}

func (c *Client) push(payload []byte) {
	select {
	case c.send <- payload:
	default:
		// A stale client must not block the authoritative room loop.
	}
}
