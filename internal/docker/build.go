// Package docker provides helpers for building Docker images and pushing them to ECR.
package docker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ECRClient is the subset of ECR operations needed by this package.
// *internalaws.ECRClient satisfies this interface.
type ECRClient interface {
	ImageExists(ctx context.Context, repoName, tag string) (bool, error)
	CreateRepository(ctx context.Context, name string) (string, error)
	AuthToken(ctx context.Context) (string, error)
}

// BuildOptions configures a Docker build+push operation.
type BuildOptions struct {
	Dockerfile string // Dockerfile content as a string
	Lang       string // "python", "go", "julia", "typescript", "r"
	EnvHash    string // SHA256 hex string — used as the ECR image tag
	ECRBaseURI string // e.g. "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	Region     string
	BuildArgs  map[string]string // optional --build-arg values
}

// ImageURI returns the full ECR image URI for these options.
func (o BuildOptions) ImageURI() string {
	return fmt.Sprintf("%s/burst-workers-%s:%s", o.ECRBaseURI, o.Lang, o.EnvHash)
}

// RepoName returns the ECR repository name for these options.
func (o BuildOptions) RepoName() string {
	return "burst-workers-" + o.Lang
}

// BuildAndPush builds a Docker image from opts.Dockerfile and pushes it to ECR.
// Returns the full image URI immediately if ECR already has the image tagged with
// opts.EnvHash (idempotent). Progress output is streamed to w with "[docker] " and
// "[ecr] " prefixes.
func BuildAndPush(ctx context.Context, ecrc ECRClient, opts BuildOptions, w io.Writer) (string, error) {
	uri := opts.ImageURI()
	repoName := opts.RepoName()

	// Skip build if image already exists in ECR.
	exists, err := ecrc.ImageExists(ctx, repoName, opts.EnvHash)
	if err != nil {
		return "", fmt.Errorf("checking ECR for existing image: %w", err)
	}
	if exists {
		return uri, nil
	}

	// Ensure ECR repository exists.
	if _, err := ecrc.CreateRepository(ctx, repoName); err != nil {
		return "", fmt.Errorf("creating ECR repository: %w", err)
	}

	// Write Dockerfile to a temp directory.
	tmpDir, err := os.MkdirTemp("", "burst-docker-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dfPath := tmpDir + "/Dockerfile"
	if err := os.WriteFile(dfPath, []byte(opts.Dockerfile), 0600); err != nil {
		return "", fmt.Errorf("writing Dockerfile: %w", err)
	}

	// Build image.
	buildArgs := []string{"build", "-t", uri, tmpDir}
	for k, v := range opts.BuildArgs {
		buildArgs = append(buildArgs, "--build-arg", k+"="+v)
	}
	if err := runCommand(ctx, "docker", buildArgs, nil, w, "[docker] "); err != nil {
		return "", fmt.Errorf("docker build: %w", err)
	}

	// Login to ECR.
	password, err := ecrc.AuthToken(ctx)
	if err != nil {
		return "", fmt.Errorf("getting ECR auth token: %w", err)
	}
	loginArgs := []string{"login", "--username", "AWS", "--password-stdin", opts.ECRBaseURI}
	if err := runCommand(ctx, "docker", loginArgs, strings.NewReader(password), w, "[ecr] "); err != nil {
		return "", fmt.Errorf("docker login: %w", err)
	}

	// Push image.
	if err := runCommand(ctx, "docker", []string{"push", uri}, nil, w, "[ecr] "); err != nil {
		return "", fmt.Errorf("docker push: %w", err)
	}

	return uri, nil
}

// runCommand runs an external command, prefixing each output line with prefix
// and writing to w. stdin may be nil.
func runCommand(ctx context.Context, name string, args []string, stdin io.Reader, w io.Writer, prefix string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// Stream stdout and stderr to w with prefix.
	done := make(chan struct{}, 2)
	streamLines := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			fmt.Fprintf(w, "%s%s\n", prefix, sc.Text())
		}
		done <- struct{}{}
	}
	go streamLines(stdout)
	go streamLines(stderr)
	<-done
	<-done

	return cmd.Wait()
}
