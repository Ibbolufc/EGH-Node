package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Debug  bool   `yaml:"debug"`
	Remote string `yaml:"remote"`
	Token  string `yaml:"token"`
	NodeID int    `yaml:"node_id"`

	API struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
		SSL  struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"ssl"`
		UploadLimit int `yaml:"upload_limit"`
	} `yaml:"api"`

	System struct {
		Data string `yaml:"data"`
		SFTP struct {
			BindPort int `yaml:"bind_port"`
		} `yaml:"sftp"`
	} `yaml:"system"`

	AllowedOrigins []string `yaml:"allowed_origins"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Remote == "" {
		return nil, fmt.Errorf("config missing remote")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("config missing token")
	}
	if cfg.NodeID <= 0 {
		return nil, fmt.Errorf("config missing valid node_id")
	}
	if cfg.API.Host == "" {
		cfg.API.Host = "0.0.0.0"
	}
	if cfg.API.Port == 0 {
		cfg.API.Port = 8080
	}

	return &cfg, nil
}
