package planner

import (
	"testing"

	"datacenter-thermal-capacity-planner/backend/internal/model"
)

func TestAdjacentHeatPenaltyUsesNeighborHeat(t *testing.T) {
	zones := []model.ThermalZone{
		{ID: 1, ZoneCode: "TZ-A", CoolingCapacityKW: 40, SupplyTempC: 18, MaxReturnTempC: 31, AdjacencyJSON: `{}`, ZoneStatus: "active"},
		{ID: 2, ZoneCode: "TZ-B", CoolingCapacityKW: 40, SupplyTempC: 18, MaxReturnTempC: 31, AdjacencyJSON: `{"TZ-A":0.2}`, ZoneStatus: "active"},
	}
	heat := map[uint]float64{1: 20, 2: 0}
	got := adjacentHeatPenalty(zones[1], zones, heat)
	want := (20.0 / 40.0) * 0.2
	if got != want {
		t.Fatalf("adjacentHeatPenalty = %.4f, want %.4f (neighbor heat 20 should matter)", got, want)
	}
}
