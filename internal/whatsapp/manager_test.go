package whatsapp

import (
	"testing"
	"time"
)

func TestReconnectBackoff(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: 15 * time.Second},
		{failures: 1, want: 15 * time.Second},
		{failures: 2, want: 30 * time.Second},
		{failures: 3, want: time.Minute},
		{failures: 7, want: 10 * time.Minute},
		{failures: 20, want: 10 * time.Minute},
	}

	for _, test := range tests {
		if got := reconnectBackoff(test.failures); got != test.want {
			t.Fatalf("reconnectBackoff(%d) = %v, want %v", test.failures, got, test.want)
		}
	}
}
