package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// fakeAPIKeyServer implements the v3beta1 service user API key endpoints for
// one service user.
type fakeAPIKeyServer struct {
	mu      sync.Mutex
	next    int
	keys    map[string]*serviceUserAPIKey
	revoked []string
}

func (f *fakeAPIKeyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	base := enterpriseServiceUserAPIKeysPath("service-user-1")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		var body serviceUserAPIKeyCreateRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.next++
		id := fmt.Sprintf("key-%d", f.next)
		f.keys[id] = &serviceUserAPIKey{APIKeyID: id, APIKeyName: body.Name, CreatedAt: 1700000000, ExpiresAt: body.ExpiresAt, IsActive: true}
		_ = json.NewEncoder(w).Encode(serviceUserAPIKeyCreateResponse{APIKeyID: id, APIKeyName: body.Name, Token: "cog_" + id})
	case r.Method == http.MethodGet && r.URL.Path == base:
		page := serviceUserAPIKeyPage{Items: []serviceUserAPIKey{}}
		for _, k := range f.keys {
			if k.IsActive {
				page.Items = append(page.Items, *k)
			}
		}
		_ = json.NewEncoder(w).Encode(page)
	case r.Method == http.MethodDelete:
		id := r.URL.Path[len(base)+1:]
		k, ok := f.keys[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !k.IsActive {
			w.WriteHeader(http.StatusConflict)
			return
		}
		k.IsActive = false
		f.revoked = append(f.revoked, id)
		_ = json.NewEncoder(w).Encode(map[string]string{"api_key_id": id})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func apiKeyConfig(name string) string {
	return fmt.Sprintf(`%s
resource "devin_enterprise_service_user_api_key" "test" {
  service_user_id = "service-user-1"
  name            = %q
  expires_at      = 4102444800

  lifecycle {
    create_before_destroy = true
  }
}
`, providerConfig, name)
}

func TestEnterpriseServiceUserAPIKeyLifecycle(t *testing.T) {
	fake := &fakeAPIKeyServer{keys: map[string]*serviceUserAPIKey{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	t.Setenv("DEVIN_API_URL", server.URL)
	t.Setenv("DEVIN_TOKEN", "cog_test")

	const addr = "devin_enterprise_service_user_api_key.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: apiKeyConfig("first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "api_key_id", "key-1"),
					resource.TestCheckResourceAttr(addr, "token", "cog_key-1"),
					resource.TestCheckResourceAttr(addr, "created_at", "1700000000"),
				),
			},
			{
				// Renaming creates the replacement before revoking key-1.
				Config: apiKeyConfig("second"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "api_key_id", "key-2"),
					resource.TestCheckResourceAttr(addr, "token", "cog_key-2"),
				),
			},
			{
				// A key revoked outside Terraform is recreated.
				PreConfig: func() {
					fake.mu.Lock()
					fake.keys["key-2"].IsActive = false
					fake.mu.Unlock()
				},
				Config: apiKeyConfig("second"),
				Check:  resource.TestCheckResourceAttr(addr, "api_key_id", "key-3"),
			},
		},
	})

	if len(fake.revoked) != 2 || fake.revoked[0] != "key-1" || fake.revoked[1] != "key-3" {
		t.Errorf("expected key-1 then key-3 revoked, got %v", fake.revoked)
	}
}
