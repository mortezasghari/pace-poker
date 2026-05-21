package server

import (
	"context"
	"io"
	"testing"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/auth"
	"google.golang.org/grpc/metadata"
)

// ── fakeSubmitter ─────────────────────────────────────────────────────────────

type fakeSubmitter struct {
	events           []*pb.GameEvent
	err              error
	invalidateCalled []uuid.UUID
}

func (f *fakeSubmitter) Submit(_ context.Context, _ uuid.UUID, _ string, _ *pb.PlayerCommand) ([]*pb.GameEvent, error) {
	return f.events, f.err
}

func (f *fakeSubmitter) Invalidate(gameID uuid.UUID) {
	f.invalidateCalled = append(f.invalidateCalled, gameID)
}

// ── fakePlayStream ────────────────────────────────────────────────────────────

type fakePlayStream struct {
	ctx  context.Context
	cmds []*pb.PlayerCommand
	pos  int
	sent []*pb.GameEvent
}

func (f *fakePlayStream) Recv() (*pb.PlayerCommand, error) {
	if f.pos >= len(f.cmds) {
		return nil, io.EOF
	}
	cmd := f.cmds[f.pos]
	f.pos++
	return cmd, nil
}

func (f *fakePlayStream) Send(ev *pb.GameEvent) error {
	f.sent = append(f.sent, ev)
	return nil
}

func (f *fakePlayStream) Context() context.Context         { return f.ctx }
func (f *fakePlayStream) SetHeader(metadata.MD) error      { return nil }
func (f *fakePlayStream) SendHeader(metadata.MD) error     { return nil }
func (f *fakePlayStream) SetTrailer(metadata.MD)           {}
func (f *fakePlayStream) SendMsg(any) error                { return nil }
func (f *fakePlayStream) RecvMsg(any) error                { return nil }

// ── fixed test UUIDs ─────────────────────────────────────────────────────────

var (
	aliceID = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001")
	bobID   = uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002")
)

// ── helpers ───────────────────────────────────────────────────────────────────

func holeCardsRevealedEvent(playerID string) *pb.GameEvent {
	return &pb.GameEvent{
		Event: &pb.GameEvent_HoleCardsRevealed{
			HoleCardsRevealed: &pb.HoleCardsRevealed{
				PlayerId: playerID,
				Card_1:   0,
				Card_2:   1,
			},
		},
	}
}

func holeCardsDealtEvent(playerID string) *pb.GameEvent {
	return &pb.GameEvent{
		Event: &pb.GameEvent_HoleCardsDealt{
			HoleCardsDealt: &pb.HoleCardsDealt{PlayerId: playerID},
		},
	}
}

func potAwardedEvent() *pb.GameEvent {
	return &pb.GameEvent{
		Event: &pb.GameEvent_PotAwarded{
			PotAwarded: &pb.PotAwarded{Amount: 100, WinnerPlayerIds: []string{"alice"}},
		},
	}
}

// ── shouldSuppressFromActor ───────────────────────────────────────────────────

func TestShouldSuppressFromActor(t *testing.T) {
	aliceStr := aliceID.String()
	bobStr := bobID.String()
	tests := []struct {
		name      string
		ev        *pb.GameEvent
		actor     string
		wantSuppr bool
	}{
		{
			name:      "HoleCardsRevealed for self — not suppressed",
			ev:        holeCardsRevealedEvent(aliceStr),
			actor:     aliceStr,
			wantSuppr: false,
		},
		{
			name:      "HoleCardsRevealed for opponent — suppressed",
			ev:        holeCardsRevealedEvent(bobStr),
			actor:     aliceStr,
			wantSuppr: true,
		},
		{
			name:      "HoleCardsDealt (broadcast) — never suppressed",
			ev:        holeCardsDealtEvent(bobStr),
			actor:     aliceStr,
			wantSuppr: false,
		},
		{
			name:      "PotAwarded — never suppressed",
			ev:        potAwardedEvent(),
			actor:     aliceStr,
			wantSuppr: false,
		},
		{
			name:      "HoleCardsRevealed empty actor — suppressed when player id is non-empty",
			ev:        holeCardsRevealedEvent(bobStr),
			actor:     "",
			wantSuppr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldSuppressFromActor(tc.ev, tc.actor)
			if got != tc.wantSuppr {
				t.Errorf("shouldSuppressFromActor() = %v, want %v", got, tc.wantSuppr)
			}
		})
	}
}

