package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
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

type guardTestEnv struct {
	engine *gin.Engine
	db     *gorm.DB
	scen   *repository.LayoutScenarioRepository
	tokens map[string]string
}

func newGuardTestEnv(t *testing.T) *guardTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		Port: "0", DBDriver: "sqlite", DBDSN: "file:" + filepath.Join(t.TempDir(), "test.db") + "?_busy_timeout=10000&_txlock=immediate",
		DBAutoMigrate: true, JWTSecret: "scenario-guard-contract-secret-0123456789abcdef",
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

	env := &guardTestEnv{engine: engine, db: db, scen: scenarioRepo, tokens: map[string]string{}}
	env.tokens["planner"] = env.login(t, "planner", "planner123")
	env.tokens["reviewer"] = env.login(t, "reviewer", "reviewer123")
	env.tokens["admin"] = env.login(t, "admin", "admin123")
	return env
}

func (e *guardTestEnv) login(t *testing.T, username, password string) string {
	t.Helper()
	body, _ := json.Marshal(auth.LoginRequest{Username: username, Password: password})
	rec := e.do(t, http.MethodPost, "/api/v1/auth/login", body, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: status %d", username, rec.Code)
	}
	var resp web.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	data, _ := json.Marshal(resp.Data)
	var lr auth.LoginResponse
	if err := json.Unmarshal(data, &lr); err != nil {
		t.Fatalf("decode login data: %v", err)
	}
	return lr.Token
}

func (e *guardTestEnv) do(t *testing.T, method, path string, body []byte, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.engine.ServeHTTP(rec, req)
	return rec
}

func (e *guardTestEnv) createDraft(t *testing.T) (uint, uint) {
	t.Helper()
	var ids []uint
	if err := e.db.Model(&model.EquipmentLoad{}).Where("load_status = ?", "ready").Order("id ASC").Limit(2).Pluck("id", &ids).Error; err != nil {
		t.Fatalf("load ids: %v", err)
	}
	body, _ := json.Marshal(map[string]any{"name": fmt.Sprintf("guard draft %d", time.Now().UnixNano()), "load_ids": ids})
	rec := e.do(t, http.MethodPost, "/api/v1/scenarios", body, e.tokens["planner"])
	if rec.Code != http.StatusCreated {
		t.Fatalf("create draft: status %d body=%s", rec.Code, rec.Body.String())
	}
	var resp web.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	data, _ := json.Marshal(resp.Data)
	var sc dto.ScenarioResponse
	if err := json.Unmarshal(data, &sc); err != nil {
		t.Fatalf("decode scenario: %v", err)
	}
	return sc.ID, sc.Version
}

func (e *guardTestEnv) evaluate(t *testing.T, id, version uint, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(dto.EvaluateScenarioRequest{Version: version})
	return e.do(t, http.MethodPost, fmt.Sprintf("/api/v1/scenarios/%d/evaluate", id), body, token)
}

func (e *guardTestEnv) transition(t *testing.T, id, version uint, target string, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(dto.TransitionScenarioRequest{TargetStatus: constants.ScenarioStatus(target), Version: version, Reason: "contract"})
	return e.do(t, http.MethodPost, fmt.Sprintf("/api/v1/scenarios/%d/transition", id), body, token)
}

func (e *guardTestEnv) auditFinishCount(t *testing.T) int {
	var count int64
	if err := e.db.Model(&audit.Event{}).Where("action = ?", "layout_scenario.evaluate.finish").Count(&count).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return int(count)
}

func (e *guardTestEnv) auditApproveCount(t *testing.T) int {
	var count int64
	if err := e.db.Model(&audit.Event{}).Where("action = ?", "layout_scenario.approve").Count(&count).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return int(count)
}

func (e *guardTestEnv) scenarioVersion(t *testing.T, id uint) (uint, constants.ScenarioStatus) {
	t.Helper()
	var sc model.LayoutScenario
	if err := e.db.First(&sc, id).Error; err != nil {
		t.Fatalf("load scenario: %v", err)
	}
	return sc.Version, sc.ScenarioStatus
}

