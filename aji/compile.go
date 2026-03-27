package aji

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
)

// EnvHash returns a SHA256 hash derived from the binary's path, modification time, and size.
// Used as the ECR image tag to detect when the binary has changed.
func EnvHash(binaryPath string) (string, error) {
	info, err := os.Stat(binaryPath)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", binaryPath, err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s:%d:%d", binaryPath, info.ModTime().UnixNano(), info.Size())
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// currentBinaryPath returns the absolute path to the currently running executable,
// with symlinks resolved.
func currentBinaryPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	return filepath.EvalSymlinks(p)
}

// BuildWorkerBinary cross-compiles the Go package at pkgDir for linux/amd64.
// The compiled binary is written to outPath with CGO disabled.
func BuildWorkerBinary(ctx context.Context, pkgDir, outPath string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", outPath, ".")
	cmd.Dir = pkgDir
	cmd.Env = append(os.Environ(),
		"GOOS=linux",
		"GOARCH=amd64",
		"CGO_ENABLED=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cross-compiling for linux/amd64: %w\n%s", err, out)
	}
	return nil
}

// WorkerDockerfile returns the Dockerfile content for a scratch-based worker image.
// The resulting image is ~10MB with just the binary — no OS, shell, or package manager.
func WorkerDockerfile() string {
	return "FROM scratch\nCOPY worker-linux-amd64 /worker\nENTRYPOINT [\"/worker\", \"--aji-worker\"]\n"
}

// buildAndPushWorkerImage cross-compiles the binary at binaryPath for linux/amd64,
// packages it into a scratch Docker image, and pushes to ECR.
// Returns the full ECR image URI. Skips build if ECR already has the envHash tag.
func buildAndPushWorkerImage(ctx context.Context, ecrc *internalaws.ECRClient, binaryPath, envHash string, w io.Writer) (string, error) {
	const lang = "go"
	repoName := "burst-workers-" + lang
	imageURI := fmt.Sprintf("%s/%s:%s", ecrc.BaseURI(), repoName, envHash)

	exists, err := ecrc.ImageExists(ctx, repoName, envHash)
	if err != nil {
		return "", fmt.Errorf("checking ECR for existing image: %w", err)
	}
	if exists {
		return imageURI, nil
	}

	if _, err := ecrc.CreateRepository(ctx, repoName); err != nil {
		return "", fmt.Errorf("creating ECR repository: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "aji-docker-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Cross-compile binary for linux/amd64
	workerBin := filepath.Join(tmpDir, "worker-linux-amd64")
	pkgDir := filepath.Dir(binaryPath)
	fmt.Fprintf(w, "[aji] Cross-compiling worker binary for linux/amd64...\n")
	if err := BuildWorkerBinary(ctx, pkgDir, workerBin); err != nil {
		return "", err
	}
	if err := os.Chmod(workerBin, 0755); err != nil {
		return "", fmt.Errorf("chmod worker binary: %w", err)
	}

	// Write Dockerfile
	dfPath := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dfPath, []byte(WorkerDockerfile()), 0600); err != nil {
		return "", fmt.Errorf("writing Dockerfile: %w", err)
	}

	// Build image
	if err := runDockerCmd(ctx, []string{"build", "-t", imageURI, tmpDir}, nil, w, "[docker] "); err != nil {
		return "", fmt.Errorf("docker build: %w", err)
	}

	// ECR login
	password, err := ecrc.AuthToken(ctx)
	if err != nil {
		return "", fmt.Errorf("ECR auth token: %w", err)
	}
	if err := runDockerCmd(ctx, []string{"login", "--username", "AWS", "--password-stdin", ecrc.BaseURI()},
		strings.NewReader(password), w, "[ecr] "); err != nil {
		return "", fmt.Errorf("docker login: %w", err)
	}

	// Push image
	if err := runDockerCmd(ctx, []string{"push", imageURI}, nil, w, "[ecr] "); err != nil {
		return "", fmt.Errorf("docker push: %w", err)
	}

	fmt.Fprintf(w, "✓ Image pushed: %s\n", imageURI)
	return imageURI, nil
}

// runDockerCmd runs a docker subcommand, streaming output to w with the given prefix.
func runDockerCmd(ctx context.Context, args []string, stdin io.Reader, w io.Writer, prefix string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
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
	done := make(chan struct{}, 2)
	stream := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			fmt.Fprintf(w, "%s%s\n", prefix, sc.Text())
		}
		done <- struct{}{}
	}
	go stream(stdout)
	go stream(stderr)
	<-done
	<-done
	return cmd.Wait()
}
