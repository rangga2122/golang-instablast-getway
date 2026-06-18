package whatsapp

import (
	"testing"
	"time"
)

func TestAccountInfoTreatsLoggedInHealthySessionAsOnlineWithoutActiveSocket(t *testing.T) {
	m := &Manager{activeID: "acc-1"}
	session := &Session{
		meta: AccountMeta{
			ID:        "acc-1",
			Name:      "Akun WA 1",
			JID:       "6285343791016@s.whatsapp.net",
			CreatedAt: time.Now(),
		},
		healthy: true,
	}

	info := m.accountInfoLocked(session)

	if !info.LoggedIn {
		t.Fatalf("LoggedIn = false, want true for saved WA session with JID")
	}
	if !info.Connected {
		t.Fatalf("Connected = false, want true for a healthy logged-in session")
	}
	if info.Status != "Online" {
		t.Fatalf("Status = %q, want Online", info.Status)
	}
}

func TestSupervisorMarksConnectedLoggedInSessionHealthy(t *testing.T) {
	m := &Manager{
		sessions: map[string]*Session{
			"acc-1": {
				meta:    AccountMeta{ID: "acc-1", JID: "6285343791016@s.whatsapp.net", CreatedAt: time.Now()},
				healthy: false,
				client:  nil,
			},
		},
	}

	m.mu.Lock()
	session := m.sessions["acc-1"]
	m.markSessionHealthyLocked(session)
	m.mu.Unlock()

	info := m.ListAccounts()[0]
	if !info.Connected || info.Status != "Online" {
		t.Fatalf("status after supervisor healing = connected=%v status=%q, want connected Online", info.Connected, info.Status)
	}
}

func TestReconnectBackoff(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: 15 * time.Second},
		{failures: 1, want: 15 * time.Second},
		{failures: 2, want: 30 * time.Second},
		{failures: 3, want: 60 * time.Second},
		{failures: 7, want: 10 * time.Minute},
		{failures: 20, want: 10 * time.Minute},
	}

	for _, test := range tests {
		if got := reconnectBackoff(test.failures); got != test.want {
			t.Fatalf("reconnectBackoff(%d) = %v, want %v", test.failures, got, test.want)
		}
	}
}
