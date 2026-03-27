package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_, err := loadFrom(path)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("loadFrom(missing) = %v, want ErrNotFound", err)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".burst", "config.json")

	cfg := &Config{
		Region:           "us-west-2",
		S3Bucket:         "burst-us-west-2",
		ECSCluster:       "burst-cluster",
		ECRBaseURI:       "123456789.dkr.ecr.us-west-2.amazonaws.com",
		ExecutionRoleARN: "arn:aws:iam::123456789:role/burst-execution-role",
		TaskRoleARN:      "arn:aws:iam::123456789:role/burst-task-role",
		DefaultCPU:       4,
		DefaultMemoryGB:  8,
		DefaultWorkers:   20,
		Backend:          "fargate",
	}

	if err := saveTo(cfg, path); err != nil {
		t.Fatalf("saveTo: %v", err)
	}

	got, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}

	if got.Region != cfg.Region {
		t.Errorf("Region: got %q want %q", got.Region, cfg.Region)
	}
	if got.S3Bucket != cfg.S3Bucket {
		t.Errorf("S3Bucket: got %q want %q", got.S3Bucket, cfg.S3Bucket)
	}
	if got.DefaultCPU != cfg.DefaultCPU {
		t.Errorf("DefaultCPU: got %d want %d", got.DefaultCPU, cfg.DefaultCPU)
	}
	if got.Backend != cfg.Backend {
		t.Errorf("Backend: got %q want %q", got.Backend, cfg.Backend)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "config.json")

	cfg := &Config{Region: "us-east-1"}
	if err := saveTo(cfg, path); err != nil {
		t.Fatalf("saveTo: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}

func TestSaveFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := saveTo(&Config{}, path); err != nil {
		t.Fatalf("saveTo: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("file permissions: got %o, want 0600", info.Mode().Perm())
	}
}

func TestApplyDefaults(t *testing.T) {
	// A config with zero values for optional fields gets defaults applied.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := &Config{Region: "eu-west-1"}
	if err := saveTo(cfg, path); err != nil {
		t.Fatalf("saveTo: %v", err)
	}

	got, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}

	if got.DefaultCPU != 2 {
		t.Errorf("DefaultCPU: got %d, want 2 (default)", got.DefaultCPU)
	}
	if got.DefaultMemoryGB != 4 {
		t.Errorf("DefaultMemoryGB: got %d, want 4 (default)", got.DefaultMemoryGB)
	}
	if got.DefaultWorkers != 10 {
		t.Errorf("DefaultWorkers: got %d, want 10 (default)", got.DefaultWorkers)
	}
	if got.Backend != "fargate" {
		t.Errorf("Backend: got %q, want \"fargate\" (default)", got.Backend)
	}
}

func TestValidateMissingFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want string // substring expected in error
	}{
		{
			"all missing",
			&Config{},
			"region",
		},
		{
			"missing s3_bucket",
			&Config{Region: "us-east-1", ECSCluster: "c", ECRBaseURI: "u",
				ExecutionRoleARN: "e", TaskRoleARN: "t"},
			"s3_bucket",
		},
		{
			"complete",
			&Config{
				Region: "us-east-1", S3Bucket: "b", ECSCluster: "c",
				ECRBaseURI: "u", ExecutionRoleARN: "e", TaskRoleARN: "t",
			},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.want)
			}
			if !contains(err.Error(), tt.want) {
				t.Errorf("Validate() = %q, want to contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	d := DefaultConfig()
	if d.DefaultCPU != 2 {
		t.Errorf("DefaultCPU = %d, want 2", d.DefaultCPU)
	}
	if d.DefaultMemoryGB != 4 {
		t.Errorf("DefaultMemoryGB = %d, want 4", d.DefaultMemoryGB)
	}
	if d.DefaultWorkers != 10 {
		t.Errorf("DefaultWorkers = %d, want 10", d.DefaultWorkers)
	}
	if d.Backend != "fargate" {
		t.Errorf("Backend = %q, want \"fargate\"", d.Backend)
	}
	if d.Spot {
		t.Error("Spot = true, want false")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (substr == "" || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
