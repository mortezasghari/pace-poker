package engine

import (
	"testing"

	"github.com/pacepoker/poker/internal/deck"
)

func TestCryptoDealer_ShuffleAndDeal(t *testing.T) {
	d := NewCryptoDealer()

	if err := d.Shuffle(); err != nil {
		t.Fatalf("Shuffle: %v", err)
	}
	if d.Remaining() != deck.Size {
		t.Fatalf("Remaining after Shuffle: got %d, want %d", d.Remaining(), deck.Size)
	}

	c, err := d.Deal()
	if err != nil {
		t.Fatalf("Deal: %v", err)
	}
	if c == deck.NoCard {
		t.Error("Deal returned NoCard sentinel")
	}
	if d.Remaining() != deck.Size-1 {
		t.Errorf("Remaining after one Deal: got %d, want %d", d.Remaining(), deck.Size-1)
	}
}

func TestCryptoDealer_Burn(t *testing.T) {
	d := NewCryptoDealer()
	if err := d.Shuffle(); err != nil {
		t.Fatal(err)
	}
	if err := d.Burn(); err != nil {
		t.Fatalf("Burn: %v", err)
	}
	if d.Remaining() != deck.Size-1 {
		t.Errorf("Remaining after Burn: got %d, want %d", d.Remaining(), deck.Size-1)
	}
}

func TestCryptoDealer_ReshuffleResetsPosition(t *testing.T) {
	d := NewCryptoDealer()
	if err := d.Shuffle(); err != nil {
		t.Fatal(err)
	}
	// Exhaust a few cards.
	for range 10 {
		if _, err := d.Deal(); err != nil {
			t.Fatal(err)
		}
	}
	// Re-shuffle must restore all 52 cards.
	if err := d.Shuffle(); err != nil {
		t.Fatal(err)
	}
	if d.Remaining() != deck.Size {
		t.Errorf("Remaining after re-Shuffle: got %d, want %d", d.Remaining(), deck.Size)
	}
}

func TestFixedDealer_DealsInOrder(t *testing.T) {
	cards := []deck.Card{0, 1, 2, 3, 4}
	d := NewFixedDealer(cards)

	if err := d.Shuffle(); err != nil {
		t.Fatal(err)
	}
	if d.Remaining() != len(cards) {
		t.Fatalf("Remaining: got %d, want %d", d.Remaining(), len(cards))
	}

	for i, want := range cards {
		got, err := d.Deal()
		if err != nil {
			t.Fatalf("Deal[%d]: %v", i, err)
		}
		if got != want {
			t.Errorf("Deal[%d]: got %d, want %d", i, got, want)
		}
	}

	if _, err := d.Deal(); err == nil {
		t.Error("expected error when dealing past end of fixed deck")
	}
}

func TestFixedDealer_BurnAdvancesPosition(t *testing.T) {
	cards := []deck.Card{10, 20, 30}
	d := NewFixedDealer(cards)

	if err := d.Burn(); err != nil {
		t.Fatal(err)
	}
	got, err := d.Deal()
	if err != nil {
		t.Fatal(err)
	}
	if got != 20 {
		t.Errorf("after Burn, Deal returned %d, want 20", got)
	}
}

func TestFixedDealer_ShuffleResetsPosition(t *testing.T) {
	cards := []deck.Card{5, 6, 7}
	d := NewFixedDealer(cards)

	if _, err := d.Deal(); err != nil {
		t.Fatal(err)
	}
	if err := d.Shuffle(); err != nil {
		t.Fatal(err)
	}
	got, err := d.Deal()
	if err != nil {
		t.Fatal(err)
	}
	if got != 5 {
		t.Errorf("after Shuffle reset, Deal returned %d, want 5", got)
	}
}