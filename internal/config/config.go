package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv               string
	HTTPAddr             string
	PublicBaseURL        string
	DatabaseURL          string
	AdminUsername        string
	AdminPassword        string
	AdminAPIToken        string
	BackendPublicIP      string
	NodeAgentDefaultPort int
}

func Load() Config {
	_ = godotenv.Load()
	return Config{
		AppEnv:               getenv("APP_ENV", "dev"),
		HTTPAddr:             getenv("HTTP_ADDR", ":8080"),
		PublicBaseURL:        getenv("PUBLIC_BASE_URL", "https://sub.vetka.tech"),
		DatabaseURL:          getenv("DATABASE_URL", "postgres://vetka:vetka@localhost:5432/vetka_backend?sslmode=disable"),
		AdminUsername:        getenv("ADMIN_USERNAME", "admin"),
		AdminPassword:        getenv("ADMIN_PASSWORD", "change-me"),
		AdminAPIToken:        getenv("ADMIN_API_TOKEN", "change-me-token"),
		BackendPublicIP:      getenv("BACKEND_PUBLIC_IP", "127.0.0.1"),
		NodeAgentDefaultPort: getenvInt("NODE_AGENT_DEFAULT_PORT", 2222),
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}
