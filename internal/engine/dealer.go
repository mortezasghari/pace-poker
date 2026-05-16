package engine

import (
	"errors"

	"github.com/pacepoker/poker/internal/deck"
)

// Dealer is the source of cards for a hand. It abstracts shuffling so that
// production code uses crypto/rand and tests use a deterministic sequence.
//
// A Dealer is bound to one session. Shuffle is called once per hand start.
type Dealer interface {
	// Shuffle prepares a fresh 52-card deck. Calling it again re-shuffles.
	Shuffle() error
	// Deal pops the next card off the deck.
	Deal() (deck.Card, error)
	// Burn discards the next card (standard poker procedure before each board street).
	Burn() error
	// Remaining returns how many cards are left.
	Remaining() int
}

// CryptoDealer is the production dealer. Uses crypto/rand via deck.CryptoShuffle.
type CryptoDealer struct {
	d *deck.Deck
}

func NewCryptoDealer() *CryptoDealer { return &CryptoDealer{d: deck.NewDeck()} }

func (c *CryptoDealer) Shuffle() error {
	c.d = deck.NewDeck()
	return c.d.CryptoShuffle()
}

func (c *CryptoDealer) Deal() (deck.Card, error) { return c.d.Draw() }

func (c *CryptoDealer) Burn() error {
	_, err := c.d.Draw()
	return err
}

func (c *CryptoDealer) Remaining() int { return c.d.Remaining() }

// FixedDealer deals cards in a pre-specified order. For tests and replay.
type FixedDealer struct {
	cards []deck.Card
	pos   int
}

func NewFixedDealer(cards []deck.Card) *FixedDealer {
	return &FixedDealer{cards: cards}
}

func (f *FixedDealer) Shuffle() error { f.pos = 0; return nil }

func (f *FixedDealer) Deal() (deck.Card, error) {
	if f.pos >= len(f.cards) {
		return 0, errors.New("fixed dealer: out of cards")
	}
	c := f.cards[f.pos]
	f.pos++
	return c, nil
}

func (f *FixedDealer) Burn() error {
	if f.pos >= len(f.cards) {
		return errors.New("fixed dealer: out of cards")
	}
	f.pos++
	return nil
}

func (f *FixedDealer) Remaining() int { return len(f.cards) - f.pos }