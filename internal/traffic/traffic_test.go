package traffic_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/helmedeiros/traffic-gen/internal/traffic"
)

// TestRequestJSONShapeMatchesMarkupSvc pins the contract documented
// in ADR-0001: traffic-gen's Request must JSON-encode to the exact
// shape markup-svc's /decide expects. Field names use snake_case
// (omitempty), Amount is a number, all others are strings.
func TestRequestJSONShapeMatchesMarkupSvc(t *testing.T) {
	req := traffic.Request{
		ProductID:    "p-42",
		Category:     "electronics",
		CustomerTier: "enterprise",
		Channel:      "web",
		Country:      "BR",
		Inventory:    "high",
		TimeWindow:   "peak",
		Amount:       100.0,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Round-trip into a map keyed by the wire field names so we can
	// assert each expected key is present and not accidentally
	// renamed.
	var asMap map[string]interface{}
	if err := json.Unmarshal(body, &asMap); err != nil {
		t.Fatalf("Unmarshal into map: %v", err)
	}
	for _, key := range []string{
		"product_id", "category", "customer_tier", "channel",
		"country", "inventory", "time_window", "amount",
	} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("expected JSON key %q in encoded body %s", key, string(body))
		}
	}
	// Amount is numeric, not a string.
	if amt, ok := asMap["amount"].(float64); !ok || amt != 100.0 {
		t.Errorf("amount = %v (type %T), want float64 100.0", asMap["amount"], asMap["amount"])
	}
}

// TestRequestOmitsEmptyFields confirms that an unset string field
// disappears from the encoded body (so the markup-svc /decide
// handler does not see e.g. country="" and route to a no-match by
// accident). The omitempty tag is what makes this work.
func TestRequestOmitsEmptyFields(t *testing.T) {
	req := traffic.Request{CustomerTier: "enterprise"} // everything else zero
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, missing := range []string{
		"product_id", "category", "channel", "country",
		"inventory", "time_window", "amount",
	} {
		if strings.Contains(string(body), missing) {
			t.Errorf("body %s leaked zero-valued field %q (omitempty broken)", string(body), missing)
		}
	}
	if !strings.Contains(string(body), `"customer_tier":"enterprise"`) {
		t.Errorf("body %s missing the populated customer_tier", string(body))
	}
}
