package ai

import "testing"

func TestExtractDestinationQuery(t *testing.T) {
	got := extractDestinationQuery("ongkir ke bogor berapa?")
	if got != "bogor" {
		t.Fatalf("expected bogor, got %q", got)
	}
}

func TestExtractWeightGrams(t *testing.T) {
	got, assumed := extractWeightGrams("ongkir ke bogor untuk 1.5 kg")
	if got != 1500 {
		t.Fatalf("expected 1500, got %d", got)
	}
	if assumed {
		t.Fatalf("expected explicit weight, got assumed")
	}
}

func TestExtractRequestedCouriers(t *testing.T) {
	got := extractRequestedCouriers("cek ongkir jne sicepat ke bogor")
	if got != "jne:sicepat" {
		t.Fatalf("expected jne:sicepat, got %q", got)
	}
}

func TestLooksLikeOriginQuestion(t *testing.T) {
	if !looksLikeOriginQuestion("pengiriman dari mana?") {
		t.Fatalf("expected origin question to be detected")
	}
}
