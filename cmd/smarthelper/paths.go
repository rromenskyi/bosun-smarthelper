package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/roman220/bosun-smarthelper/internal/backup"
	"github.com/roman220/bosun-smarthelper/internal/config"
)

func resolveBackupS3(cfg *config.Config) (backup.S3Config, error) {
	s3cfg := cfg.Backup.S3
	if s3cfg.Endpoint == "" || s3cfg.Bucket == "" {
		return backup.S3Config{}, fmt.Errorf("backup.s3.endpoint and backup.s3.bucket must be set in config.yaml")
	}
	accessKeyID := os.Getenv(s3cfg.AccessKeyIDEnv)
	secretAccessKey := os.Getenv(s3cfg.SecretAccessKeyEnv)
	if accessKeyID == "" || secretAccessKey == "" {
		return backup.S3Config{}, fmt.Errorf("%s and %s must be set (in .env)", s3cfg.AccessKeyIDEnv, s3cfg.SecretAccessKeyEnv)
	}
	return backup.S3Config{
		Endpoint:        s3cfg.Endpoint,
		Region:          s3cfg.Region,
		Bucket:          s3cfg.Bucket,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
	}, nil
}

// resolveDataDir mirrors every store's own default (see e.g.
// tools.NewMemoTool) so backup/restore agree with the running service on
// where its data actually lives unless backup.data_dir overrides it.
func resolveDataDir(configuredDir string) (string, error) {
	if configuredDir != "" {
		return configuredDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "bosun"), nil
}
