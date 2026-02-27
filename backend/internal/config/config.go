package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Environment     string
	AppName         string
	Port            string
	LogLevel        slog.Level
	SQLitePath      string
	MigrationsPath  string
	SeedDefaultData bool
	PollingEnabled  bool
	PollingMinutes  int
}

func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		Environment:     getEnv("APP_ENV", "development"),
		AppName:         getEnv("APP_NAME", "cross-site-tracker"),
		Port:            getEnv("APP_PORT", "8080"),
		SQLitePath:      getEnv("SQLITE_PATH", "./data/app.sqlite"),
		MigrationsPath:  getEnv("MIGRATIONS_PATH", "./migrations"),
		SeedDefaultData: getEnvAsBool("SEED_DEFAULT_DATA", true),
		PollingEnabled:  getEnvAsBool("POLLING_ENABLED", true),
		PollingMinutes:  getEnvAsInt("POLLING_MINUTES", 30),
	}

	if cfg.PollingMinutes <= 0 {
		cfg.PollingMinutes = 30
	}

	level, err := parseLogLevel(getEnv("LOG_LEVEL", "INFO"))
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = level

	return cfg, nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch raw {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARN":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid LOG_LEVEL %q, expected DEBUG|INFO|WARN|ERROR", raw)
	}
}

func getEnv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getEnvAsBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvAsInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
