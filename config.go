package main

import (
    "fmt"
    "os"
    "sync"

    "gopkg.in/yaml.v3"
)

type Config struct {
    mu sync.RWMutex `yaml:"-"`

    Debug       bool   `yaml:"debug"`
    Remote      string `yaml:"remote"`
    Token       string `yaml:"token"`
    DaemonToken string `yaml:"daemon_token,omitempty"`
    NodeID      int    `yaml:"node_id"`

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
    if cfg.System.Data == "" {
        cfg.System.Data = "/var/lib/egh-node/volumes"
    }

    return &cfg, nil
}

func SaveConfig(path string, cfg *Config) error {
    cfg.mu.RLock()
    clone := *cfg
    cfg.mu.RUnlock()
    clone.mu = sync.RWMutex{}

    data, err := yaml.Marshal(&clone)
    if err != nil {
        return fmt.Errorf("marshal config: %w", err)
    }
    if err := os.WriteFile(path, data, 0600); err != nil {
        return fmt.Errorf("write config: %w", err)
    }
    return nil
}

func (cfg *Config) CurrentDaemonSecret() string {
    cfg.mu.RLock()
    defer cfg.mu.RUnlock()
    if cfg.DaemonToken != "" {
        return cfg.DaemonToken
    }
    return cfg.Token
}

func (cfg *Config) UpdateDaemonToken(token string) bool {
    if token == "" {
        return false
    }
    cfg.mu.Lock()
    defer cfg.mu.Unlock()
    if cfg.DaemonToken == token {
        return false
    }
    cfg.DaemonToken = token
    return true
}