func TestScenarioConcurrentEvaluateOneWins(t *testing.T) {
	env := newGuardTestEnv(t)
	id, version := env.createDraft(t)

	start := make(chan struct{})
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			rec := env.evaluate(t, id, version, env.tokens["planner"])
			codes[idx] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()

	success, conflict := 0, 0
	for _, code := range codes {
		if code == http.StatusOK {
			success++
		}
		if code == http.StatusConflict {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("expected exactly one 200 and one 409, got codes=%v", codes)
	}
	finalVersion, status := env.scenarioVersion(t, id)
	if finalVersion != 3 || status != constants.ScenarioPendingReview {
		t.Fatalf("expected version 3 pending_review, got version=%d status=%s", finalVersion, status)
	}
	if n := env.auditFinishCount(t); n != 1 {
		t.Fatalf("expected 1 evaluate.finish audit event, got %d", n)
	}
}

func TestScenarioEvaluateStaleVersionGuardOn(t *testing.T) {
	env := newGuardTestEnv(t)
	id, version := env.createDraft(t)
	rec := env.evaluate(t, id, version, env.tokens["planner"])
	if rec.Code != http.StatusOK {
		t.Fatalf("first evaluate should succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	rec = env.evaluate(t, id, version, env.tokens["planner"])
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale evaluate should conflict, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestScenarioConcurrentTransitionOneWins(t *testing.T) {
	env := newGuardTestEnv(t)
	id, version := env.createDraft(t)
	rec := env.evaluate(t, id, version, env.tokens["planner"])
	if rec.Code != http.StatusOK {
		t.Fatalf("evaluate should succeed, got %d", rec.Code)
	}
	finalVersion, _ := env.scenarioVersion(t, id)

	start := make(chan struct{})
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			r := env.transition(t, id, finalVersion, "approved", env.tokens["reviewer"])
			codes[idx] = r.Code
		}(i)
	}
	close(start)
	wg.Wait()

	success, conflict := 0, 0
	for _, code := range codes {
		if code == http.StatusOK {
			success++
		}
		if code == http.StatusConflict {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("expected one 200 and one 409, got codes=%v", codes)
	}
	if n := env.auditApproveCount(t); n != 1 {
		t.Fatalf("expected 1 approve audit event, got %d", n)
	}
}

func TestScenarioTransitionStaleVersionGuardOn(t *testing.T) {
	env := newGuardTestEnv(t)
	id, version := env.createDraft(t)
	rec := env.evaluate(t, id, version, env.tokens["planner"])
	if rec.Code != http.StatusOK {
		t.Fatalf("evaluate should succeed, got %d", rec.Code)
	}
	rec = env.transition(t, id, 1, "approved", env.tokens["reviewer"])
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale transition should conflict, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFinishEvaluationStaleVersionGuardOn(t *testing.T) {
	env := newGuardTestEnv(t)
	sc := model.LayoutScenario{
		Name: fmt.Sprintf("finish guard %d", time.Now().UnixNano()), ScenarioStatus: constants.ScenarioDraft,
		RackAssignmentsJSON: "[]", InputSnapshotJSON: "{}", ZoneResultsJSON: "[]",
		ConstraintViolationsJSON: "[]", AlgorithmVersion: "thermal-v1", Version: 1, CreatedBy: 1,
	}
	if err := env.db.Create(&sc).Error; err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	if err := env.db.Model(&model.LayoutScenario{}).Where("id = ?", sc.ID).Updates(map[string]any{"scenario_status": constants.ScenarioEvaluating, "version": 2}).Error; err != nil {
		t.Fatalf("set evaluating: %v", err)
	}
	sc.ScenarioStatus = constants.ScenarioEvaluating
	sc.Version = 2
	update := repository.EvaluationUpdate{
		AssignmentsJSON: "[]", SnapshotJSON: "{}", ZoneResultsJSON: "[]", ViolationsJSON: "[]",
		TotalPowerKW: 0, PeakTempC: 0, Score: 100,
	}
	entry := audit.Entry{RequestID: "finish-guard", ActorID: 1, ActorUsername: "planner", Action: "layout_scenario.evaluate.finish", EntityType: "layout_scenario"}

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = env.scen.FinishEvaluation(context.Background(), sc, update, entry)
		}(i)
	}
	close(start)
	wg.Wait()

	ok, conflict := 0, 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
		var appErr *web.AppError
		if err != nil && jsonErrAs(err, &appErr) && appErr.Code == "SCENARIO_VERSION_CONFLICT" {
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("expected one success and one version conflict, got ok=%d conflict=%d errs=%v", ok, conflict, errs)
	}
}

func jsonErrAs(err error, target **web.AppError) bool {
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
