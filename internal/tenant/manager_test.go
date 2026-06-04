package tenant

import (
	"testing"
	"time"

	"github.com/azkazamdigital/wa-gateway/internal/storage"
)

func TestUserEligibleForBackgroundStart(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		user storage.AppUser
		want bool
	}{
		{name: "active user", user: storage.AppUser{IsActive: true, ExpiresAt: now.Add(time.Hour)}, want: true},
		{name: "active without expiry", user: storage.AppUser{IsActive: true}, want: true},
		{name: "inactive user", user: storage.AppUser{IsActive: false, ExpiresAt: now.Add(time.Hour)}, want: false},
		{name: "expired user", user: storage.AppUser{IsActive: true, ExpiresAt: now.Add(-time.Hour)}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := userEligibleForBackgroundStart(test.user, now); got != test.want {
				t.Fatalf("userEligibleForBackgroundStart() = %v, want %v", got, test.want)
			}
		})
	}
}
