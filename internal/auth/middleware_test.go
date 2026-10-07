package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

const (
	testAudience = "https://api.sprout.test"
	testSubject  = "apple|001234.abcd"
)

// signingKey is an RSA key pair with the key id it is published under.
type signingKey struct {
	private jwk.Key
	public  jwk.Key
}

func newSigningKey(t *testing.T, kid string) signingKey {
	t.Helper()
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	private, err := jwk.Import(raw)
	require.NoError(t, err)
	require.NoError(t, private.Set(jwk.KeyIDKey, kid))

	public, err := private.PublicKey()
	require.NoError(t, err)
	require.NoError(t, public.Set(jwk.AlgorithmKey, jwa.RS256()))
	require.NoError(t, public.Set(jwk.KeyUsageKey, "sig"))
	return signingKey{private: private, public: public}
}

// issuer is a stand-in for the Auth0 tenant: it serves the discovery
// document and the key set, and its keys can be swapped or taken offline.
type issuer struct {
	url string // as it appears in the iss claim, with the trailing slash

	mu          sync.Mutex
	keys        []signingKey
	down        bool
	keysFetches atomic.Int32
}

func newIssuer(t *testing.T, keys ...signingKey) *issuer {
	t.Helper()
	iss := &issuer{keys: keys}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		if iss.isDown() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   iss.url,
			"jwks_uri": iss.url + ".well-known/jwks.json",
		})
	})
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		if iss.isDown() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		iss.keysFetches.Add(1)
		set := jwk.NewSet()
		iss.mu.Lock()
		for _, key := range iss.keys {
			_ = set.AddKey(key.public)
		}
		iss.mu.Unlock()
		_ = json.NewEncoder(w).Encode(set)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	iss.url = srv.URL + "/"
	return iss
}

func (i *issuer) isDown() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.down
}

func (i *issuer) setDown(down bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.down = down
}

func (i *issuer) setKeys(keys ...signingKey) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.keys = keys
}

// tokenSpec describes a token to sign. The zero value of a field means the
// valid default.
type tokenSpec struct {
	issuer    string
	audience  string
	subject   string
	noSubject bool
	expiresAt time.Time
	notBefore time.Time
	claims    map[string]any
}

func (i *issuer) sign(t *testing.T, key signingKey, spec tokenSpec) string {
	t.Helper()
	now := time.Now()
	if spec.issuer == "" {
		spec.issuer = i.url
	}
	if spec.audience == "" {
		spec.audience = testAudience
	}
	if spec.subject == "" && !spec.noSubject {
		spec.subject = testSubject
	}
	if spec.expiresAt.IsZero() {
		spec.expiresAt = now.Add(time.Hour)
	}

	b := jwt.NewBuilder().
		Issuer(spec.issuer).
		Audience([]string{spec.audience}).
		IssuedAt(now.Add(-time.Minute)).
		Expiration(spec.expiresAt)
	if spec.subject != "" {
		b = b.Subject(spec.subject)
	}
	if !spec.notBefore.IsZero() {
		b = b.NotBefore(spec.notBefore)
	}
	for name, value := range spec.claims {
		b = b.Claim(name, value)
	}
	token, err := b.Build()
	require.NoError(t, err)

	signed, err := jwt.Sign(token, jwt.WithKey(jwa.RS256(), key.private))
	require.NoError(t, err)
	return string(signed)
}

func newVerifier(t *testing.T, iss *issuer, opts ...auth.Option) *auth.JWKSVerifier {
	t.Helper()
	v, err := auth.NewVerifier(iss.url, testAudience, opts...)
	require.NoError(t, err)
	return v
}

// protected is a handler behind the middleware that answers with the claims
// it was given.
func protected(t *testing.T, verifier auth.Verifier) http.Handler {
	t.Helper()
	logger := testutil.Logger(t)
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, claims)
	})
	return auth.Middleware(verifier, httpx.NewResponder(logger), logger)(echo)
}

func request(authorization string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req
}

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func requireError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, rec.Code, "body: %s", rec.Body.String())
	env := testutil.DecodeJSON[errorEnvelope](t, rec)
	assert.Equal(t, code, env.Error.Code)
	assert.NotEmpty(t, env.Error.Message)
}

