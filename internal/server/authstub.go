package server

import (
	"context"

	"google.golang.org/grpc/metadata"
)

// actorPlayerIDFromContext extracts the player identity from gRPC metadata.
//
// INSECURE stub for development only. Real auth replaces this with a verified
// claim from a JWT or session token. Never trust client-supplied identity in
// production without verification.
func actorPlayerIDFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("x-player-id")
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}