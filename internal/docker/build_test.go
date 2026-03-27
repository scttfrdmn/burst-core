package docker

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type mockECR struct {
	imageExistsFn      func(ctx context.Context, repoName, tag string) (bool, error)
	createRepositoryFn func(ctx context.Context, name string) (string, error)
	authTokenFn        func(ctx context.Context) (string, error)
}

func (m *mockECR) ImageExists(ctx context.Context, repoName, tag string) (bool, error) {
	return m.imageExistsFn(ctx, repoName, tag)
}
func (m *mockECR) CreateRepository(ctx context.Context, name string) (string, error) {
	if m.createRepositoryFn != nil {
		return m.createRepositoryFn(ctx, name)
	}
	return "uri", nil
}
func (m *mockECR) AuthToken(ctx context.Context) (string, error) {
	if m.authTokenFn != nil {
		return m.authTokenFn(ctx)
	}
	return "token", nil
}

func TestBuildAndPush_skipsIfExists(t *testing.T) {
	called := false
	ecrc := &mockECR{
		imageExistsFn: func(_ context.Context, _, _ string) (bool, error) {
			return true, nil
		},
		createRepositoryFn: func(_ context.Context, _ string) (string, error) {
			called = true
			return "", errors.New("should not be called")
		},
	}

	opts := BuildOptions{
		Dockerfile: "FROM scratch",
		Lang:       "go",
		EnvHash:    "abc123",
		ECRBaseURI: "123.dkr.ecr.us-east-1.amazonaws.com",
		Region:     "us-east-1",
	}

	var w bytes.Buffer
	uri, err := BuildAndPush(context.Background(), ecrc, opts, &w)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "123.dkr.ecr.us-east-1.amazonaws.com/burst-workers-go:abc123"
	if uri != want {
		t.Errorf("uri: got %q, want %q", uri, want)
	}
	if called {
		t.Error("CreateRepository should not have been called when image already exists")
	}
}

func TestBuildOptions_URIComposition(t *testing.T) {
	tests := []struct {
		lang    string
		hash    string
		base    string
		wantURI string
		wantRepo string
	}{
		{"python", "sha256:abc", "123.dkr.ecr.us-east-1.amazonaws.com",
			"123.dkr.ecr.us-east-1.amazonaws.com/burst-workers-python:sha256:abc",
			"burst-workers-python"},
		{"go", "deadbeef", "456.dkr.ecr.eu-west-1.amazonaws.com",
			"456.dkr.ecr.eu-west-1.amazonaws.com/burst-workers-go:deadbeef",
			"burst-workers-go"},
	}
	for _, tt := range tests {
		opts := BuildOptions{Lang: tt.lang, EnvHash: tt.hash, ECRBaseURI: tt.base}
		if got := opts.ImageURI(); got != tt.wantURI {
			t.Errorf("ImageURI(%s): got %q, want %q", tt.lang, got, tt.wantURI)
		}
		if got := opts.RepoName(); got != tt.wantRepo {
			t.Errorf("RepoName(%s): got %q, want %q", tt.lang, got, tt.wantRepo)
		}
	}
}
