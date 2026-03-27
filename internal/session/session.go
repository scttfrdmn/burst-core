// Package session provides helpers for reading and managing burst session
// manifests stored in S3.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// S3Client is the subset of S3 operations needed by this package.
// *internalaws.S3Client satisfies this interface.
type S3Client interface {
	GetObject(ctx context.Context, bucket, key string) ([]byte, error)
	PutObject(ctx context.Context, bucket, key string, body []byte) error
	ListObjects(ctx context.Context, bucket, prefix string) ([]string, error)
	DeleteObjects(ctx context.Context, bucket string, keys []string) error
}

// ReadManifest downloads and parses the manifest for the named session.
func ReadManifest(ctx context.Context, s3c S3Client, bucket, sessionID string) (*protocol.Manifest, error) {
	key := fmt.Sprintf("sessions/%s/manifest.json", sessionID)
	data, err := s3c.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, fmt.Errorf("reading manifest for session %q: %w", sessionID, err)
	}
	var m protocol.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest for session %q: %w", sessionID, err)
	}
	return &m, nil
}

// WriteManifest serializes m and uploads it to S3.
func WriteManifest(ctx context.Context, s3c S3Client, bucket string, m *protocol.Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	key := fmt.Sprintf("sessions/%s/manifest.json", m.SessionID)
	if err := s3c.PutObject(ctx, bucket, key, data); err != nil {
		return fmt.Errorf("writing manifest for session %q: %w", m.SessionID, err)
	}
	return nil
}

// ListSessions returns all session manifests in the bucket sorted by CreatedAt
// descending (newest first). Sessions whose manifests cannot be read are skipped.
func ListSessions(ctx context.Context, s3c S3Client, bucket string) ([]*protocol.Manifest, error) {
	keys, err := s3c.ListObjects(ctx, bucket, "sessions/")
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}

	// Collect unique session IDs from keys like "sessions/{id}/..."
	seen := map[string]struct{}{}
	for _, k := range keys {
		parts := strings.SplitN(k, "/", 3)
		if len(parts) >= 2 && parts[1] != "" {
			seen[parts[1]] = struct{}{}
		}
	}

	var manifests []*protocol.Manifest
	for sessionID := range seen {
		m, err := ReadManifest(ctx, s3c, bucket, sessionID)
		if err != nil {
			// Skip sessions we can't read (partially written, deleted, etc.)
			continue
		}
		manifests = append(manifests, m)
	}

	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].CreatedAt.After(manifests[j].CreatedAt)
	})
	return manifests, nil
}

// DeleteSession deletes all S3 objects under sessions/{sessionID}/.
func DeleteSession(ctx context.Context, s3c S3Client, bucket, sessionID string) error {
	prefix := fmt.Sprintf("sessions/%s/", sessionID)
	keys, err := s3c.ListObjects(ctx, bucket, prefix)
	if err != nil {
		return fmt.Errorf("listing session objects for %q: %w", sessionID, err)
	}
	if len(keys) == 0 {
		return nil
	}
	const batchSize = 1000
	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		if err := s3c.DeleteObjects(ctx, bucket, keys[i:end]); err != nil {
			return fmt.Errorf("deleting session objects for %q: %w", sessionID, err)
		}
	}
	return nil
}
