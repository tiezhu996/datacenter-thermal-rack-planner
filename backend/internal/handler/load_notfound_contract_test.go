package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"datacenter-thermal-capacity-planner/backend/internal/audit"
	"datacenter-thermal-capacity-planner/backend/internal/auth"
	"datacenter-thermal-capacity-planner/backend/internal/config"
	"datacenter-thermal-capacity-planner/backend/internal/database"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/handler"
	"datacenter-thermal-capacity-planner/backend/internal/middleware"
	"datacenter-thermal-capacity-planner/backend/internal/planner"
	"datacenter-thermal-capacity-planner/backend/internal/repository"
	"datacenter-thermal-capacity-planner/backend/internal/router"
	"datacenter-thermal-capacity-planner/backend/internal/service"
	"datacenter-thermal-capacity-planner/backend/internal/web"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type load404Env struct {
	engine *gin.Engine
	db     *gorm.DB
	token  string
}

func newLoad404Env(t *testing.T) *load404Env {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		Port: "0", DBDriver: "sqlite", DBDSN: "file:" + filepath.Join(t.TempDir(), "test.db") + "?_busy_timeout=10000",
		DBAutoMigrate: true, JWTSecret: "load-notfound-contract-secret-0123456789abcdef",
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

	body, _ := json.Marshal(auth.LoginRequest{Username: "planner", Password: "planner123"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	var env web.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	data, _ := json.Marshal(env.Data)
	var lr auth.LoginResponse
	if err := json.Unmarshal(data, &lr); err != nil {
		t.Fatalf("decode login data: %v", err)
	}
	return &load404Env{engine: engine, db: db, token: lr.Token}
}

func (e *load404Env) do(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.engine.ServeHTTP(rec, req)
	return rec
}

func TestMissingLoadGetReturns404(t *testing.T) {
	env := newLoad404Env(t)
	rec := env.do(t, http.MethodGet, "/api/v1/loads/9999", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing load: status %d body=%s", rec.Code, rec.Body.String())
	}
	var resp web.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND error, got %+v", resp.Error)
	}
}

func TestMissingLoadUpdateReturns404(t *testing.T) {
	env := newLoad404Env(t)
	body, _ := json.Marshal(dto.UpdateEquipmentLoadRequest{
		Name: "ghost", PowerKW: 5, HeatKW: 5, AirflowCFM: 1000, RackUnits: 4,
		RedundancyGroup: "G", LoadStatus: "ready",
	})
	rec := env.do(t, http.MethodPut, "/api/v1/loads/9999", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT missing load: status %d body=%s", rec.Code, rec.Body.String())
	}
}
