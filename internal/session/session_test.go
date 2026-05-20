package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// memS3 is an in-memory S3Client stub for tests.
type memS3 struct {
	objects map[string][]byte
}

func newMemS3(objects map[string][]byte) *memS3 {
	if objects == nil {
		objects = map[string][]byte{}
	}
	return &memS3{objects: objects}
}

func (m *memS3) GetObject(_ context.Context, _, key string) ([]byte, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("NoSuchKey: %s", key)
	}
	return data, nil
}

func (m *memS3) PutObject(_ context.Context, _, key string, body []byte) error {
	m.objects[key] = body
	return nil
}

func (m *memS3) ListObjects(_ context.Context, _, prefix string) ([]string, error) {
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memS3) DeleteObjects(_ context.Context, _ string, keys []string) error {
	for _, k := range keys {
		delete(m.objects, k)
	}
	return nil
}

func makeManifest(sessionID string, createdAt time.Time) *protocol.Manifest {
	return &protocol.Manifest{
		SessionStatus: protocol.SessionStatus{
			SessionID: sessionID,
			Language:  "py",
			Status:    "complete",
			CreatedAt: createdAt,
		},
	}
}

func marshalManifest(t *testing.T, m *protocol.Manifest) []byte {
	t.Helper()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return b
}

const bucket = "burst-us-east-1"

func TestReadManifest_found(t *testing.T) {
	m := makeManifest("py-20260315-aabbccdd", time.Now())
	s3c := newMemS3(map[string][]byte{
		"sessions/py-20260315-aabbccdd/manifest.json": marshalManifest(t, m),
	})

	got, err := ReadManifest(context.Background(), s3c, bucket, "py-20260315-aabbccdd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SessionID != "py-20260315-aabbccdd" {
		t.Errorf("session ID: got %q, want py-20260315-aabbccdd", got.SessionID)
	}
}

func TestReadManifest_notFound(t *testing.T) {
	s3c := newMemS3(nil)
	_, err := ReadManifest(context.Background(), s3c, bucket, "py-missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestListSessions_empty(t *testing.T) {
	s3c := newMemS3(nil)
	sessions, err := ListSessions(context.Background(), s3c, bucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

func TestListSessions_multiple_sorted(t *testing.T) {
	now := time.Now()
	older := now.Add(-24 * time.Hour)
	m1 := makeManifest("py-20260315-aabbccdd", now)
	m2 := makeManifest("py-20260314-11223344", older)

	s3c := newMemS3(map[string][]byte{
		"sessions/py-20260315-aabbccdd/manifest.json":          marshalManifest(t, m1),
		"sessions/py-20260315-aabbccdd/tasks/task-0000.status": []byte("done"),
		"sessions/py-20260314-11223344/manifest.json":          marshalManifest(t, m2),
	})

	sessions, err := ListSessions(context.Background(), s3c, bucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].SessionID != "py-20260315-aabbccdd" {
		t.Errorf("first session should be newest, got %q", sessions[0].SessionID)
	}
}

func TestDeleteSession(t *testing.T) {
	s3c := newMemS3(map[string][]byte{
		"sessions/py-20260315-aabbccdd/manifest.json":          []byte("{}"),
		"sessions/py-20260315-aabbccdd/tasks/task-0000.task":   []byte("data"),
		"sessions/py-20260315-aabbccdd/tasks/task-0000.result": []byte("result"),
		"sessions/py-20260315-aabbccdd/tasks/task-0000.status": []byte("done"),
		"sessions/other-session/manifest.json":                 []byte("{}"),
	})

	if err := DeleteSession(context.Background(), s3c, bucket, "py-20260315-aabbccdd"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Objects under that session should be gone
	remaining, _ := s3c.ListObjects(context.Background(), bucket, "sessions/py-20260315-aabbccdd/")
	if len(remaining) != 0 {
		t.Errorf("expected 0 remaining objects, got %d", len(remaining))
	}
	// Other session should be untouched
	other, _ := s3c.ListObjects(context.Background(), bucket, "sessions/other-session/")
	if len(other) != 1 {
		t.Errorf("other session should have 1 object, got %d", len(other))
	}
}
