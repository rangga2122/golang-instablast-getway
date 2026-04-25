package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/storage"
)

const SessionCookieName = "instablast_session"

type Session struct {
	Token     string
	UserID    string
	Email     string
	IsAdmin   bool
	ExpiresAt time.Time
}

type Service struct {
	mu       sync.RWMutex
	store    *storage.Storage
	sessions map[string]Session
	ttl      time.Duration
}

func NewService(store *storage.Storage) *Service {
	return &Service{
		store:    store,
		sessions: make(map[string]Session),
		ttl:      7 * 24 * time.Hour,
	}
}

func (s *Service) Login(email, password string) (storage.AppUser, Session, error) {
	user, err := s.store.AuthenticateUser(email, password)
	if err != nil {
		return storage.AppUser{}, Session{}, err
	}
	token, err := newToken()
	if err != nil {
		return storage.AppUser{}, Session{}, err
	}
	session := Session{
		Token:     token,
		UserID:    user.ID,
		Email:     user.Email,
		IsAdmin:   user.IsAdmin,
		ExpiresAt: time.Now().Add(s.ttl),
	}
	s.mu.Lock()
	s.sessions[token] = session
	s.mu.Unlock()
	return user, session, nil
}

func (s *Service) Logout(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func (s *Service) GetUserByToken(token string) (storage.AppUser, Session, error) {
	if token == "" {
		return storage.AppUser{}, Session{}, fmt.Errorf("missing session")
	}
	s.mu.RLock()
	session, ok := s.sessions[token]
	s.mu.RUnlock()
	if !ok {
		return storage.AppUser{}, Session{}, fmt.Errorf("invalid session")
	}
	if time.Now().After(session.ExpiresAt) {
		s.Logout(token)
		return storage.AppUser{}, Session{}, fmt.Errorf("session expired")
	}
	user, err := s.store.GetUserByID(session.UserID)
	if err != nil {
		s.Logout(token)
		return storage.AppUser{}, Session{}, err
	}
	if !user.IsActive || (!user.ExpiresAt.IsZero() && time.Now().After(user.ExpiresAt)) {
		s.Logout(token)
		return storage.AppUser{}, Session{}, fmt.Errorf("account inactive")
	}
	return user, session, nil
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