// ── PlaySession filtering ─────────────────────────────────────────────────────

func newPlaySession(actorID uuid.UUID, sub CommandSubmitter, cmds []*pb.PlayerCommand) (*Server, *fakePlayStream) {
	s := &Server{router: sub}
	stream := &fakePlayStream{
		ctx:  contextWithActor(context.Background(), actorID),
		cmds: cmds,
	}
	return s, stream
}

// contextWithActor injects a Principal into the context, simulating what the
// auth interceptor does for real requests.
func contextWithActor(ctx context.Context, actorID uuid.UUID) context.Context {
	return auth.WithPrincipal(ctx, auth.Principal{UserID: actorID})
}

func TestPlaySession_FiltersOpponentHoleCards(t *testing.T) {
	gameID := uuid.New().String()
	sub := &fakeSubmitter{
		events: []*pb.GameEvent{
			holeCardsDealtEvent(aliceID.String()),   // broadcast — alice gets this
			holeCardsRevealedEvent(aliceID.String()), // alice's own cards — alice gets this
			holeCardsDealtEvent(bobID.String()),     // broadcast — alice gets this
			holeCardsRevealedEvent(bobID.String()),  // bob's cards — alice must NOT get this
			potAwardedEvent(),                       // broadcast — alice gets this
		},
	}

	cmd := &pb.PlayerCommand{GameId: gameID}
	s, stream := newPlaySession(aliceID, sub, []*pb.PlayerCommand{cmd})

	if err := s.PlaySession(stream); err != nil {
		t.Fatalf("PlaySession error: %v", err)
	}

	// alice should receive 4 events (all except bob's HoleCardsRevealed)
	if len(stream.sent) != 4 {
		t.Fatalf("got %d events, want 4", len(stream.sent))
	}

	for _, ev := range stream.sent {
		if r, ok := ev.Event.(*pb.GameEvent_HoleCardsRevealed); ok {
			if r.HoleCardsRevealed.GetPlayerId() != aliceID.String() {
				t.Errorf("alice received HoleCardsRevealed for player %q", r.HoleCardsRevealed.GetPlayerId())
			}
		}
	}
}

func TestPlaySession_ReceivesOwnHoleCards(t *testing.T) {
	gameID := uuid.New().String()
	sub := &fakeSubmitter{
		events: []*pb.GameEvent{
			holeCardsRevealedEvent(aliceID.String()),
		},
	}

	cmd := &pb.PlayerCommand{GameId: gameID}
	s, stream := newPlaySession(aliceID, sub, []*pb.PlayerCommand{cmd})

	if err := s.PlaySession(stream); err != nil {
		t.Fatalf("PlaySession error: %v", err)
	}

	if len(stream.sent) != 1 {
		t.Fatalf("got %d events, want 1", len(stream.sent))
	}
	r, ok := stream.sent[0].Event.(*pb.GameEvent_HoleCardsRevealed)
	if !ok {
		t.Fatal("expected HoleCardsRevealed")
	}
	if r.HoleCardsRevealed.GetPlayerId() != aliceID.String() {
		t.Errorf("wrong player id: %q", r.HoleCardsRevealed.GetPlayerId())
	}
}

func TestPlaySession_NonHoleCardEventsPassThrough(t *testing.T) {
	gameID := uuid.New().String()
	sub := &fakeSubmitter{
		events: []*pb.GameEvent{
			potAwardedEvent(),
			holeCardsDealtEvent(bobID.String()),
			holeCardsDealtEvent(aliceID.String()),
		},
	}

	cmd := &pb.PlayerCommand{GameId: gameID}
	s, stream := newPlaySession(aliceID, sub, []*pb.PlayerCommand{cmd})

	if err := s.PlaySession(stream); err != nil {
		t.Fatalf("PlaySession error: %v", err)
	}

	if len(stream.sent) != 3 {
		t.Fatalf("got %d events, want 3", len(stream.sent))
	}
}