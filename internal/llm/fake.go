package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNoReply is the fake asked something it has no reply for.
var ErrNoReply = errors.New("llm: the fake has no reply for this prompt")

// Reply is one canned answer of the fake.
type Reply struct {
	// Text is the answer, checked against the schema like a real one.
	Text string
	// Err fails the attempt instead, such as a *StatusError or ErrRefused.
	Err error
	// Delay holds the answer back, for timeouts.
	Delay time.Duration
	Usage Usage
}

// Fake is a deterministic Provider for tests. Replies are keyed by a hash of
// what is asked; each call takes the next reply for its key, and the last
// one repeats. A prompt it has no reply for fails with ErrNoReply, so a
// test notices calls it did not expect.
type Fake struct {
	mu      sync.Mutex
	replies map[string][]Reply
	calls   map[string]int
	total   int
}

// NewFake returns a fake without replies.
func NewFake() *Fake {
	return &Fake{replies: map[string][]Reply{}, calls: map[string]int{}}
}

// On sets the replies to a text request.
func (f *Fake) On(req Request, schema *Schema, replies ...Reply) {
	f.set(Key(req, nil, schema), replies)
}

// OnImage sets the replies to a request about an image.
func (f *Fake) OnImage(req Request, image Image, schema *Schema, replies ...Reply) {
	f.set(Key(req, &image, schema), replies)
}

func (f *Fake) set(key string, replies []Reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[key] = replies
}

// Calls is how often a request was asked.
func (f *Fake) Calls(req Request, schema *Schema) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[Key(req, nil, schema)]
}

// TotalCalls is how many attempts the fake has answered, or failed.
func (f *Fake) TotalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

// Complete answers with the next reply for the call.
func (f *Fake) Complete(ctx context.Context, call Call) (Completion, error) {
	key := callKey(call.System, call.Prompt, call.Image, call.Schema)
	f.mu.Lock()
	f.total++
	n := f.calls[key]
	f.calls[key] = n + 1
	replies := f.replies[key]
	f.mu.Unlock()

	if len(replies) == 0 {
		return Completion{}, fmt.Errorf("%w (key %s)", ErrNoReply, key[:12])
	}
	reply := replies[min(n, len(replies)-1)]
	if reply.Delay > 0 {
		select {
		case <-ctx.Done():
			return Completion{}, ctx.Err()
		case <-time.After(reply.Delay):
		}
	}
	completion := Completion{Text: reply.Text, Model: call.Model, Usage: reply.Usage}
	if reply.Err != nil {
		return completion, reply.Err
	}
	return completion, nil
}

// Key identifies a request to the fake: a hash of the system prompt, the
// prompt, the image and the schema's name.
func Key(req Request, image *Image, schema *Schema) string {
	return callKey(req.System, req.Prompt, image, schema)
}

func callKey(system, prompt string, image *Image, schema *Schema) string {
	h := sha256.New()
	for _, part := range []string{system, prompt, schema.Name} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	if image != nil {
		h.Write([]byte(image.MediaType))
		h.Write([]byte{0})
		h.Write(image.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
