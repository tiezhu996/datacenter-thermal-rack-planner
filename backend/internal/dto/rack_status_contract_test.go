package dto_test

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
)

func TestMaintenanceRackNeverReceivesLoad(t *testing.T) {
	if constants.RackCanReceiveLoad(constants.RackMaintenance) {
		t.Fatalf("maintenance rack must not be able to receive load")
	}
	if !constants.RackCanReceiveLoad(constants.RackReserved) {
		t.Fatalf("reserved rack should still be able to receive load")
	}
}

func TestReservedRackReceivesLoad(t *testing.T) {
	zones := []model.ThermalZone{
		{ID: 1, ZoneCode: "TZ-A", CoolingCapacityKW: 40, SupplyTempC: 18, MaxReturnTempC: 31, AdjacencyJSON: `{}`, ZoneStatus: "active"},
	}
	racks := []model.Rack{
		{ID: 2, ZoneID: 1, RackCode: "R-01", PowerLimitKW: 24, AirflowLimitCFM: 7000, RackUnits: 42, RackStatus: constants.RackReserved},
		{ID: 3, ZoneID: 1, RackCode: "U-01", PowerLimitKW: 24, AirflowLimitCFM: 7000, RackUnits: 42, RackStatus: constants.RackUnavailable},
	}
	loads := []model.EquipmentLoad{
		{ID: 1, Name: "L1", PowerKW: 5, HeatKW: 4, AirflowCFM: 1200, RackUnits: 4, RedundancyGroup: "G1", LoadStatus: "ready"},
	}
	result := planner.NewEngine(100).Evaluate(zones, racks, loads)
	if len(result.Assignments) == 0 {
		t.Fatalf("reserved rack should be usable for placement, got unplaced violations=%+v", result.Violations)
	}
	for _, a := range result.Assignments {
		if a.RackID == 3 {
			t.Fatalf("load placed on unavailable rack: %+v", a)
		}
	}
}

type rackHTTPEnv struct {
	engine *gin.Engine
	token  string
}

func newRackHTTPEnv(t *testing.T) *rackHTTPEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		Port: "0", DBDriver: "sqlite", DBDSN: "file:" + filepath.Join(t.TempDir(), "test.db") + "?_busy_timeout=10000",
		DBAutoMigrate: true, JWTSecret: "rack-status-contract-secret-0123456789abcdef",
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
	return &rackHTTPEnv{engine: engine, token: lr.Token}
}

func (e *rackHTTPEnv) do(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.engine.ServeHTTP(rec, req)
	return rec
}

func TestInvalidRackStatusDisallowed(t *testing.T) {
	env := newRackHTTPEnv(t)
	body, _ := json.Marshal(dto.CreateRackRequest{
		ZoneID: 1, RackCode: "BAD-01", RowIndex: 8, ColumnIndex: 8,
		PowerLimitKW: 20, AirflowLimitCFM: 6000, RackUnits: 42, RackStatus: constants.RackStatus("decommissioned"),
	})
	rec := env.do(t, http.MethodPost, "/api/v1/racks", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid rack status create: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNewRackStartsWithVersionOne(t *testing.T) {
	env := newRackHTTPEnv(t)
	body, _ := json.Marshal(dto.CreateRackRequest{
		ZoneID: 1, RackCode: "v-01", RowIndex: 7, ColumnIndex: 7,
		PowerLimitKW: 20, AirflowLimitCFM: 6000, RackUnits: 42, RackStatus: constants.RackAvailable,
	})
	rec := env.do(t, http.MethodPost, "/api/v1/racks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create rack: status %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var resp dto.RackResponse
	_ = json.Unmarshal(data, &resp)
	if resp.RackCode != "V-01" {
		t.Fatalf("new rack code = %q, want uppercase V-01", resp.RackCode)
	}
	if resp.Version != 1 {
		t.Fatalf("new rack version = %d, want 1", resp.Version)
	}
}
