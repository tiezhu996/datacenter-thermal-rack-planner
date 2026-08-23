package planner_test

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
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/handler"
	"datacenter-thermal-capacity-planner/backend/internal/middleware"
	"datacenter-thermal-capacity-planner/backend/internal/planner"
	"datacenter-thermal-capacity-planner/backend/internal/model"
	"datacenter-thermal-capacity-planner/backend/internal/repository"
	"datacenter-thermal-capacity-planner/backend/internal/router"
	"datacenter-thermal-capacity-planner/backend/internal/service"
	"datacenter-thermal-capacity-planner/backend/internal/web"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type adjHTTPEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	token  string
}

func newAdjHTTPEnv(t *testing.T) *adjHTTPEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		Port: "0", DBDriver: "sqlite", DBDSN: "file:" + filepath.Join(t.TempDir(), "test.db") + "?_busy_timeout=10000",
		DBAutoMigrate: true, JWTSecret: "adj-contract-secret-0123456789abcdef",
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
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var lr auth.LoginResponse
	_ = json.Unmarshal(data, &lr)
	return &adjHTTPEnv{engine: engine, db: db, token: lr.Token}
}

func (e *adjHTTPEnv) do(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.engine.ServeHTTP(rec, req)
	return rec
}

func TestSelfAdjacencyProhibited(t *testing.T) {
	env := newAdjHTTPEnv(t)
	body, _ := json.Marshal(dto.CreateThermalZoneRequest{
		ZoneCode: "TZ-SELF", Name: "self zone", CoolingCapacityKW: 50, SupplyTempC: 18,
		MaxReturnTempC: 30, Adjacency: map[string]float64{"TZ-SELF": 0.5}, ZoneStatus: "active",
	})
	rec := env.do(t, http.MethodPost, "/api/v1/zones", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("self adjacency create: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdjacencyKeysNormalized(t *testing.T) {
	env := newAdjHTTPEnv(t)
	var zone model.ThermalZone
	if err := env.db.Where("zone_code = ?", "TZ-B").First(&zone).Error; err != nil {
		t.Fatalf("load zone TZ-B: %v", err)
	}
	updateBody, _ := json.Marshal(dto.UpdateThermalZoneRequest{
		Name: zone.Name, CoolingCapacityKW: zone.CoolingCapacityKW, SupplyTempC: zone.SupplyTempC,
		MaxReturnTempC: zone.MaxReturnTempC, Adjacency: map[string]float64{"tz-a": 0.33}, ZoneStatus: zone.ZoneStatus,
	})
	rec := env.do(t, http.MethodPut, fmt.Sprintf("/api/v1/zones/%d", zone.ID), updateBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("update zone: status %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var resp dto.ThermalZoneResponse
	_ = json.Unmarshal(data, &resp)
	_, ok := resp.Adjacency["TZ-A"]
	if !ok {
		t.Fatalf("adjacency keys not normalized to uppercase: %+v", resp.Adjacency)
	}
}

func TestCorruptAdjacencyReturnsEmptyMap(t *testing.T) {
	env := newAdjHTTPEnv(t)
	var zone model.ThermalZone
	if err := env.db.First(&zone).Error; err != nil {
		t.Fatalf("load zone: %v", err)
	}
	if err := env.db.Model(&model.ThermalZone{}).Where("id = ?", zone.ID).Update("adjacency_json", "not-json").Error; err != nil {
		t.Fatalf("corrupt adjacency: %v", err)
	}
	rec := env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/zones/%d", zone.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET zone: status %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var resp dto.ThermalZoneResponse
	_ = json.Unmarshal(data, &resp)
	if resp.Adjacency == nil {
		t.Fatalf("adjacency should be empty map, got nil")
	}
	if len(resp.Adjacency) != 0 {
		t.Fatalf("adjacency should be empty, got %+v", resp.Adjacency)
	}
}
