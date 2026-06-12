package config

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv                          string
	HTTPAddr                        string
	AppTimezone                     string
	PublicBaseURL                   string
	PanelPublicBaseURL              string
	SubscriptionPublicBaseURL       string
	SubscriptionProfileTitle        string
	SubscriptionUpdateIntervalHours int
	ExpiryReconcileEnabled          bool
	ExpiryReconcileInterval         time.Duration
	DatabaseURL                     string
	AdminUsername                   string
	AdminPassword                   string
	AdminAPIToken                   string
	BackendPublicIP                 string
	NodeAgentDefaultPort            int
}

func Load() Config {
	_ = godotenv.Load()
	publicBaseURL := getenv("PUBLIC_BASE_URL", "https://sub.vetka.tech")
	panelBaseURL := getenv("PANEL_PUBLIC_BASE_URL", publicBaseURL)
	subscriptionBaseURL := getenv("SUBSCRIPTION_PUBLIC_BASE_URL", publicBaseURL)
	return Config{
		AppEnv:                          getenv("APP_ENV", "dev"),
		HTTPAddr:                        getenv("HTTP_ADDR", ":8080"),
		AppTimezone:                     getenv("APP_TIMEZONE", "Europe/Moscow"),
		PublicBaseURL:                   publicBaseURL,
		PanelPublicBaseURL:              panelBaseURL,
		SubscriptionPublicBaseURL:       subscriptionBaseURL,
		SubscriptionProfileTitle:        getenv("SUBSCRIPTION_PROFILE_TITLE", "Ветка VPN"),
		SubscriptionUpdateIntervalHours: getenvInt("SUBSCRIPTION_UPDATE_INTERVAL_HOURS", 12),
		ExpiryReconcileEnabled:          getenvBool("EXPIRY_RECONCILE_ENABLED", true),
		ExpiryReconcileInterval:         getenvDuration("EXPIRY_RECONCILE_INTERVAL", time.Minute),
		DatabaseURL:                     getenv("DATABASE_URL", "postgres://vetka:vetka@localhost:5432/vetka_backend?sslmode=disable"),
		AdminUsername:                   getenv("ADMIN_USERNAME", "admin"),
		AdminPassword:                   getenv("ADMIN_PASSWORD", "change-me"),
		AdminAPIToken:                   getenv("ADMIN_API_TOKEN", "change-me-token"),
		BackendPublicIP:                 getenv("BACKEND_PUBLIC_IP", "127.0.0.1"),
		NodeAgentDefaultPort:            getenvInt("NODE_AGENT_DEFAULT_PORT", 2222),
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

func getenvBool(key string, fallback bool) bool {
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

func getenvDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
