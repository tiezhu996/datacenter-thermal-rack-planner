package web_test

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

type deferEnv struct {
	engine   *gin.Engine
	db       *gorm.DB
	zoneRepo *repository.ThermalZoneRepository
	rackRepo *repository.RackRepository
	token    string
}

func newDeferEnv(t *testing.T) *deferEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		Port: "0", DBDriver: "sqlite", DBDSN: "file:" + filepath.Join(t.TempDir(), "test.db") + "?_busy_timeout=10000",
		DBAutoMigrate: true, JWTSecret: "defer-contract-secret-0123456789abcdef",
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
	return &deferEnv{engine: engine, db: db, zoneRepo: zoneRepo, rackRepo: rackRepo, token: lr.Token}
}

func (e *deferEnv) do(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.engine.ServeHTTP(rec, req)
	return rec
}

func (e *deferEnv) auditCount(t *testing.T, entityType string, entityID uint) int {
	var count int64
	if err := e.db.Model(&audit.Event{}).Where("entity_type = ? AND entity_id = ?", entityType, entityID).Count(&count).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return int(count)
}

func TestDuplicateRackCodeReturnsConflict(t *testing.T) {
	env := newDeferEnv(t)
	var zone model.ThermalZone
	if err := env.db.First(&zone).Error; err != nil {
		t.Fatalf("load zone: %v", err)
	}
	body, _ := json.Marshal(dto.CreateRackRequest{
		ZoneID: zone.ID, RackCode: "DUP-01", RowIndex: 9, ColumnIndex: 9,
		PowerLimitKW: 20, AirflowLimitCFM: 6000, RackUnits: 42, RackStatus: constants.RackAvailable,
	})
	rec := env.do(t, http.MethodPost, "/api/v1/racks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first rack create: status %d body=%s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/v1/racks", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate rack: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateMissingRackReturnsNotFound(t *testing.T) {
	env := newDeferEnv(t)
	var zone model.ThermalZone
	if err := env.db.First(&zone).Error; err != nil {
		t.Fatalf("load zone: %v", err)
	}
	body, _ := json.Marshal(dto.UpdateRackRequest{
		ZoneID: zone.ID, RowIndex: 3, ColumnIndex: 3, PowerLimitKW: 20, AirflowLimitCFM: 6000,
		RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1,
	})
	rec := env.do(t, http.MethodPut, "/api/v1/racks/9999", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT missing rack: status %d body=%s", rec.Code, rec.Body.String())
	}
	if n := env.auditCount(t, "rack", 9999); n != 0 {
		t.Fatalf("missing rack update should not record audit, got %d", n)
	}
}

func TestUpdateRackStaleVersionBlocked(t *testing.T) {
	env := newDeferEnv(t)
	var zone model.ThermalZone
	if err := env.db.First(&zone).Error; err != nil {
		t.Fatalf("load zone: %v", err)
	}
	var rack model.Rack
	if err := env.db.Where("zone_id = ?", zone.ID).First(&rack).Error; err != nil {
		t.Fatalf("load rack: %v", err)
	}
	body, _ := json.Marshal(dto.UpdateRackRequest{
		ZoneID: zone.ID, RowIndex: 5, ColumnIndex: 5, PowerLimitKW: 20, AirflowLimitCFM: 6000,
		RackUnits: 42, RackStatus: constants.RackAvailable, Version: rack.Version + 100,
	})
	rec := env.do(t, http.MethodPut, fmt.Sprintf("/api/v1/racks/%d", rack.ID), body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale version rack update: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDuplicateZoneCodeReturnsConflict(t *testing.T) {
	env := newDeferEnv(t)
	body, _ := json.Marshal(dto.CreateThermalZoneRequest{
		ZoneCode: "TZ-DUP", Name: "duplicate zone", CoolingCapacityKW: 50, SupplyTempC: 18,
		MaxReturnTempC: 30, Adjacency: map[string]float64{}, ZoneStatus: "active",
	})
	rec := env.do(t, http.MethodPost, "/api/v1/zones", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first zone create: status %d body=%s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/v1/zones", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate zone: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMissingZoneDetailNotFound(t *testing.T) {
	env := newDeferEnv(t)
	rec := env.do(t, http.MethodGet, "/api/v1/zones/9999", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing zone: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func asAppError(err error, target **web.AppError) bool {
	for err != nil {
		if appErr, ok := err.(*web.AppError); ok {
			*target = appErr
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
