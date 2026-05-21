package server

import (
	"context"

	"github.com/google/uuid"
	"github.com/pacepoker/poker/internal/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// actorFromContext returns the authenticated user's internal UUID from the
// request context. Returns Unauthenticated if the interceptor didn't run
// (which is a programming error in any non-public RPC).
func actorFromContext(ctx context.Context) (uuid.UUID, error) {
	p, err := auth.FromContext(ctx)
	if err != nil {
		return uuid.Nil, status.Error(codes.Unauthenticated, "no authenticated user")
	}
	return p.UserID, nil
}
