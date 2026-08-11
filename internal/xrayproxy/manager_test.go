package xrayproxy

import "testing"

func TestAvailableRoutePortDoesNotReuseReservedRoutePort(t *testing.T) {
	reserved, err := availablePort()
	if err != nil {
		t.Fatalf("find reserved port: %v", err)
	}
	routes := map[string]routeState{"existing": {port: reserved}}
	selected, err := availableRoutePort(routes)
	if err != nil {
		t.Fatalf("find unique route port: %v", err)
	}
	if selected == reserved {
		t.Fatalf("selected reserved route port %d", selected)
	}
}
