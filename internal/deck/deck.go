package deck

import (
	"crypto/rand"
	"errors"
	"math/big"
)

// Card is a value in [0, 51].
// Encoding: card = suit*13 + rank
// rank ∈ [0..12] → 2,3,4,5,6,7,8,9,10,J,Q,K,A
// suit ∈ [0..3]  → ♣ ♦ ♥ ♠
type Card uint8

const (
	NoCard Card = 255
	Size        = 52
)

// Deck is an ordered sequence of cards.
type Deck struct {
	cards []Card
	pos   int // next card to draw
}

// NewDeck returns an unshuffled 52-card deck (2♣ … A♠).
func NewDeck() *Deck {
	cards := make([]Card, Size)
	for i := range cards {
		cards[i] = Card(i)
	}
	return &Deck{cards: cards}
}

// CryptoShuffle shuffles the deck using crypto/rand (Fisher-Yates).
func (d *Deck) CryptoShuffle() error {
	d.pos = 0
	n := len(d.cards)
	for i := n - 1; i > 0; i-- {
		jBig, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		j := int(jBig.Int64())
		d.cards[i], d.cards[j] = d.cards[j], d.cards[i]
	}
	return nil
}

// Draw pops and returns the next card.
func (d *Deck) Draw() (Card, error) {
	if d.pos >= len(d.cards) {
		return NoCard, errors.New("deck: no cards remaining")
	}
	c := d.cards[d.pos]
	d.pos++
	return c, nil
}

// Remaining returns how many cards are left to draw.
func (d *Deck) Remaining() int {
	return len(d.cards) - d.pos
}