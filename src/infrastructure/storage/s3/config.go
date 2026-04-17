package s3

import "fmt"

// Config holds S3-compatible storage configuration.
type Config struct {
	Endpoint     string
	Region       string
	AccessKey    string
	SecretKey    string
	Bucket       string
	UsePathStyle bool
}

// Validate checks required configuration fields.
func (c Config) Validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("s3: endpoint is required")
	}
	if c.Bucket == "" {
		return fmt.Errorf("s3: bucket is required")
	}
	if c.AccessKey == "" {
		return fmt.Errorf("s3: access_key is required")
	}
	if c.SecretKey == "" {
		return fmt.Errorf("s3: secret_key is required")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	return nil
}
