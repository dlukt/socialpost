package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config contains shared runtime configuration for all binaries.
type Config struct {
	ServiceName   string
	Environment   string
	LogLevel      string
	APIListenAddr string
	PostgresURL   string
	RedisAddr     string
	RedisPassword string
	RedisDB       int
}

func Load(serviceName string) (Config, error) {
	cfg := Config{
		ServiceName:   serviceName,
		Environment:   getEnv("APP_ENV", "development"),
		LogLevel:      getEnv("LOG_LEVEL", "info"),
		APIListenAddr: getEnv("API_LISTEN_ADDR", ":8080"),
		PostgresURL:   getEnv("POSTGRES_URL", "postgres://postgres:postgres@localhost:5432/mixpost_go?sslmode=disable"),
		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
	}

	redisDB, err := getEnvInt("REDIS_DB", 0)
	if err != nil {
		return Config{}, fmt.Errorf("invalid REDIS_DB: %w", err)
	}
	cfg.RedisDB = redisDB

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}
