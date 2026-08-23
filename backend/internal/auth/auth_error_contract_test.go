package auth_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"datacenter-thermal-capacity-planner/backend/internal/audit"
	"datacenter-thermal-capacity-planner/backend/internal/auth"
	"datacenter-thermal-capacity-planner/backend/internal/config"
	"datacenter-thermal-capacity-planner/backend/internal/database"
	"datacenter-thermal-capacity-planner/backend/internal/handler"
	"datacenter-thermal-capacity-planner/backend/internal/middleware"
	"datacenter-thermal-capacity-planner/backend/internal/planner"
	"datacenter-thermal-capacity-planner/backend/internal/repository"
	"datacenter-thermal-capacity-planner/backend/internal/router"
	"datacenter-thermal-capacity-planner/backend/internal/service"
	"datacenter-thermal-capacity-planner/backend/internal/web"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

const authSecret = "auth-error-contract-secret-0123456789abcdef"

type authEnv struct {
	engine *gin.Engine
	db     *gorm.DB
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		Port: "0", DBDriver: "sqlite", DBDSN: "file:" + filepath.Join(t.TempDir(), "test.db") + "?_busy_timeout=10000",
		DBAutoMigrate: true, JWTSecret: authSecret,
		JWTTTL: time.Hour, CORSOrigins: []string{}, RateLimitPerMinute: 10000,
		PlannerMaxIterations: 500, LogLevel: "error", GracefulShutdownPeriod: time.Second,
	}
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	auditRepo := audit.NewRepository(db)
	authRepo := auth.NewRepository(db)
	zoneRepo := repository.NewThermalZoneRepository(db, auditRepo)
	rackRepo := repository.NewRackRepository(db, auditRepo)
	loadRepo := repository.NewEquipmentLoadRepository(db, auditRepo)
	scenarioRepo := repository.NewLayoutScenarioRepository(db, auditRepo)
	authService := auth.NewService(authRepo, cfg.JWTSecret, cfg.JWTTTL)
	zoneService := service.NewThermalZoneService(zoneRepo, rackRepo)
	rackService := service.NewRackService(rackRepo, zoneRepo)
	loadService := service.NewEquipmentLoadService(loadRepo, zoneRepo)
	scenarioService := service.NewLayoutScenarioService(scenarioRepo, zoneRepo, rackRepo, loadRepo, planner.NewEngine(cfg.PlannerMaxIterations))

	engine := gin.New()
	engine.Use(middleware.RequestID())
	api := engine.Group("/api/v1")
	api.POST("/auth/login", auth.NewHandler(authService).Login)
	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	write := middleware.RBAC(auth.RolePlanner, auth.RoleAdmin)
	review := middleware.RBAC(auth.RoleReviewer, auth.RoleAdmin)
	router.RegisterThermalZoneRoutes(protected, handler.NewThermalZoneHandler(zoneService), write)
	router.RegisterRackRoutes(protected, handler.NewRackHandler(rackService), write)
	router.RegisterEquipmentLoadRoutes(protected, handler.NewEquipmentLoadHandler(loadService), write)
	router.RegisterLayoutScenarioRoutes(protected, handler.NewLayoutScenarioHandler(scenarioService), write, review)
	return &authEnv{engine: engine, db: db}
}

func (e *authEnv) do(t *testing.T, method, path string, body []byte, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	e.engine.ServeHTTP(rec, req)
	return rec
}

func TestWrongPasswordReturns401(t *testing.T) {
	env := newAuthEnv(t)
	body, _ := json.Marshal(auth.LoginRequest{Username: "planner", Password: "wrong-password"})
	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", body, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func expiredToken(t *testing.T) string {
	t.Helper()
	claims := auth.Claims{
		UserID: 1, Username: "planner", Role: "planner",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "1",
			Issuer:    "datacenter-thermal-capacity-planner",
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(authSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func TestExpiredTokenReturns401(t *testing.T) {
	env := newAuthEnv(t)
	token := expiredToken(t)
	rec := env.do(t, http.MethodGet, "/api/v1/zones", nil, "Bearer "+token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInactiveAccountReturns401(t *testing.T) {
	env := newAuthEnv(t)
	body, _ := json.Marshal(auth.LoginRequest{Username: "planner", Password: "planner123"})
	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", body, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status %d", rec.Code)
	}
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var lr auth.LoginResponse
	_ = json.Unmarshal(data, &lr)
	if err := env.db.Model(&auth.User{}).Where("username = ?", "planner").Update("active", false).Error; err != nil {
		t.Fatalf("deactivate user: %v", err)
	}
	rec = env.do(t, http.MethodGet, "/api/v1/zones", nil, "Bearer "+lr.Token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("inactive account: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMissingBearerReturns401(t *testing.T) {
	env := newAuthEnv(t)
	rec := env.do(t, http.MethodGet, "/api/v1/zones", nil, "Bearer")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("malformed bearer: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestValidTokenStillWorks(t *testing.T) {
	env := newAuthEnv(t)
	body, _ := json.Marshal(auth.LoginRequest{Username: "planner", Password: "planner123"})
	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", body, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status %d", rec.Code)
	}
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var lr auth.LoginResponse
	_ = json.Unmarshal(data, &lr)
	rec = env.do(t, http.MethodGet, "/api/v1/zones", nil, "Bearer "+lr.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: status %d body=%s", rec.Code, rec.Body.String())
	}
}

var _ = fmt.Sprintf
