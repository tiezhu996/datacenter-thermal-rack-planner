package service_test

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
	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/database"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/handler"
	"datacenter-thermal-capacity-planner/backend/internal/middleware"
	"datacenter-thermal-capacity-planner/backend/internal/model"
	"datacenter-thermal-capacity-planner/backend/internal/planner"
	"datacenter-thermal-capacity-planner/backend/internal/repository"
	"datacenter-thermal-capacity-planner/backend/internal/router"
	"datacenter-thermal-capacity-planner/backend/internal/service"
	"datacenter-thermal-capacity-planner/backend/internal/web"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type nilEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	token  string
}

func newNilEnv(t *testing.T) *nilEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		Port: "0", DBDriver: "sqlite", DBDSN: "file:" + filepath.Join(t.TempDir(), "test.db") + "?_busy_timeout=10000",
		DBAutoMigrate: true, JWTSecret: "nil-contract-secret-0123456789abcdef",
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
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var lr auth.LoginResponse
	_ = json.Unmarshal(data, &lr)
	return &nilEnv{engine: engine, db: db, token: lr.Token}
}

func (e *nilEnv) do(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.engine.ServeHTTP(rec, req)
	return rec
}

func TestZeroPowerLoadHeatRatioSafe(t *testing.T) {
	env := newNilEnv(t)
	load := model.EquipmentLoad{Name: fmt.Sprintf("zero power %d", time.Now().UnixNano()), PowerKW: 0, HeatKW: 0, AirflowCFM: 100, RackUnits: 1, RedundancyGroup: "ZP", LoadStatus: "ready"}
	if err := env.db.Create(&load).Error; err != nil {
		t.Fatalf("create load: %v", err)
	}
	rec := env.do(t, http.MethodGet, fmt.Sprintf("/api/v1/loads/%d", load.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET zero power load: status %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope web.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data, _ := json.Marshal(envelope.Data)
	var resp dto.EquipmentLoadResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("decode load: %v", err)
	}
	if resp.HeatRatio != 0 {
		t.Fatalf("heat_ratio = %v, want 0", resp.HeatRatio)
	}
}

func TestPlacedLoadNotPlannable(t *testing.T) {
	env := newNilEnv(t)
	var zone model.ThermalZone
	if err := env.db.First(&zone).Error; err != nil {
		t.Fatalf("load zone: %v", err)
	}
	var rack model.Rack
	if err := env.db.Where("zone_id = ?", zone.ID).First(&rack).Error; err != nil {
		t.Fatalf("load rack: %v", err)
	}
	load := model.EquipmentLoad{Name: fmt.Sprintf("placed load %d", time.Now().UnixNano()), PowerKW: 5, HeatKW: 4, AirflowCFM: 1200, RackUnits: 4, RedundancyGroup: "PL", LoadStatus: "placed"}
	if err := env.db.Create(&load).Error; err != nil {
		t.Fatalf("create load: %v", err)
	}
	zones, err := env.zonesAll()
	if err != nil {
		t.Fatalf("zones: %v", err)
	}
	racks, err := env.racksAll()
	if err != nil {
		t.Fatalf("racks: %v", err)
	}
	result := planner.NewEngine(500).Evaluate(zones, racks, []model.EquipmentLoad{load})
	if len(result.Assignments) != 0 {
		t.Fatalf("placed load should not be assigned: %+v", result.Assignments)
	}
	found := false
	for _, v := range result.Violations {
		if v.Code == "LOAD_NOT_READY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected LOAD_NOT_READY violation, got %+v", result.Violations)
	}
}

func (e *nilEnv) zonesAll() ([]model.ThermalZone, error) {
	var zones []model.ThermalZone
	return zones, e.db.Order("zone_code ASC").Find(&zones).Error
}

func (e *nilEnv) racksAll() ([]model.Rack, error) {
	var racks []model.Rack
	return racks, e.db.Order("rack_code ASC").Find(&racks).Error
}

func TestValidateLoadWithoutPreferredZoneNoPanic(t *testing.T) {
	env := newNilEnv(t)
	load := model.EquipmentLoad{Name: fmt.Sprintf("no pref %d", time.Now().UnixNano()), PowerKW: 5, HeatKW: 4, AirflowCFM: 1200, RackUnits: 4, RedundancyGroup: "NP", LoadStatus: "ready"}
	if err := env.db.Create(&load).Error; err != nil {
		t.Fatalf("create load: %v", err)
	}
	rec := env.do(t, http.MethodPost, "/api/v1/loads/validate", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHeatRatioToleranceInValidRange(t *testing.T) {
	env := newNilEnv(t)
	body, _ := json.Marshal(dto.CreateEquipmentLoadRequest{
		Name: fmt.Sprintf("tolerant load %d", time.Now().UnixNano()), PowerKW: 10, HeatKW: 11,
		AirflowCFM: 1200, RackUnits: 4, RedundancyGroup: "TL", LoadStatus: "ready",
	})
	rec := env.do(t, http.MethodPost, "/api/v1/loads", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create load: status %d body=%s", rec.Code, rec.Body.String())
	}
	var created web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	cdata, _ := json.Marshal(created.Data)
	var createdLoad dto.EquipmentLoadResponse
	_ = json.Unmarshal(cdata, &createdLoad)

	rec = env.do(t, http.MethodPost, "/api/v1/loads/validate", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate: status %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var resp dto.BatchValidateResponse
	_ = json.Unmarshal(data, &resp)
	for _, r := range resp.Results {
		if r.LoadID == createdLoad.ID && !r.Valid {
			t.Fatalf("heat ratio 1.1x within tolerance must be valid: %+v", r.Issues)
		}
	}
}

func TestNewRackDefaults(t *testing.T) {
	// sanity: RackStatus constants remain intact for other records
	if constants.RackAvailable != "available" {
		t.Fatal("rack constants changed")
	}
}
