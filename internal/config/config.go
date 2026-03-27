// Package config manages the burst-core configuration file at ~/.burst/config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned by Load when ~/.burst/config.json does not exist.
// Callers should distinguish this from parse errors and prompt the user to
// run `burst-core setup`.
var ErrNotFound = errors.New("burst config not found; run: burst-core setup")

// Config holds all burst-core configuration values.
// The config file lives at ~/.burst/config.json with permissions 0600.
type Config struct {
	Region             string  `json:"region"`
	S3Bucket           string  `json:"s3_bucket"`
	ECSCluster         string  `json:"ecs_cluster"`
	ECRBaseURI         string  `json:"ecr_base_uri"`
	ExecutionRoleARN   string  `json:"execution_role_arn"`
	TaskRoleARN        string  `json:"task_role_arn"`
	DefaultCPU         int     `json:"default_cpu"`
	DefaultMemoryGB    int     `json:"default_memory_gb"`
	DefaultWorkers     int     `json:"default_workers"`
	MaxCostPerJob      float64 `json:"max_cost_per_job"`      // 0 = no limit
	CostAlertThreshold float64 `json:"cost_alert_threshold"`  // 0 = no alert
	Backend            string  `json:"backend"`               // fargate|ec2
	Spot               bool    `json:"spot"`
	FargateQuotaVCPU   float64 `json:"fargate_quota_vcpu"`
}

// ConfigPath returns the path to the config file (~/.burst/config.json).
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".burst", "config.json"), nil
}

// configPath is an alias used internally.
func configPath() (string, error) { return ConfigPath() }

// Load reads the config from ~/.burst/config.json and applies defaults for
// any unset numeric fields. Returns ErrNotFound if the file does not exist.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	return loadFrom(path)
}

// loadFrom reads from an explicit path. Used directly by tests.
func loadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

// Save writes the config to ~/.burst/config.json with permissions 0600.
// The ~/.burst directory is created if it does not exist.
func (c *Config) Save() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	return saveTo(c, path)
}

// saveTo writes to an explicit path. Used directly by tests.
func saveTo(c *Config, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return os.Chmod(path, 0600)
}

// Validate returns an error if any required fields are missing.
func (c *Config) Validate() error {
	var missing []string
	if c.Region == "" {
		missing = append(missing, "region")
	}
	if c.S3Bucket == "" {
		missing = append(missing, "s3_bucket")
	}
	if c.ECSCluster == "" {
		missing = append(missing, "ecs_cluster")
	}
	if c.ECRBaseURI == "" {
		missing = append(missing, "ecr_base_uri")
	}
	if c.ExecutionRoleARN == "" {
		missing = append(missing, "execution_role_arn")
	}
	if c.TaskRoleARN == "" {
		missing = append(missing, "task_role_arn")
	}

	if len(missing) > 0 {
		return fmt.Errorf("invalid config: missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

// applyDefaults fills zero-value numeric/string fields with sensible defaults.
func applyDefaults(c *Config) {
	d := DefaultConfig()
	if c.DefaultCPU == 0 {
		c.DefaultCPU = d.DefaultCPU
	}
	if c.DefaultMemoryGB == 0 {
		c.DefaultMemoryGB = d.DefaultMemoryGB
	}
	if c.DefaultWorkers == 0 {
		c.DefaultWorkers = d.DefaultWorkers
	}
	if c.Backend == "" {
		c.Backend = d.Backend
	}
}
