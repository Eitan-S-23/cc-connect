package daemon

import (
	"fmt"
	"io"
	"path/filepath"
	"time"
)

const defaultSupervisorRestartDelay = 10 * time.Second

const (
	supervisedProcessEnv   = "CC_DAEMON_SUPERVISED"
	supervisorStartGateEnv = "CC_DAEMON_START_GATE"
)

type SupervisorConfig struct {
	BinaryPath   string
	ConfigPath   string
	WorkDir      string
	LogFile      string
	LogMaxSize   int64
	LogBackups   int
	RestartDelay time.Duration
	logWriter    io.Writer
}

func (cfg SupervisorConfig) resolve() (SupervisorConfig, error) {
	if cfg.BinaryPath == "" {
		return SupervisorConfig{}, fmt.Errorf("supervisor binary path is required")
	}
	if cfg.ConfigPath == "" {
		if cfg.WorkDir == "" {
			return SupervisorConfig{}, fmt.Errorf("supervisor config path is required")
		}
		cfg.ConfigPath = filepath.Join(cfg.WorkDir, "config.toml")
	}
	configPath, err := filepath.Abs(cfg.ConfigPath)
	if err != nil {
		return SupervisorConfig{}, fmt.Errorf("resolve supervisor config path: %w", err)
	}
	cfg.ConfigPath = filepath.Clean(configPath)
	if cfg.WorkDir == "" {
		cfg.WorkDir = filepath.Dir(cfg.ConfigPath)
	}
	workDir, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return SupervisorConfig{}, fmt.Errorf("resolve supervisor work directory: %w", err)
	}
	cfg.WorkDir = filepath.Clean(workDir)
	if cfg.RestartDelay <= 0 {
		cfg.RestartDelay = defaultSupervisorRestartDelay
	}
	if cfg.LogFile == "" {
		cfg.LogFile = DefaultLogFile()
	}
	if cfg.LogMaxSize <= 0 {
		cfg.LogMaxSize = DefaultLogMaxSize
	}
	if cfg.LogBackups < 1 {
		cfg.LogBackups = DefaultLogMaxBackups
	}
	return cfg, nil
}

func instanceLockPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "."+filepath.Base(configPath)+".lock")
}
