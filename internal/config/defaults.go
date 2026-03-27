package config

// DefaultConfig returns a Config with sensible default values for optional fields.
// Required fields (Region, S3Bucket, etc.) are left empty.
func DefaultConfig() *Config {
	return &Config{
		DefaultCPU:      2,
		DefaultMemoryGB: 4,
		DefaultWorkers:  10,
		Backend:         "fargate",
		Spot:            false,
	}
}
