package dto

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"datacenter-thermal-capacity-planner/backend/internal/model"
)

type CreateThermalZoneRequest struct {
	ZoneCode          string             `json:"zone_code" binding:"required,min=2,max=32"`
	Name              string             `json:"name" binding:"required,min=2,max=120"`
	CoolingCapacityKW float64            `json:"cooling_capacity_kw" binding:"required,gt=0,lte=10000"`
	SupplyTempC       float64            `json:"supply_temp_c" binding:"required,gte=10,lte=30"`
	MaxReturnTempC    float64            `json:"max_return_temp_c" binding:"required,gte=18,lte=60"`
	Adjacency         map[string]float64 `json:"adjacency" binding:"required"`
	ZoneStatus        string             `json:"zone_status" binding:"required,oneof=active constrained offline"`
}

type UpdateThermalZoneRequest struct {
	Name              string             `json:"name" binding:"required,min=2,max=120"`
	CoolingCapacityKW float64            `json:"cooling_capacity_kw" binding:"required,gt=0,lte=10000"`
	SupplyTempC       float64            `json:"supply_temp_c" binding:"required,gte=10,lte=30"`
	MaxReturnTempC    float64            `json:"max_return_temp_c" binding:"required,gte=18,lte=60"`
	Adjacency         map[string]float64 `json:"adjacency" binding:"required"`
	ZoneStatus        string             `json:"zone_status" binding:"required,oneof=active constrained offline"`
}

type ThermalZoneResponse struct {
	ID                  uint               `json:"id"`
	ZoneCode            string             `json:"zone_code"`
	Name                string             `json:"name"`
	CoolingCapacityKW   float64            `json:"cooling_capacity_kw"`
	SupplyTempC         float64            `json:"supply_temp_c"`
	MaxReturnTempC      float64            `json:"max_return_temp_c"`
	Adjacency           map[string]float64 `json:"adjacency"`
	ZoneStatus          string             `json:"zone_status"`
	RackCount           int64              `json:"rack_count"`
	AllocatedPowerKW    float64            `json:"allocated_power_kw"`
	CapacityUtilization float64            `json:"capacity_utilization"`
	TemperatureHeadroom float64            `json:"temperature_headroom_c"`
}

func (r CreateThermalZoneRequest) ValidateBusiness() error {
	if strings.TrimSpace(r.ZoneCode) == "" {
		return errors.New("zone code is required")
	}
	if r.MaxReturnTempC <= r.SupplyTempC {
		return errors.New("max return temperature must exceed supply temperature")
	}
	return validateAdjacency(r.ZoneCode, r.Adjacency)
}

func (r UpdateThermalZoneRequest) ValidateBusiness(zoneCode string) error {
	if r.MaxReturnTempC <= r.SupplyTempC {
		return errors.New("max return temperature must exceed supply temperature")
	}
	return validateAdjacency(zoneCode, r.Adjacency)
}

func validateAdjacency(self string, adjacency map[string]float64) error {
	selfCode := normalizeZoneCode(self)
	seen := make(map[string]bool, len(adjacency))
	for code, weight := range adjacency {
		neighbor := normalizeZoneCode(code)
		if neighbor == "" {
			return fmt.Errorf("adjacency key %q is not a valid zone code", code)
		}
		if neighbor == selfCode {
			return fmt.Errorf("thermal zone %s cannot be adjacent to itself", selfCode)
		}
		if weight < 0 || weight > 1 {
			return fmt.Errorf("adjacency weight for %s must be between 0 and 1", neighbor)
		}
		if seen[neighbor] {
			return fmt.Errorf("adjacency entry for %s is duplicated", neighbor)
		}
		seen[neighbor] = true
	}
	return nil
}

func NewThermalZone(req CreateThermalZoneRequest) (model.ThermalZone, error) {
	if err := req.ValidateBusiness(); err != nil {
		return model.ThermalZone{}, err
	}
	normalized := NormalizeAdjacency(req.Adjacency)
	adjacency, err := json.Marshal(normalized)
	if err != nil {
		return model.ThermalZone{}, fmt.Errorf("encode zone adjacency: %w", err)
	}
	return model.ThermalZone{
		ZoneCode:          normalizeZoneCode(req.ZoneCode),
		Name:              strings.TrimSpace(req.Name),
		CoolingCapacityKW: req.CoolingCapacityKW,
		SupplyTempC:       req.SupplyTempC,
		MaxReturnTempC:    req.MaxReturnTempC,
		AdjacencyJSON:     string(adjacency),
		ZoneStatus:        req.ZoneStatus,
	}, nil
}

// normalizeZoneCode matches the canonical form used for zone_code so that
// adjacency keys line up with stored zone codes regardless of input case.
func normalizeZoneCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// NormalizeAdjacency upper-cases and trims every adjacency key, collapsing
// case-only duplicates. Callers must have already validated the input.
func NormalizeAdjacency(adjacency map[string]float64) map[string]float64 {
	if adjacency == nil {
		return map[string]float64{}
	}
	normalized := make(map[string]float64, len(adjacency))
	for code, weight := range adjacency {
		normalized[normalizeZoneCode(code)] = weight
	}
	return normalized
}

// DecodeAdjacency parses a stored adjacency blob. It always returns a usable
// map (never nil), so serialized responses carry `{}` instead of `null` when
// the stored value is empty or malformed.
func DecodeAdjacency(raw string) map[string]float64 {
	value := map[string]float64{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return map[string]float64{}
	}
	if value == nil {
		return map[string]float64{}
	}
	return value
}
