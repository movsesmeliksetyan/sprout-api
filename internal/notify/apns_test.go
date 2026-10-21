package notify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPNs_Push checks the request sent to APNs and how its answers are
// read, against a stand-in server.
func TestAPNs_Push(t *testing.T) {
	t.Parallel()
	var got struct {
		path, topic, pushType string
		body                  map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path, got.topic, got.pushType = r.URL.Path, r.Header.Get("apns-topic"), r.Header.Get("apns-push-type")
		_ = json.Unmarshal(raw, &got.body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/gone"):
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
		case strings.HasSuffix(r.URL.Path, "/bad"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"reason":"BadDeviceToken"}`))
		case strings.HasSuffix(r.URL.Path, "/busy"):
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"reason":"ServiceUnavailable"}`))
		}
	}))
	t.Cleanup(server.Close)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	client := func() *apns2.Client {
		c := apns2.NewTokenClient(&token.Token{AuthKey: key, KeyID: "KEY", TeamID: "TEAM"})
		c.HTTPClient = server.Client()
		c.Host = server.URL
		return c
	}
	a := &APNs{topic: "app.sprout", sandbox: client(), production: client()}
	push := func(tok string) error {
		return a.Push(context.Background(), Push{
			Token: tok, Environment: EnvironmentSandbox, Title: "Your week", Body: "You spent $120.",
			NotificationID: "n1", DeepLink: "sprout://stats",
		})
	}

	require.NoError(t, push("ok"))
	assert.Equal(t, "/3/device/ok", got.path)
	assert.Equal(t, "app.sprout", got.topic)
	assert.Equal(t, "alert", got.pushType)
	assert.Equal(t, "n1", got.body["notification_id"])
	assert.Equal(t, "sprout://stats", got.body["deep_link"])
	alert := got.body["aps"].(map[string]any)["alert"].(map[string]any)
	assert.Equal(t, "Your week", alert["title"])

	assert.ErrorIs(t, push("gone"), ErrTokenInvalid)
	assert.ErrorIs(t, push("bad"), ErrTokenInvalid)
	err = push("busy")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrTokenInvalid)
}
