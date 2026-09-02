package transport

import (
	"encoding/json"
	"log"
	"math/rand"
	"sync"
	"time"

	"kabo/server/game"
	"kabo/server/persistence"

	"github.com/gorilla/websocket"
)

type Client struct {
	playerID string
	room     *Room
	conn     *websocket.Conn
	send     chan []byte
	close    sync.Once
	sendMu   sync.Mutex
	closed   bool
}

type TimeoutConfig struct {
	Initial time.Duration
	Turn    time.Duration
	Reveal  time.Duration
}

const (
	DefaultInitialTimeout = 30 * time.Second
	DefaultTurnTimeout    = 15 * time.Second
	DefaultRevealTimeout  = 3 * time.Second
)

func (c TimeoutConfig) normalized() TimeoutConfig {
	if c.Initial <= 0 {
		c.Initial = DefaultInitialTimeout
	}
	if c.Turn <= 0 {
		c.Turn = DefaultTurnTimeout
	}
	if c.Reveal <= 0 {
		c.Reveal = DefaultRevealTimeout
	}
	return c
}

type ManagerConfig struct {
	Timeouts TimeoutConfig
	Results  *persistence.Store
}

type Room struct {
	mu      sync.Mutex
	game    *game.Game
	clients map[string]*Client

	timeouts          TimeoutConfig
	results           *persistence.Store
	timeoutTimer      *time.Timer
	timeoutKey        string
	lastRecordedRound int
}

type Manager struct {
	mu       sync.Mutex
	rooms    map[string]*Room
	timeouts TimeoutConfig
	results  *persistence.Store
}

func NewManager(config ...ManagerConfig) *Manager {
	settings := ManagerConfig{}
	if len(config) > 0 {
		settings = config[0]
	}
	return &Manager{
		rooms:    map[string]*Room{},
		timeouts: settings.Timeouts.normalized(),
		results:  settings.Results,
	}
}

func (m *Manager) Join(roomID, playerID, name string, conn *websocket.Conn) (*Client, error) {
	return m.JoinWithMetadata(roomID, playerID, name, game.RoomMetadata{Platform: "browser", InstanceID: roomID}, conn)
}

func (m *Manager) JoinWithMetadata(roomID, playerID, name string, metadata game.RoomMetadata, conn *websocket.Conn) (*Client, error) {
	m.mu.Lock()
	room := m.rooms[roomID]
	if room == nil {
		room = &Room{
			game:     game.NewWithMetadata(roomID, rand.New(rand.NewSource(rand.Int63())), metadata),
			clients:  map[string]*Client{},
			timeouts: m.timeouts,
			results:  m.results,
		}
		m.rooms[roomID] = room
	}
	m.mu.Unlock()

	room.mu.Lock()
	room.game.Lock()
	room.game.SetRoomMetadata(metadata)
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
	room.broadcastLocked()
	room.scheduleTimeoutLocked()
	room.mu.Unlock()
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
		c.room.mu.Lock()
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
		c.room.recordResultLocked()
		c.room.scheduleTimeoutLocked()
		c.room.broadcastLocked()
		c.room.mu.Unlock()
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
	client.sendMu.Lock()
	client.closed = true
	client.close.Do(func() { close(client.send) })
	client.sendMu.Unlock()
	if len(r.clients) == 0 {
		r.stopTimeoutLocked()
	} else {
		r.broadcastLocked()
		r.scheduleTimeoutLocked()
	}
	r.mu.Unlock()
}

func (r *Room) broadcast() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broadcastLocked()
}

func (r *Room) broadcastLocked() {
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

func (r *Room) scheduleTimeoutLocked() {
	r.game.Lock()
	key := r.game.TimeoutKey()
	kind := r.game.TimeoutKind()
	if key == r.timeoutKey {
		r.game.Unlock()
		return
	}
	if r.timeoutTimer != nil {
		r.timeoutTimer.Stop()
		r.timeoutTimer = nil
	}
	r.timeoutKey = key
	if key == "" {
		r.game.DeadlineAt = time.Time{}
		r.game.Unlock()
		return
	}
	duration := r.timeouts.Turn
	if kind == game.TimerInitial {
		duration = r.timeouts.Initial
	} else if kind == game.TimerReveal {
		duration = r.timeouts.Reveal
	}
	deadline := time.Now().Add(duration)
	r.game.DeadlineAt = deadline
	r.game.Unlock()
	r.timeoutTimer = time.AfterFunc(duration, func() {
		r.fireTimeout(key)
	})
}

func (r *Room) stopTimeoutLocked() {
	if r.timeoutTimer != nil {
		r.timeoutTimer.Stop()
		r.timeoutTimer = nil
	}
	r.timeoutKey = ""
	r.game.Lock()
	r.game.DeadlineAt = time.Time{}
	r.game.Unlock()
}

func (r *Room) fireTimeout(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timeoutKey != key {
		return
	}
	r.timeoutTimer = nil
	r.game.Lock()
	if r.game.TimeoutKey() != key {
		r.game.Unlock()
		r.scheduleTimeoutLocked()
		return
	}
	err := r.game.Timeout()
	r.game.Unlock()
	if err != nil {
		log.Printf("apply timeout: %v", err)
	}
	r.recordResultLocked()
	r.scheduleTimeoutLocked()
	r.broadcastLocked()
}

func (r *Room) recordResultLocked() {
	if r.results == nil {
		return
	}
	r.game.Lock()
	result := r.game.Result()
	r.game.Unlock()
	if result == nil || result.Round <= r.lastRecordedRound {
		return
	}
	if err := r.results.RecordRound(*result); err != nil {
		log.Printf("record round %d: %v", result.Round, err)
		return
	}
	r.lastRecordedRound = result.Round
}

func (c *Client) push(payload []byte) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.send <- payload:
		return
	default:
		// A stale client must not block the authoritative room loop. Drop the
		// oldest queued message so the newest snapshot is always retained.
		select {
		case <-c.send:
		default:
		}
		select {
		case c.send <- payload:
		default:
		}
	}
}
