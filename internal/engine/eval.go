package engine

import (
	"fmt"
	"sort"
)

// handCat ranks Texas Hold'em hand categories. Higher = better.
type handCat uint8

const (
	catHighCard handCat = iota
	catOnePair
	catTwoPair
	catThreeOfAKind
	catStraight
	catFlush
	catFullHouse
	catFourOfAKind
	catStraightFlush // includes royal flush
)

// handRank encodes category + 5 tiebreaker slots into a uint64 that supports
// direct integer comparison (higher value = stronger hand).
//
// Bit layout (MSB to LSB):
//
//	[63:60] category  [59:56] tb[0]  [55:52] tb[1]  [51:48] tb[2]
//	[47:44] tb[3]     [43:40] tb[4]
type handRank uint64

func newHandRank(cat handCat, tb [5]int) handRank {
	v := uint64(cat) << 60
	v |= uint64(tb[0]) << 56
	v |= uint64(tb[1]) << 52
	v |= uint64(tb[2]) << 48
	v |= uint64(tb[3]) << 44
	v |= uint64(tb[4]) << 40
	return handRank(v)
}

// cardRank returns the rank component of a card (0=2 … 12=A).
// Card encoding: card = suit*13 + rank.
func cardRank(c uint32) int { return int(c % 13) }

// cardSuit returns the suit component of a card (0=♣ 1=♦ 2=♥ 3=♠).
func cardSuit(c uint32) int { return int(c / 13) }

var (
	rankSingular = [13]string{"2", "3", "4", "5", "6", "7", "8", "9", "10", "Jack", "Queen", "King", "Ace"}
	rankPlural   = [13]string{"Twos", "Threes", "Fours", "Fives", "Sixes", "Sevens", "Eights", "Nines", "Tens", "Jacks", "Queens", "Kings", "Aces"}
)

func rankName(r int) string {
	if r >= 0 && r < 13 {
		return rankSingular[r]
	}
	return "?"
}

func rankNamePlural(r int) string {
	if r >= 0 && r < 13 {
		return rankPlural[r]
	}
	return "?"
}

// playerHandEval caches the best-hand result for one player at showdown.
type playerHandEval struct {
	rank     handRank
	bestFive [5]uint32
	desc     string
}

// bestHand selects the strongest 5-card hand from the given hole cards and
// board. It tries all C(n,5) combinations where n = 2 + len(board).
// Returns a comparable rank (higher = better), the 5 winning cards, and a
// human-readable description.
func bestHand(c1, c2 uint32, board []uint32) (handRank, [5]uint32, string) {
	pool := make([]uint32, 0, 2+len(board))
	pool = append(pool, c1, c2)
	pool = append(pool, board...)

	n := len(pool)
	if n < 5 {
		// Pre-river evaluation (shouldn't happen at showdown but handled safely).
		var five [5]uint32
		copy(five[:], pool)
		r, desc := eval5(five)
		return r, five, desc
	}

	var (
		best      handRank
		bestCards [5]uint32
		bestDesc  string
		first     = true
	)
	// Enumerate all C(n,5) combinations using an index array.
	idx := [5]int{0, 1, 2, 3, 4}
	for {
		var five [5]uint32
		for i, ix := range idx {
			five[i] = pool[ix]
		}
		r, desc := eval5(five)
		if first || r > best {
			best, bestCards, bestDesc = r, five, desc
			first = false
		}
		// Advance to next combination (standard combinatorial enumeration).
		i := 4
		for i >= 0 && idx[i] == i+n-5 {
			i--
		}
		if i < 0 {
			break
		}
		idx[i]++
		for j := i + 1; j < 5; j++ {
			idx[j] = idx[j-1] + 1
		}
	}
	return best, bestCards, bestDesc
}

