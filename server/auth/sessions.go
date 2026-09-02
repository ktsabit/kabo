package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

type Identity struct {
	ID   string
	Name string
}

type SessionMetadata struct {
	Platform       string
	ClientPlatform string
	ApplicationID  string
	InstanceID     string
	GuildID        string
	ChannelID      string
	LocationID     string
	CustomID       string
	ReferrerID     string
}

type session struct {
	Identity
	RoomID    string
	Metadata  SessionMetadata
	ExpiresAt time.Time
}

type Sessions struct {
	mu   sync.Mutex
	data map[string]session
}

func NewSessions() *Sessions {
	return &Sessions{data: map[string]session{}}
}

func (s *Sessions) Create(identity Identity, roomID string, lifetime time.Duration) (string, error) {
	return s.CreateWithMetadata(identity, roomID, SessionMetadata{}, lifetime)
}

func (s *Sessions) CreateWithMetadata(identity Identity, roomID string, metadata SessionMetadata, lifetime time.Duration) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	s.data[id] = session{Identity: identity, RoomID: roomID, Metadata: metadata, ExpiresAt: time.Now().Add(lifetime)}
	s.mu.Unlock()
	return id, nil
}

func (s *Sessions) Resolve(id, roomID string) (Identity, error) {
	identity, _, err := s.ResolveWithMetadata(id, roomID)
	return identity, err
}

func (s *Sessions) ResolveWithMetadata(id, roomID string) (Identity, SessionMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	if !ok || time.Now().After(value.ExpiresAt) {
		delete(s.data, id)
		return Identity{}, SessionMetadata{}, errors.New("session is missing or expired")
	}
	if value.RoomID != roomID {
		return Identity{}, SessionMetadata{}, errors.New("session is not valid for this room")
	}
	return value.Identity, value.Metadata, nil
}
