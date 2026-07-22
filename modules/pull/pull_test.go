package pull

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestGetBytesReservesOldAndNewBuffersDuringGrowth(t *testing.T) {
	body := make([]byte, 40<<10)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	var peak int64
	data, _, err := client.GetBytesLimitReserve(context.Background(), Request{URL: origin.URL}, 64<<10, func(liveBytes int64) error {
		if liveBytes > peak {
			peak = liveBytes
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(body) {
		t.Fatalf("bytes = %d, want %d", len(data), len(body))
	}
	if want := int64((32 << 10) + (64 << 10)); peak != want {
		t.Fatalf("peak reservation = %d, want %d", peak, want)
	}
}

func TestGetBytesUsesKnownContentLengthAsOneExactAllocation(t *testing.T) {
	body := make([]byte, 40<<10)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	var reservations []int64
	data, _, err := client.GetBytesLimitReserve(context.Background(), Request{URL: origin.URL}, 64<<10, func(liveBytes int64) error {
		reservations = append(reservations, liveBytes)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(body) || cap(data) != len(body) {
		t.Fatalf("buffer = len %d cap %d, want %d/%d", len(data), cap(data), len(body), len(body))
	}
	if len(reservations) != 1 || reservations[0] != int64(len(body)) {
		t.Fatalf("reservations = %v, want [%d]", reservations, len(body))
	}
}
