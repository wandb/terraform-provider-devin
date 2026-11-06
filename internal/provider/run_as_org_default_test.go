package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// fakeOrgDefaultAPI mimics an organization with a default automation service
// user: run_as 'organization' is stored and read back as that service user.
type fakeOrgDefaultAPI struct {
	mu      sync.Mutex
	stored  map[string]any
	patches int
}

func (f *fakeOrgDefaultAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/automations"):
		f.stored = map[string]any{"automation_id": "auto-1", "enabled": true, "triggers": []any{}}
		f.apply(r)
	case r.Method == http.MethodPatch:
		f.patches++
		f.apply(r)
	case r.Method == http.MethodDelete:
		f.stored = nil
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if f.stored == nil {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(f.stored)
}

func (f *fakeOrgDefaultAPI) apply(r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, key := range []string{"name", "enabled", "metadata"} {
		if v, ok := body[key]; ok {
			f.stored[key] = v
		}
	}
	if runAs, ok := body["run_as"].(map[string]any); ok && runAs["type"] == "organization" {
		f.stored["run_as"] = map[string]any{"type": "service_user", "service_user_id": "service-user-default"}
	} else if ok {
		f.stored["run_as"] = runAs
	}
}

func TestAutomationOrganizationRunAsWithDefaultServiceUserHasNoDrift(t *testing.T) {
	api := &fakeOrgDefaultAPI{}
	server := httptest.NewServer(api)
	defer server.Close()
	t.Setenv("DEVIN_API_URL", server.URL)
	t.Setenv("DEVIN_TOKEN", "cog_test")

	config := `
resource "devin_automation" "test" {
  org_id   = "org-1"
  name     = "org default"
  run_as   = "organization"
  triggers = jsonencode([{ event_type = "schedule:recurring" }])
  actions  = jsonencode([{ type = "start_session", prompt = "hi" }])
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("devin_automation.test", "run_as", "organization"),
					resource.TestCheckNoResourceAttr("devin_automation.test", "run_as_service_user_id"),
				),
			},
			{Config: config, PlanOnly: true},
		},
	})
	if api.patches != 0 {
		t.Errorf("provider sent %d PATCH requests, want 0", api.patches)
	}
}
