package provider

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &enterpriseServiceUserAPIKeyResource{}

type enterpriseServiceUserAPIKeyResource struct {
	client *Client
}

type enterpriseServiceUserAPIKeyModel struct {
	ServiceUserID types.String `tfsdk:"service_user_id"`
	Name          types.String `tfsdk:"name"`
	ExpiresAt     types.Int64  `tfsdk:"expires_at"`
	APIKeyID      types.String `tfsdk:"api_key_id"`
	Token         types.String `tfsdk:"token"`
	CreatedAt     types.Int64  `tfsdk:"created_at"`
}

// The service user API key endpoints are v3beta1 and not in the exported v3
// spec, so their models are declared here instead of in internal/api.
type serviceUserAPIKeyCreateRequest struct {
	Name      string `json:"name"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

type serviceUserAPIKeyCreateResponse struct {
	APIKeyID   string `json:"api_key_id"`
	APIKeyName string `json:"api_key_name"`
	Token      string `json:"token"`
}

type serviceUserAPIKey struct {
	APIKeyID   string `json:"api_key_id"`
	APIKeyName string `json:"api_key_name"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  *int64 `json:"expires_at"`
	IsActive   bool   `json:"is_active"`
	RevokedAt  *int64 `json:"revoked_at"`
}

type serviceUserAPIKeyPage struct {
	Items       []serviceUserAPIKey `json:"items"`
	EndCursor   *string             `json:"end_cursor"`
	HasNextPage bool                `json:"has_next_page"`
}

func NewEnterpriseServiceUserAPIKeyResource() resource.Resource {
	return &enterpriseServiceUserAPIKeyResource{}
}

func (r *enterpriseServiceUserAPIKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_enterprise_service_user_api_key"
}

func (r *enterpriseServiceUserAPIKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Creates an API key for an existing enterprise service user. The token is only returned at " +
			"creation and is stored in state. Every attribute forces a replacement, which creates the new key " +
			"before revoking the old one when combined with create_before_destroy. A key that is revoked or " +
			"expires outside Terraform is removed from state and recreated on the next apply. Requires the " +
			"ManageAccountServiceUsers permission at the enterprise level. Cannot be imported.",
		Attributes: map[string]schema.Attribute{
			"service_user_id": schema.StringAttribute{
				Description: "ID of an existing service user (prefix: service-user-).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Description: "Name of the API key.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"expires_at": schema.Int64Attribute{
				Description: "Expiration as a UNIX timestamp in seconds. Must be in the future. Omit for a key that does not expire.",
				Optional:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"api_key_id": schema.StringAttribute{
				Description: "API key ID (assigned by Devin).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"token": schema.StringAttribute{
				Description: "The raw API key (cog_ prefix). Only returned by the API at creation.",
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.Int64Attribute{
				Description: "Creation time as a UNIX timestamp in seconds.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *enterpriseServiceUserAPIKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.client = req.ProviderData.(*Client)
}

func (r *enterpriseServiceUserAPIKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan enterpriseServiceUserAPIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := serviceUserAPIKeyCreateRequest{Name: plan.Name.ValueString()}
	if !plan.ExpiresAt.IsNull() && !plan.ExpiresAt.IsUnknown() {
		expiresAt := plan.ExpiresAt.ValueInt64()
		body.ExpiresAt = &expiresAt
	}

	serviceUserID := plan.ServiceUserID.ValueString()
	var created serviceUserAPIKeyCreateResponse
	if err := r.client.Post(ctx, enterpriseServiceUserAPIKeysPath(serviceUserID), body, &created); err != nil {
		resp.Diagnostics.AddError("Failed to create service user API key", err.Error())
		return
	}

	plan.APIKeyID = types.StringValue(created.APIKeyID)
	plan.Token = types.StringValue(created.Token)
	plan.CreatedAt = types.Int64Null()

	key, err := findServiceUserAPIKey(ctx, r.client, serviceUserID, created.APIKeyID)
	if err != nil {
		resp.Diagnostics.AddWarning("Failed to read created service user API key", err.Error())
	} else if key != nil {
		plan.CreatedAt = types.Int64Value(key.CreatedAt)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *enterpriseServiceUserAPIKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state enterpriseServiceUserAPIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := findServiceUserAPIKey(ctx, r.client, state.ServiceUserID.ValueString(), state.APIKeyID.ValueString())
	if IsNotFound(err) || (err == nil && key == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read service user API key", err.Error())
		return
	}

	state.Name = types.StringValue(key.APIKeyName)
	state.CreatedAt = types.Int64Value(key.CreatedAt)
	if key.ExpiresAt != nil {
		state.ExpiresAt = types.Int64Value(*key.ExpiresAt)
	} else {
		state.ExpiresAt = types.Int64Null()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *enterpriseServiceUserAPIKeyResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Service user API keys cannot be updated in place",
		"All devin_enterprise_service_user_api_key attributes require replacement. This is a bug in the provider's plan modifiers.",
	)
}

func (r *enterpriseServiceUserAPIKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state enterpriseServiceUserAPIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Delete(ctx, enterpriseServiceUserAPIKeyPath(state.ServiceUserID.ValueString(), state.APIKeyID.ValueString()), nil)
	// 404: already gone; 409: already revoked.
	var apiErr *APIError
	if err != nil && !(errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusConflict)) {
		resp.Diagnostics.AddError("Failed to revoke service user API key", err.Error())
	}
}

// findServiceUserAPIKey returns the key if it is still active, or nil if it
// was revoked, expired, or deleted. There is no GET-by-id endpoint.
func findServiceUserAPIKey(ctx context.Context, client *Client, serviceUserID, apiKeyID string) (*serviceUserAPIKey, error) {
	cursor := ""
	for {
		query := url.Values{}
		query.Set("status", "active")
		if cursor != "" {
			query.Set("after", cursor)
		}

		var page serviceUserAPIKeyPage
		if err := client.Get(ctx, enterpriseServiceUserAPIKeysPath(serviceUserID)+"?"+query.Encode(), &page); err != nil {
			return nil, err
		}
		for i := range page.Items {
			if page.Items[i].APIKeyID == apiKeyID && page.Items[i].IsActive {
				return &page.Items[i], nil
			}
		}
		if !page.HasNextPage || page.EndCursor == nil {
			return nil, nil
		}
		cursor = *page.EndCursor
	}
}
