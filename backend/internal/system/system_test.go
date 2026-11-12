package system

import "testing"

func TestSystem_ServicesProbeAndStateMapping(t *testing.T) {
	services := buildServicesFromStates([]string{"active", "inactive", "failed", "activating", "unknown"})
	if len(services) != len(coreServiceCatalog) {
		t.Fatalf("expected %d services, got %d", len(coreServiceCatalog), len(services))
	}
	if !services[0].Active || services[0].BadgeClass != "green" {
		t.Fatalf("expected active service green, got %+v", services[0])
	}
	if services[1].Active || services[1].BadgeClass != "slate" || services[1].StateLabel != "Inactive (stopped)" {
		t.Fatalf("expected inactive service slate, got %+v", services[1])
	}
	if services[2].BadgeClass != "red" || services[2].StateLabel != "Failed" {
		t.Fatalf("expected failed service red, got %+v", services[2])
	}
	if services[3].BadgeClass != "amber" || services[3].StateLabel != "Activating" {
		t.Fatalf("expected activating service amber, got %+v", services[3])
	}
	if services[4].BadgeClass != "slate" {
		t.Fatalf("expected unknown service slate, got %+v", services[4])
	}

	status := NewCollector(true).GetStatus()
	if len(status.Services) != len(coreServiceCatalog) {
		t.Fatalf("expected %d services in mock SystemStatus, got %d", len(coreServiceCatalog), len(status.Services))
	}
}

