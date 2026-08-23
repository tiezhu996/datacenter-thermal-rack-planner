package model_test

import (
	"reflect"
	"testing"

	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/model"
	"datacenter-thermal-capacity-planner/backend/internal/planner"
)

func TestEvaluateDoesNotMutateInputRacks(t *testing.T) {
	zones, _ := sliceFixture()
	loads := []model.EquipmentLoad{
		{ID: 1, Name: "L1", PowerKW: 5, HeatKW: 4, AirflowCFM: 1200, RackUnits: 4, RedundancyGroup: "G1", LoadStatus: "ready"},
		{ID: 2, Name: "L2", PowerKW: 5, HeatKW: 4, AirflowCFM: 1200, RackUnits: 4, RedundancyGroup: "G2", LoadStatus: "ready"},
	}
	racks := []model.Rack{
		{ID: 3, ZoneID: 2, RackCode: "C-01", PowerLimitKW: 24, AirflowLimitCFM: 7000, RackUnits: 42, RackStatus: constants.RackAvailable},
		{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 24, AirflowLimitCFM: 7000, RackUnits: 42, RackStatus: constants.RackAvailable},
		{ID: 2, ZoneID: 2, RackCode: "B-01", PowerLimitKW: 24, AirflowLimitCFM: 7000, RackUnits: 42, RackStatus: constants.RackAvailable},
	}
	before := append([]model.Rack(nil), racks...)
	planner.NewEngine(100).Evaluate(zones, racks, loads)
	if !reflect.DeepEqual(racks, before) {
		t.Fatalf("Evaluate mutated input racks order:\nbefore=%+v\nafter=%+v", before, racks)
	}
}

func TestEvaluateDoesNotMutateInputLoads(t *testing.T) {
	zones, _ := sliceFixture()
	loads := []model.EquipmentLoad{
		{ID: 1, Name: "L1", PowerKW: 3, HeatKW: 2, AirflowCFM: 600, RackUnits: 2, RedundancyGroup: "G1", LoadStatus: "ready"},
		{ID: 2, Name: "L2", PowerKW: 10, HeatKW: 9, AirflowCFM: 2000, RackUnits: 8, RedundancyGroup: "G2", LoadStatus: "ready"},
	}
	before := append([]model.EquipmentLoad(nil), loads...)
	planner.NewEngine(100).Evaluate(zones, sliceRacks(), loads)
	if !reflect.DeepEqual(loads, before) {
		t.Fatalf("Evaluate mutated input loads order:\nbefore=%+v\nafter=%+v", before, loads)
	}
}

func TestEvaluateDoesNotMutateInputZones(t *testing.T) {
	zones := []model.ThermalZone{
		{ID: 2, ZoneCode: "TZ-B", CoolingCapacityKW: 40, SupplyTempC: 18, MaxReturnTempC: 31, AdjacencyJSON: `{}`, ZoneStatus: "active"},
		{ID: 1, ZoneCode: "TZ-A", CoolingCapacityKW: 40, SupplyTempC: 18, MaxReturnTempC: 31, AdjacencyJSON: `{}`, ZoneStatus: "active"},
	}
	before := append([]model.ThermalZone(nil), zones...)
	loads := []model.EquipmentLoad{
		{ID: 1, Name: "L1", PowerKW: 5, HeatKW: 4, AirflowCFM: 1200, RackUnits: 4, RedundancyGroup: "G1", LoadStatus: "ready"},
		{ID: 2, Name: "L2", PowerKW: 5, HeatKW: 4, AirflowCFM: 1200, RackUnits: 4, RedundancyGroup: "G2", LoadStatus: "ready"},
	}
	planner.NewEngine(100).Evaluate(zones, sliceRacks(), loads)
	if !reflect.DeepEqual(zones, before) {
		t.Fatalf("Evaluate mutated input zones order:\nbefore=%+v\nafter=%+v", before, zones)
	}
}

func TestConsecutiveEvaluationsNoAliasing(t *testing.T) {
	zones, _ := sliceFixture()
	loadsA := []model.EquipmentLoad{
		{ID: 1, Name: "A1", PowerKW: 5, HeatKW: 4, AirflowCFM: 1200, RackUnits: 4, RedundancyGroup: "GA", LoadStatus: "ready"},
		{ID: 2, Name: "A2", PowerKW: 6, HeatKW: 5, AirflowCFM: 1300, RackUnits: 4, RedundancyGroup: "GB", LoadStatus: "ready"},
	}
	loadsB := []model.EquipmentLoad{
		{ID: 3, Name: "B1", PowerKW: 8, HeatKW: 7, AirflowCFM: 1500, RackUnits: 4, RedundancyGroup: "GC", LoadStatus: "ready"},
		{ID: 4, Name: "B2", PowerKW: 9, HeatKW: 8, AirflowCFM: 1600, RackUnits: 4, RedundancyGroup: "GD", LoadStatus: "ready"},
	}
	first := planner.NewEngine(100).Evaluate(zones, sliceRacks(), loadsA)
	snapshot := append([]dto.RackAssignment(nil), first.Assignments...)
	planner.NewEngine(100).Evaluate(zones, sliceRacks(), loadsB)
	if !reflect.DeepEqual(first.Assignments, snapshot) {
		t.Fatalf("first result assignments were overwritten by second evaluation:\nbefore=%+v\nafter=%+v", snapshot, first.Assignments)
	}
}

func sliceFixture() ([]model.ThermalZone, []model.Rack) {
	zones := []model.ThermalZone{
		{ID: 1, ZoneCode: "TZ-A", CoolingCapacityKW: 40, SupplyTempC: 18, MaxReturnTempC: 31, AdjacencyJSON: `{}`, ZoneStatus: "active"},
		{ID: 2, ZoneCode: "TZ-B", CoolingCapacityKW: 40, SupplyTempC: 18, MaxReturnTempC: 31, AdjacencyJSON: `{}`, ZoneStatus: "active"},
	}
	return zones, sliceRacks()
}

func sliceRacks() []model.Rack {
	return []model.Rack{
		{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 24, AirflowLimitCFM: 7000, RackUnits: 42, RackStatus: constants.RackAvailable},
		{ID: 2, ZoneID: 2, RackCode: "B-01", PowerLimitKW: 24, AirflowLimitCFM: 7000, RackUnits: 42, RackStatus: constants.RackAvailable},
	}
}
