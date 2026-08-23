package config_test

import (
	"path/filepath"
	"testing"

	"datacenter-thermal-capacity-planner/backend/internal/config"
	"datacenter-thermal-capacity-planner/backend/internal/database"
)

func validEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PORT", "8080")
	t.Setenv("DB_DRIVER", "sqlite")
	t.Setenv("DB_DSN", "file:"+filepath.Join(t.TempDir(), "t.db"))
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "100")
	t.Setenv("PLANNER_MAX_ITERATIONS", "500")
}

func TestShortJWTSecretUnchecked(t *testing.T) {
	validEnv(t)
	t.Setenv("JWT_SECRET", "short")
	if _, err := config.Load(); err == nil {
		t.Fatalf("short JWT secret must be rejected")
	}
}

func TestUnknownDriverUncheckedByConfig(t *testing.T) {
	validEnv(t)
	t.Setenv("DB_DRIVER", "mysql")
	if _, err := config.Load(); err == nil {
		t.Fatalf("unsupported DB_DRIVER must be rejected by config validation")
	}
}

func TestInvalidPlannerIterationsUnchecked(t *testing.T) {
	validEnv(t)
	t.Setenv("PLANNER_MAX_ITERATIONS", "5")
	if _, err := config.Load(); err == nil {
		t.Fatalf("planner iterations below minimum must be rejected")
	}
}

func TestUnknownDriverUncheckedByDriver(t *testing.T) {
	cfg := config.Config{
		Port: "8080", DBDriver: "mysql", DBDSN: "file:" + filepath.Join(t.TempDir(), "t.db"),
		DBAutoMigrate: false, JWTSecret: "0123456789abcdef0123456789abcdef",
		RateLimitPerMinute: 100, PlannerMaxIterations: 500,
	}
	if _, err := database.Open(cfg); err == nil {
		t.Fatalf("unknown database driver must be rejected")
	}
}
