package config

import (
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/internal/platformdir"
)

// MediaConfig says where the gateway keeps media files: audio and images
// captured for the audit log today, generated videos and images later. Media
// metadata always lives in the shared storage backend; only the bytes go
// here. See docs/adr/0013-media-storage.md.
type MediaConfig struct {
	Storage MediaStorageConfig `yaml:"storage"`
}

// MediaStorageConfig selects the blob backend.
type MediaStorageConfig struct {
	// Type is the blob backend: "filesystem" (default) writes files under
	// Path; "memory" keeps them in the process and loses them on restart.
	Type string `yaml:"type" env:"MEDIA_STORAGE_TYPE"`

	// Path is the directory the filesystem backend writes to. Default:
	// ./data/media when a ./data directory exists, otherwise the OS per-user
	// data directory (e.g. ~/.local/share/gomodel/media). Mount a volume,
	// EFS, or an S3 FUSE mount there to keep media off the local disk.
	Path string `yaml:"path" env:"MEDIA_STORAGE_PATH"`
}

const (
	MediaStorageFilesystem = "filesystem"
	MediaStorageMemory     = "memory"
)

// DefaultMediaPath is where media files go when no path is configured.
func DefaultMediaPath() string {
	return platformdir.DataFile("media")
}

// ResolveMediaConfig fills defaults and validates the media storage block.
func ResolveMediaConfig(cfg *MediaConfig) error {
	cfg.Storage.Type = strings.ToLower(strings.TrimSpace(cfg.Storage.Type))
	if cfg.Storage.Type == "" {
		cfg.Storage.Type = MediaStorageFilesystem
	}
	switch cfg.Storage.Type {
	case MediaStorageFilesystem, MediaStorageMemory:
	default:
		return fmt.Errorf("media.storage.type must be one of: %s, %s; got %q", MediaStorageFilesystem, MediaStorageMemory, cfg.Storage.Type)
	}
	cfg.Storage.Path = strings.TrimSpace(cfg.Storage.Path)
	if cfg.Storage.Path == "" {
		cfg.Storage.Path = DefaultMediaPath()
	}
	return nil
}
