package auth

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// PublicMethods lists RPC full-method names that skip authentication.
// Format: "/<package>.<Service>/<Method>".
var PublicMethods = map[string]bool{
	"/grpc.health.v1.Health/Check":                           true,
	"/grpc.health.v1.Health/Watch":                           true,
	"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo": true,
}

// UnaryInterceptor returns a gRPC unary server interceptor that authenticates
// every request not in PublicMethods.
func (a *Authenticator) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if PublicMethods[info.FullMethod] {
			return handler(ctx, req)
		}
		newCtx, err := a.authContext(ctx)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// StreamInterceptor returns a gRPC stream server interceptor that authenticates
// every stream not in PublicMethods.
func (a *Authenticator) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if PublicMethods[info.FullMethod] {
			return handler(srv, ss)
		}
		newCtx, err := a.authContext(ss.Context())
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: newCtx})
	}
}

// wrappedStream replaces the context on a grpc.ServerStream.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// authContext extracts and validates credentials, returning an enriched context.
// Consults the dev stub first (only when DevModeAllowStubAuth is true).
func (a *Authenticator) authContext(ctx context.Context) (context.Context, error) {
	if a.cfg.DevModeAllowStubAuth {
		if p, ok := tryDevStub(ctx); ok {
			return WithPrincipal(ctx, p), nil
		}
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization header")
	}
	raw := vals[0]
	token := strings.TrimPrefix(raw, "Bearer ")
	if token == raw || token == "" {
		return nil, status.Error(codes.Unauthenticated, "malformed authorization header")
	}

	p, err := a.Verify(ctx, token)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
	}
	return WithPrincipal(ctx, p), nil
}

// tryDevStub reads x-dev-player-id from metadata. Only called when
// DevModeAllowStubAuth is true. The value must be a valid UUID.
func tryDevStub(ctx context.Context) (Principal, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return Principal{}, false
	}
	vals := md.Get("x-dev-player-id")
	if len(vals) == 0 {
		return Principal{}, false
	}
	id, err := uuid.Parse(vals[0])
	if err != nil {
		return Principal{}, false
	}
	return Principal{UserID: id, ExternalID: "dev:" + vals[0]}, true
}
