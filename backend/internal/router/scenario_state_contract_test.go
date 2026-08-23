package router_test

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

type stateEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	tokens map[string]string
}

func newStateEnv(t *testing.T) *stateEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		Port: "0", DBDriver: "sqlite", DBDSN: "file:" + filepath.Join(t.TempDir(), "test.db") + "?_busy_timeout=10000",
		DBAutoMigrate: true, JWTSecret: "scenario-state-contract-secret-0123456789abcdef",
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

	env := &stateEnv{engine: engine, db: db, tokens: map[string]string{}}
	for _, u := range []struct{ name, pass string }{{"planner", "planner123"}, {"reviewer", "reviewer123"}, {"admin", "admin123"}} {
		body, _ := json.Marshal(auth.LoginRequest{Username: u.name, Password: u.pass})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		var envelope web.Envelope
		_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
		data, _ := json.Marshal(envelope.Data)
		var lr auth.LoginResponse
		_ = json.Unmarshal(data, &lr)
		env.tokens[u.name] = lr.Token
	}
	return env
}

func (e *stateEnv) do(t *testing.T, method, path string, body []byte, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.tokens[role])
	rec := httptest.NewRecorder()
	e.engine.ServeHTTP(rec, req)
	return rec
}

func (e *stateEnv) createDraft(t *testing.T) (uint, uint) {
	t.Helper()
	var ids []uint
	if err := e.db.Model(&model.EquipmentLoad{}).Where("load_status = ?", "ready").Order("id ASC").Limit(1).Pluck("id", &ids).Error; err != nil {
		t.Fatalf("load ids: %v", err)
	}
	body, _ := json.Marshal(map[string]any{"name": fmt.Sprintf("state draft %d", time.Now().UnixNano()), "load_ids": ids})
	rec := e.do(t, http.MethodPost, "/api/v1/scenarios", body, "planner")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create draft: status %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var sc dto.ScenarioResponse
	_ = json.Unmarshal(data, &sc)
	return sc.ID, sc.Version
}

func TestDraftCannotSkipToApproved(t *testing.T) {
	env := newStateEnv(t)
	id, version := env.createDraft(t)
	body, _ := json.Marshal(dto.TransitionScenarioRequest{TargetStatus: constants.ScenarioApproved, Version: version, Reason: "skip"})
	rec := env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/scenarios/%d/transition", id), body, "reviewer")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("draft->approved should be rejected: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPendingReviewCanApprove(t *testing.T) {
	env := newStateEnv(t)
	id, version := env.createDraft(t)
	body, _ := json.Marshal(dto.EvaluateScenarioRequest{Version: version})
	rec := env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/scenarios/%d/evaluate", id), body, "planner")
	if rec.Code != http.StatusOK {
		t.Fatalf("evaluate: status %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope web.Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	data, _ := json.Marshal(envelope.Data)
	var sc dto.ScenarioResponse
	_ = json.Unmarshal(data, &sc)
	if sc.ScenarioStatus != constants.ScenarioPendingReview {
		t.Fatalf("scenario status = %s, want pending_review", sc.ScenarioStatus)
	}
	body, _ = json.Marshal(dto.TransitionScenarioRequest{TargetStatus: constants.ScenarioApproved, Version: sc.Version, Reason: "ok"})
	rec = env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/scenarios/%d/transition", id), body, "reviewer")
	if rec.Code != http.StatusOK {
		t.Fatalf("pending_review->approved should succeed: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestApprovedCanArchive(t *testing.T) {
	env := newStateEnv(t)
	sc := model.LayoutScenario{
		Name: fmt.Sprintf("archive %d", time.Now().UnixNano()), ScenarioStatus: constants.ScenarioApproved,
		RackAssignmentsJSON: "[]", InputSnapshotJSON: "{}", ZoneResultsJSON: "[]",
		ConstraintViolationsJSON: "[]", AlgorithmVersion: "thermal-v1", Version: 1, CreatedBy: 1,
	}
	if err := env.db.Create(&sc).Error; err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	body, _ := json.Marshal(dto.TransitionScenarioRequest{TargetStatus: constants.ScenarioArchived, Version: 1, Reason: "archive"})
	rec := env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/scenarios/%d/transition", sc.ID), body, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("approved->archived should succeed: status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReviewerCannotArchive(t *testing.T) {
	env := newStateEnv(t)
	sc := model.LayoutScenario{
		Name: fmt.Sprintf("archive denied %d", time.Now().UnixNano()), ScenarioStatus: constants.ScenarioApproved,
		RackAssignmentsJSON: "[]", InputSnapshotJSON: "{}", ZoneResultsJSON: "[]",
		ConstraintViolationsJSON: "[]", AlgorithmVersion: "thermal-v1", Version: 1, CreatedBy: 1,
	}
	if err := env.db.Create(&sc).Error; err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	body, _ := json.Marshal(dto.TransitionScenarioRequest{TargetStatus: constants.ScenarioArchived, Version: 1, Reason: "nope"})
	rec := env.do(t, http.MethodPost, fmt.Sprintf("/api/v1/scenarios/%d/transition", sc.ID), body, "reviewer")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reviewer archive should be forbidden: status %d body=%s", rec.Code, rec.Body.String())
	}
}
