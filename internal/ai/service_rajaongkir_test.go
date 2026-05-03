package ai

import (
	"strings"
	"testing"
)

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

func TestSanitizeSettingsKeepsSystemOriginTrimmed(t *testing.T) {
	settings := sanitizeSettings(Settings{
		SystemOngkirEnabled: true,
		SystemOngkirOrigin:  "  Jakarta Selatan  ",
	})
	if settings.SystemOngkirOrigin != "Jakarta Selatan" {
		t.Fatalf("expected trimmed system origin, got %q", settings.SystemOngkirOrigin)
	}
}

func TestSaveDisablesRajaOngkirWhenSystemOngkirEnabled(t *testing.T) {
	service := NewService(func(string, string) {})
	saved, err := service.Save(nil, Settings{
		AccountIDs:          []string{"acc-1"},
		SystemOngkirEnabled: true,
		SystemOngkirOrigin:  "Bandung",
		RajaOngkirEnabled:   true,
		RajaOngkirAPIKey:    "abc",
		RajaOngkirOrigin:    "Jakarta",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !saved.SystemOngkirEnabled {
		t.Fatalf("expected system ongkir to stay enabled")
	}
	if saved.RajaOngkirEnabled {
		t.Fatalf("expected RajaOngkir to be disabled when system ongkir is enabled")
	}
}

func TestBuildVisionFallbackReplyForPaymentProof(t *testing.T) {
	userText := "Berikut analisa gambar yang dikirim user:\n---\n*Teks Penting*:\n- Pembayaran QRIS berhasil\n- Jumlah Rp120.000\n---\nJawab user berdasarkan isi gambar tersebut."
	got := buildVisionFallbackReply(userText)
	if got == "" {
		t.Fatalf("expected non-empty fallback reply")
	}
	if !strings.Contains(got, "pembayaran berhasil") {
		t.Fatalf("expected payment fallback, got %q", got)
	}
}

func TestBuildVisionFallbackReplyGenericSummary(t *testing.T) {
	userText := "Berikut analisa gambar yang dikirim user:\n---\nPoster promo ikat pinggang kulit dengan harga spesial minggu ini.\n---\nJawab user berdasarkan isi gambar tersebut."
	got := buildVisionFallbackReply(userText)
	if got == "" {
		t.Fatalf("expected non-empty fallback reply")
	}
}

func TestSelectProductsForAccountReturnsAllWhenNoMapping(t *testing.T) {
	settings := Settings{
		Products: []ProductKnowledge{
			{ID: "p1", Name: "Produk A", Content: "A"},
			{ID: "p2", Name: "Produk B", Content: "B"},
		},
		AccountProductIDs: map[string][]string{},
	}
	got := selectProductsForAccount(settings, "acc-1")
	if len(got) != 2 {
		t.Fatalf("expected 2 products, got %d", len(got))
	}
}

func TestSelectProductsForAccountPrioritizesMappedButKeepsAll(t *testing.T) {
	settings := Settings{
		Products: []ProductKnowledge{
			{ID: "p1", Name: "Produk A", Content: "A"},
			{ID: "p2", Name: "Produk B", Content: "B"},
			{ID: "p3", Name: "Produk C", Content: "C"},
		},
		AccountProductIDs: map[string][]string{
			"acc-1": {"p2"},
		},
	}
	got := selectProductsForAccount(settings, "acc-1")
	if len(got) != 3 {
		t.Fatalf("expected 3 products, got %d", len(got))
	}
	if got[0].ID != "p2" {
		t.Fatalf("expected mapped product p2 to be prioritized first, got %q", got[0].ID)
	}
}