// eval5 evaluates exactly 5 cards and returns a comparable rank and a
// human-readable description such as "Full House, Aces full of Kings".
func eval5(cards [5]uint32) (handRank, string) {
	var ranks [5]int
	var suits [5]int
	for i, c := range cards {
		ranks[i] = cardRank(c)
		suits[i] = cardSuit(c)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ranks[:])))

	isFlush := suits[0] == suits[1] && suits[1] == suits[2] &&
		suits[2] == suits[3] && suits[3] == suits[4]
	isStraight, hi := detectStraight(ranks)

	// Build frequency groups: sorted by count desc, then rank desc.
	var freq [13]int
	for _, r := range ranks {
		freq[r]++
	}
	type group struct{ rank, count int }
	groups := make([]group, 0, 5)
	for r, c := range freq {
		if c > 0 {
			groups = append(groups, group{r, c})
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].count != groups[j].count {
			return groups[i].count > groups[j].count
		}
		return groups[i].rank > groups[j].rank
	})

	// Tiebreakers: each group's rank repeated count times (highest count first).
	var tb [5]int
	pos := 0
	for _, g := range groups {
		for range g.count {
			if pos < 5 {
				tb[pos] = g.rank
				pos++
			}
		}
	}

	top := groups[0]
	var second group
	if len(groups) > 1 {
		second = groups[1]
	}

	switch {
	case isFlush && isStraight:
		sfTB := [5]int{hi}
		if hi == 12 {
			return newHandRank(catStraightFlush, sfTB), "Royal Flush"
		}
		return newHandRank(catStraightFlush, sfTB),
			fmt.Sprintf("Straight Flush, %s-high", rankName(hi))

	case top.count == 4:
		return newHandRank(catFourOfAKind, tb),
			fmt.Sprintf("Four %s", rankNamePlural(top.rank))

	case top.count == 3 && second.count == 2:
		return newHandRank(catFullHouse, tb),
			fmt.Sprintf("Full House, %s full of %s", rankNamePlural(top.rank), rankNamePlural(second.rank))

	case isFlush:
		flTB := [5]int{ranks[0], ranks[1], ranks[2], ranks[3], ranks[4]}
		return newHandRank(catFlush, flTB),
			fmt.Sprintf("Flush, %s-high", rankName(ranks[0]))

	case isStraight:
		sTB := [5]int{hi}
		return newHandRank(catStraight, sTB),
			fmt.Sprintf("Straight, %s-high", rankName(hi))

	case top.count == 3:
		return newHandRank(catThreeOfAKind, tb),
			fmt.Sprintf("Three %s", rankNamePlural(top.rank))

	case top.count == 2 && second.count == 2:
		return newHandRank(catTwoPair, tb),
			fmt.Sprintf("Two Pair, %s and %s", rankNamePlural(top.rank), rankNamePlural(second.rank))

	case top.count == 2:
		return newHandRank(catOnePair, tb),
			fmt.Sprintf("Pair of %s", rankNamePlural(top.rank))

	default:
		return newHandRank(catHighCard, tb),
			fmt.Sprintf("High Card, %s", rankName(ranks[0]))
	}
}

// detectStraight reports whether the sorted-descending ranks form a straight,
// returning (true, highRank). Handles the ace-low wheel (A-2-3-4-5 → 5-high).
func detectStraight(ranks [5]int) (bool, int) {
	// Normal straight: range exactly 4 and all values distinct (sorted desc,
	// so adjacent pairs must all differ).
	if ranks[0]-ranks[4] == 4 &&
		ranks[0] != ranks[1] && ranks[1] != ranks[2] &&
		ranks[2] != ranks[3] && ranks[3] != ranks[4] {
		return true, ranks[0]
	}
	// Wheel (A-2-3-4-5): sorted desc → [12, 3, 2, 1, 0].
	if ranks[0] == 12 && ranks[1] == 3 && ranks[2] == 2 && ranks[3] == 1 && ranks[4] == 0 {
		return true, 3 // 5-high: rank 3 = "5"
	}
	return false, 0
}