func TestMiddleware_ValidToken(t *testing.T) {
	key := newSigningKey(t, "key-1")
	iss := newIssuer(t, key)
	handler := protected(t, newVerifier(t, iss))

	t.Run("claims reach the handler", func(t *testing.T) {
		token := iss.sign(t, key, tokenSpec{claims: map[string]any{
			auth.ClaimsNamespace + "email": "sam@example.com",
			auth.ClaimsNamespace + "name":  "Sam Example",
			"email":                        "not-namespaced@example.com",
		}})

		rec := testutil.Do(handler, request("Bearer "+token))

		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t,
			auth.Claims{Subject: testSubject, Email: "sam@example.com", Name: "Sam Example"},
			testutil.DecodeJSON[auth.Claims](t, rec))
	})

	t.Run("email and name are optional", func(t *testing.T) {
		rec := testutil.Do(handler, request("Bearer "+iss.sign(t, key, tokenSpec{})))

		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, auth.Claims{Subject: testSubject}, testutil.DecodeJSON[auth.Claims](t, rec))
	})

	t.Run("the scheme is case-insensitive", func(t *testing.T) {
		rec := testutil.Do(handler, request("bearer "+iss.sign(t, key, tokenSpec{})))

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("a token within the clock skew of its expiry", func(t *testing.T) {
		token := iss.sign(t, key, tokenSpec{expiresAt: time.Now().Add(-5 * time.Second)})

		rec := testutil.Do(handler, request("Bearer "+token))

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestMiddleware_Rejects(t *testing.T) {
	key := newSigningKey(t, "key-1")
	// Same key id, different key: what a forged token looks like.
	forged := newSigningKey(t, "key-1")
	unpublished := newSigningKey(t, "key-9")
	iss := newIssuer(t, key)
	handler := protected(t, newVerifier(t, iss))

	hs256, err := jwt.Sign(
		must(jwt.NewBuilder().Issuer(iss.url).Audience([]string{testAudience}).Subject(testSubject).
			Expiration(time.Now().Add(time.Hour)).Build()),
		jwt.WithKey(jwa.HS256(), []byte("a-shared-secret-of-sufficient-length")))
	require.NoError(t, err)
	valid := iss.sign(t, key, tokenSpec{})

	tests := []struct {
		name          string
		authorization string
	}{
		{"missing header", ""},
		{"empty token", "Bearer "},
		{"another scheme", "Basic " + valid},
		{"token without a scheme", valid},
		{"not a JWT", "Bearer not-a-token"},
		{"expired", "Bearer " + iss.sign(t, key, tokenSpec{expiresAt: time.Now().Add(-time.Hour)})},
		{"not valid yet", "Bearer " + iss.sign(t, key, tokenSpec{notBefore: time.Now().Add(time.Hour)})},
		{"wrong audience", "Bearer " + iss.sign(t, key, tokenSpec{audience: "https://another-api.test"})},
		{"wrong issuer", "Bearer " + iss.sign(t, key, tokenSpec{issuer: "https://another-tenant.test/"})},
		{"issuer without the trailing slash", "Bearer " + iss.sign(t, key, tokenSpec{issuer: iss.url[:len(iss.url)-1]})},
		{"bad signature", "Bearer " + iss.sign(t, forged, tokenSpec{})},
		{"tampered payload", "Bearer " + spliceSignature(t, iss.sign(t, forged, tokenSpec{subject: "apple|someone-else"}), valid)},
		{"signed by an unpublished key", "Bearer " + iss.sign(t, unpublished, tokenSpec{})},
		{"HS256 instead of RS256", "Bearer " + string(hs256)},
		{"no subject", "Bearer " + iss.sign(t, key, tokenSpec{noSubject: true})},
		{"machine-to-machine token", "Bearer " + iss.sign(t, key, tokenSpec{subject: "aBcD1234@clients"})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := testutil.Do(handler, request(tt.authorization))

			requireError(t, rec, http.StatusUnauthorized, "unauthenticated")
			assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
		})
	}

	// The control: the same handler does accept a good token.
	require.Equal(t, http.StatusOK, testutil.Do(handler, request("Bearer "+valid)).Code)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// spliceSignature returns payloadFrom's header and payload with
// signatureFrom's signature: a token whose claims were altered after signing.
func spliceSignature(t *testing.T, payloadFrom, signatureFrom string) string {
	t.Helper()
	payload, signature := strings.Split(payloadFrom, "."), strings.Split(signatureFrom, ".")
	require.Len(t, payload, 3)
	require.Len(t, signature, 3)
	return payload[0] + "." + payload[1] + "." + signature[2]
}

func TestVerifier_CachesTheKeys(t *testing.T) {
	key := newSigningKey(t, "key-1")
	iss := newIssuer(t, key)
	verifier := newVerifier(t, iss)

	for range 5 {
		_, err := verifier.Verify(context.Background(), iss.sign(t, key, tokenSpec{}))
		require.NoError(t, err)
	}

	assert.EqualValues(t, 1, iss.keysFetches.Load())
}

func TestVerifier_PicksUpRotatedKeys(t *testing.T) {
	old, next := newSigningKey(t, "key-1"), newSigningKey(t, "key-2")
	iss := newIssuer(t, old)
	verifier := newVerifier(t, iss, auth.WithKeysTTL(50*time.Millisecond))
	ctx := context.Background()

	_, err := verifier.Verify(ctx, iss.sign(t, old, tokenSpec{}))
	require.NoError(t, err)

	iss.setKeys(next)

	token := iss.sign(t, next, tokenSpec{})
	require.Eventually(t, func() bool {
		_, err := verifier.Verify(ctx, token)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "the new key was never fetched")

	_, err = verifier.Verify(ctx, iss.sign(t, old, tokenSpec{}))
	assert.ErrorIs(t, err, auth.ErrInvalidToken, "a retired key no longer signs valid tokens")
}

func TestMiddleware_KeysUnavailable(t *testing.T) {
	key := newSigningKey(t, "key-1")
	iss := newIssuer(t, key)
	iss.setDown(true)
	handler := protected(t, newVerifier(t, iss))
	token := "Bearer " + iss.sign(t, key, tokenSpec{})

	rec := testutil.Do(handler, request(token))
	requireError(t, rec, http.StatusServiceUnavailable, "unavailable")

	// A request with no usable token is turned away without asking the tenant.
	requireError(t, testutil.Do(handler, request("")), http.StatusUnauthorized, "unauthenticated")

	iss.setDown(false)
	assert.Equal(t, http.StatusOK, testutil.Do(handler, request(token)).Code, "recovers once the tenant is back")
}

func TestVerifier_ErrorsDoNotQuoteTheToken(t *testing.T) {
	key := newSigningKey(t, "key-1")
	iss := newIssuer(t, key)
	token := iss.sign(t, key, tokenSpec{expiresAt: time.Now().Add(-time.Hour)})

	_, err := newVerifier(t, iss).Verify(context.Background(), token)

	require.ErrorIs(t, err, auth.ErrInvalidToken)
	assert.Equal(t, "auth: invalid token: token_expired", err.Error())
}

func TestClaimsFromContext_OutsideARequest(t *testing.T) {
	_, ok := auth.ClaimsFromContext(context.Background())

	assert.False(t, ok)
}

// TestServer_RequiresAToken runs the real server with the middleware and the
// test verifier, the way feature tests will.
func TestServer_RequiresAToken(t *testing.T) {
	logger := testutil.Logger(t)
	server := httpx.NewServer(logger,
		httpx.WithAPIMiddleware(auth.Middleware(testutil.TokenVerifier(), httpx.NewResponder(logger), logger)))
	claims := auth.Claims{Subject: testSubject, Email: "sam@example.com", Name: "Sam Example"}

	t.Run("no token", func(t *testing.T) {
		rec := testutil.Do(server.Handler(), testutil.JSONRequest(t, http.MethodGet, "/v1/me", nil))

		requireError(t, rec, http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("a token the test verifier did not mint", func(t *testing.T) {
		req := testutil.JSONRequest(t, http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.e30.c2ln")

		requireError(t, testutil.Do(server.Handler(), req), http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("the token is checked before the request is read", func(t *testing.T) {
		rec := testutil.Do(server.Handler(), testutil.JSONRequest(t, http.MethodGet, "/v1/goals/not-a-uuid", nil))

		requireError(t, rec, http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("authenticated", func(t *testing.T) {
		rec := testutil.Do(server.Handler(), testutil.AuthedRequest(t, claims, http.MethodGet, "/v1/me", nil))

		requireError(t, rec, http.StatusNotImplemented, "not_implemented")
	})

	t.Run("health is open", func(t *testing.T) {
		rec := testutil.Do(server.Handler(), testutil.JSONRequest(t, http.MethodGet, "/healthz", nil))

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestTokenVerifier_RoundTripsClaims(t *testing.T) {
	want := auth.Claims{Subject: testSubject, Email: "sam@example.com", Name: "Sam Example"}

	got, err := testutil.TokenVerifier().Verify(context.Background(), testutil.Token(t, want))

	require.NoError(t, err)
	assert.Equal(t, want, got)
}
