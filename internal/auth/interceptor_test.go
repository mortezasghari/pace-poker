package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	jwxjwt "github.com/lestrrat-go/jwx/v2/jwt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/pacepoker/poker/internal/config"
)

// fakeStream is a minimal grpc.ServerStream for testing the stream interceptor.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

// incomingCtx returns a context carrying the given bearer token in gRPC metadata.
func incomingCtx(token string) context.Context {
	md := metadata.New(map[string]string{"authorization": "Bearer " + token})
	return metadata.NewIncomingContext(context.Background(), md)
}

// incomingCtxHeader returns a context carrying a raw header value.
func incomingCtxHeader(key, val string) context.Context {
	md := metadata.New(map[string]string{key: val})
	return metadata.NewIncomingContext(context.Background(), md)
}

// expectCode fails the test if the error's gRPC status code doesn't match.
func expectCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", code)
	}
	if s, ok := status.FromError(err); !ok || s.Code() != code {
		t.Fatalf("got %v, want code %v", err, code)
	}
}

// nopHandler is a grpc.UnaryHandler that records whether it was called.
func nopHandler(called *bool) grpc.UnaryHandler {
	return func(ctx context.Context, _ any) (any, error) {
		*called = true
		return "ok", nil
	}
}

func TestUnaryInterceptor_RejectsMissingAuthHeader(t *testing.T) {
	rig := newTestRig(t)
	interceptor := rig.auth.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/poker.v1.PokerService/JoinTable"}

	// context with no metadata at all
	_, err := interceptor(context.Background(), nil, info, nopHandler(new(bool)))
	expectCode(t, err, codes.Unauthenticated)
}

func TestUnaryInterceptor_RejectsMalformedHeader(t *testing.T) {
	rig := newTestRig(t)
	interceptor := rig.auth.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/poker.v1.PokerService/JoinTable"}

	ctx := incomingCtxHeader("authorization", "NotBearer xyz")
	_, err := interceptor(ctx, nil, info, nopHandler(new(bool)))
	expectCode(t, err, codes.Unauthenticated)
}

func TestUnaryInterceptor_RejectsInvalidToken(t *testing.T) {
	rig := newTestRig(t)
	interceptor := rig.auth.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/poker.v1.PokerService/JoinTable"}

	ctx := incomingCtx("this.is.garbage")
	_, err := interceptor(ctx, nil, info, nopHandler(new(bool)))
	expectCode(t, err, codes.Unauthenticated)
}

func TestUnaryInterceptor_AcceptsValidToken_AttachesPrincipal(t *testing.T) {
	rig := newTestRig(t)
	interceptor := rig.auth.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/poker.v1.PokerService/JoinTable"}

	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Subject("auth0|principal-test")
	})

	var capturedCtx context.Context
	handler := func(ctx context.Context, _ any) (any, error) {
		capturedCtx = ctx
		return "ok", nil
	}

	_, err := interceptor(incomingCtx(token), nil, info, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, err := FromContext(capturedCtx)
	if err != nil {
		t.Fatalf("FromContext: %v", err)
	}
	if p.ExternalID != "auth0|principal-test" {
		t.Errorf("ExternalID: got %q, want auth0|principal-test", p.ExternalID)
	}
}

func TestUnaryInterceptor_PublicMethodSkipsAuth(t *testing.T) {
	rig := newTestRig(t)
	interceptor := rig.auth.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}

	called := false
	// No token, no metadata — public method must still pass through.
	_, err := interceptor(context.Background(), nil, info, nopHandler(&called))
	if err != nil {
		t.Fatalf("unexpected error for public method: %v", err)
	}
	if !called {
		t.Error("handler not called for public method")
	}
}

func TestUnaryInterceptor_DevModeStub_OnlyWorksWhenEnabled(t *testing.T) {
	rig := newTestRig(t)
	devID := uuid.New()
	ctx := incomingCtxHeader("x-dev-player-id", devID.String())
	info := &grpc.UnaryServerInfo{FullMethod: "/poker.v1.PokerService/JoinTable"}

	// Dev mode OFF (default in rig) — stub header should not authenticate.
	interceptorOff := rig.auth.UnaryInterceptor()
	_, err := interceptorOff(ctx, nil, info, nopHandler(new(bool)))
	expectCode(t, err, codes.Unauthenticated)

	// Dev mode ON — stub header should produce a valid principal.
	cfgDev := config.Auth0Config{
		Domain:               "test.auth0.local",
		Audience:             testAudience,
		ClaimsNamespace:      testNS,
		DevModeAllowStubAuth: true,
	}
	devAuth := newAuthenticatorWithValidator(cfgDev, rig.auth.validator, rig.st, nil)
	interceptorOn := devAuth.UnaryInterceptor()

	var capturedCtx context.Context
	handler := func(ctx context.Context, _ any) (any, error) {
		capturedCtx = ctx
		return "ok", nil
	}
	_, err = interceptorOn(ctx, nil, info, handler)
	if err != nil {
		t.Fatalf("dev mode: unexpected error: %v", err)
	}
	p, _ := FromContext(capturedCtx)
	if p.UserID != devID {
		t.Errorf("dev mode UserID: got %v, want %v", p.UserID, devID)
	}
}

func TestStreamInterceptor_AcceptsValidToken(t *testing.T) {
	rig := newTestRig(t)
	interceptor := rig.auth.StreamInterceptor()
	info := &grpc.StreamServerInfo{FullMethod: "/poker.v1.PokerService/PlaySession"}

	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Subject("auth0|stream-test")
	})

	streamCtx := incomingCtx(token)
	stream := &fakeStream{ctx: streamCtx}

	var capturedCtx context.Context
	handler := func(_ any, ss grpc.ServerStream) error {
		capturedCtx = ss.Context()
		return nil
	}

	if err := interceptor(nil, stream, info, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p, err := FromContext(capturedCtx)
	if err != nil {
		t.Fatalf("FromContext: %v", err)
	}
	if p.ExternalID != "auth0|stream-test" {
		t.Errorf("ExternalID: got %q, want auth0|stream-test", p.ExternalID)
	}
}

// Ensure fakeStream satisfies grpc.ServerStream via the embedded field.
// This also tests that wrappedStream's Context() override works.
func TestStreamInterceptor_WrappedStreamContextOverride(t *testing.T) {
	rig := newTestRig(t)
	interceptor := rig.auth.StreamInterceptor()
	info := &grpc.StreamServerInfo{FullMethod: "/poker.v1.PokerService/PlaySession"}

	token := rig.signToken(t)

	stream := &fakeStream{ctx: incomingCtx(token)}
	var innerStream grpc.ServerStream
	handler := func(_ any, ss grpc.ServerStream) error {
		innerStream = ss
		return nil
	}

	if err := interceptor(nil, stream, info, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The stream the handler received must be a wrappedStream with the
	// principal in its context.
	_, err := FromContext(innerStream.Context())
	if err != nil {
		t.Errorf("handler's stream context has no principal: %v", err)
	}
}

// Compile-time: fakeStream must implement grpc.ServerStream via embedding.
var _ grpc.ServerStream = (*fakeStream)(nil)

// Verify the jwa import is used in this file (via signToken options).
var _ = jwa.RS256
