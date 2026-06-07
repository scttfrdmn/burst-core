package aji

import (
	"bufio"
	"context"
	"crypto/sha256"
	"debug/elf"
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

// BuildWorkerBinary cross-compiles the Go package at pkgDir for linux/{arch}.
// arch must be "amd64" or "arm64". CGO is disabled.
func BuildWorkerBinary(ctx context.Context, pkgDir, outPath, arch string) error {
	if arch == "" {
		arch = "amd64"
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", outPath, ".")
	cmd.Dir = pkgDir
	cmd.Env = append(os.Environ(),
		"GOOS=linux",
		"GOARCH="+arch,
		"CGO_ENABLED=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cross-compiling for linux/%s: %w\n%s", arch, err, out)
	}
	return nil
}

// WorkerDockerfile returns the Dockerfile for the worker image.
// Uses distroless/static to provide CA certificates (required for TLS to AWS APIs)
// while keeping the image small (~4MB) with no shell or package manager.
func WorkerDockerfile() string {
	return `FROM gcr.io/distroless/static:nonroot
COPY worker-linux-amd64 /worker
ENTRYPOINT ["/worker", "--aji-worker"]
`
}

// buildAndPushWorkerImage cross-compiles the binary at binaryPath for linux/{arch},
// packages it into a scratch Docker image, and pushes to ECR.
// arch is "amd64" or "arm64". Returns the full ECR image URI.
// Skips build if ECR already has the envHash tag.
func buildAndPushWorkerImage(ctx context.Context, ecrc *internalaws.ECRClient, binaryPath, envHash, arch string, w io.Writer) (string, error) {
	if arch == "" {
		arch = "amd64"
	}
	const lang = "go"
	// Include arch in the tag so amd64 and arm64 images coexist in the same repo
	tag := envHash + "-" + arch
	repoName := "burst-workers-" + lang
	imageURI := fmt.Sprintf("%s/%s:%s", ecrc.BaseURI(), repoName, tag)

	exists, err := ecrc.ImageExists(ctx, repoName, tag)
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

	// If binaryPath already points at a linux/amd64 binary (e.g. from go test -c
	// with GOOS=linux), copy it directly; otherwise cross-compile from source.
	workerBin := filepath.Join(tmpDir, "worker-linux-amd64")
	if isPrebuiltForArch(binaryPath, arch) {
		fmt.Fprintf(w, "[aji] Using pre-built linux/%s binary: %s\n", arch, binaryPath)
		if err := copyFile(binaryPath, workerBin); err != nil {
			return "", fmt.Errorf("copying pre-built binary: %w", err)
		}
	} else {
		pkgDir := filepath.Dir(binaryPath)
		fmt.Fprintf(w, "[aji] Cross-compiling worker binary for linux/%s...\n", arch)
		if err := BuildWorkerBinary(ctx, pkgDir, workerBin, arch); err != nil {
			return "", err
		}
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

// containerCLI returns the container CLI to use: "docker" if it's in PATH and
// reachable, otherwise "podman" if available. Falls back to "docker" so errors
// surface with a clear message at build time.
func containerCLI() string {
	// Try docker first (also works when Podman exposes /var/run/docker.sock)
	if _, err := exec.LookPath("docker"); err == nil {
		if exec.Command("docker", "info", "--format", "{{.ID}}").Run() == nil {
			return "docker"
		}
	}
	if path, err := exec.LookPath("podman"); err == nil {
		return path
	}
	return "docker"
}

// runDockerCmd runs a docker/podman subcommand, streaming output to w with the given prefix.
func runDockerCmd(ctx context.Context, args []string, stdin io.Reader, w io.Writer, prefix string) error {
	cli := containerCLI()
	cmd := exec.CommandContext(ctx, cli, args...)
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

// isPrebuiltForArch reports whether path is an ELF linux binary for the given arch.
func isPrebuiltForArch(path, arch string) bool {
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if f.OSABI != elf.ELFOSABI_NONE {
		return false
	}
	switch arch {
	case "arm64":
		return f.Machine == elf.EM_AARCH64
	default: // amd64
		return f.Machine == elf.EM_X86_64
	}
}

// copyFile copies src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
