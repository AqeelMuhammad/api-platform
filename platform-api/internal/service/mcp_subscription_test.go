/*
 *  Copyright (c) 2026, WSO2 LLC. (http://www.wso2.org) All Rights Reserved.
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *  http://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 *
 */

package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/wso2/api-platform/common/eventhub"
	"github.com/wso2/api-platform/platform-api/config"
	"github.com/wso2/api-platform/platform-api/internal/apperror"
	"github.com/wso2/api-platform/platform-api/internal/dto"
	"github.com/wso2/api-platform/platform-api/internal/model"
	"github.com/wso2/api-platform/platform-api/internal/repository"
)

// stubSubscriptionPlanRepo resolves plan handles from a fixed map; unknown handles return
// sql.ErrNoRows, as the real repository does.
type stubSubscriptionPlanRepo struct {
	repository.SubscriptionPlanRepository
	plans map[string]*model.SubscriptionPlan
}

func (r *stubSubscriptionPlanRepo) GetByHandleAndOrg(handle, _ string) (*model.SubscriptionPlan, error) {
	if p, ok := r.plans[handle]; ok {
		return p, nil
	}
	return nil, sql.ErrNoRows
}

func newStubPlanRepo() *stubSubscriptionPlanRepo {
	return &stubSubscriptionPlanRepo{plans: map[string]*model.SubscriptionPlan{
		"Gold":   {UUID: "plan-gold", Handle: "Gold", Status: model.SubscriptionPlanStatusActive},
		"Silver": {UUID: "plan-silver", Handle: "Silver", Status: model.SubscriptionPlanStatusActive},
		"Legacy": {UUID: "plan-legacy", Handle: "Legacy", Status: model.SubscriptionPlanStatusInactive},
	}}
}

// capturingMCPProxyRepository records the proxy passed to Create/Update and serves it back.
type capturingMCPProxyRepository struct {
	repository.MCPProxyRepository
	stored *model.MCPProxy
}

func (r *capturingMCPProxyRepository) Exists(string, string) (bool, error) { return false, nil }

func (r *capturingMCPProxyRepository) Create(p *model.MCPProxy) error {
	cp := *p
	r.stored = &cp
	return nil
}

func (r *capturingMCPProxyRepository) Update(p *model.MCPProxy) error {
	cp := *p
	r.stored = &cp
	return nil
}

func (r *capturingMCPProxyRepository) GetByHandle(string, string) (*model.MCPProxy, error) {
	if r.stored == nil {
		return nil, nil
	}
	cp := *r.stored
	return &cp, nil
}

func newMCPServiceWithPlans(repo repository.MCPProxyRepository) *MCPProxyService {
	svc := NewMCPProxyService(repo, nil, nil, nil, nil, slog.Default(), &noopAuditRepo{}, &config.Server{}, newTestIdentityService())
	svc.WithSubscriptionPlanRepository(newStubPlanRepo())
	return svc
}

func TestMCPProxyServiceCreateStoresAndReturnsSubscriptionPlans(t *testing.T) {
	repo := &capturingMCPProxyRepository{}
	svc := newMCPServiceWithPlans(repo)

	req := validMCPProxyRequest()
	req.SubscriptionPlans = &[]string{" Gold ", "Silver"}

	got, err := svc.Create("org-1", "alice", req)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	want := []string{"Gold", "Silver"}
	if !reflect.DeepEqual(repo.stored.Configuration.SubscriptionPlans, want) {
		t.Errorf("stored plans = %v, want %v (trimmed)", repo.stored.Configuration.SubscriptionPlans, want)
	}
	if got.SubscriptionPlans == nil || !reflect.DeepEqual(*got.SubscriptionPlans, want) {
		t.Errorf("returned plans = %v, want %v", got.SubscriptionPlans, want)
	}
}

func TestMCPProxyServiceCreateWithoutSubscriptionPlans(t *testing.T) {
	repo := &capturingMCPProxyRepository{}
	svc := newMCPServiceWithPlans(repo)

	got, err := svc.Create("org-1", "alice", validMCPProxyRequest())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if repo.stored.Configuration.SubscriptionPlans != nil {
		t.Errorf("stored plans = %v, want none", repo.stored.Configuration.SubscriptionPlans)
	}
	if got.SubscriptionPlans != nil {
		t.Errorf("returned plans = %v, want nil", *got.SubscriptionPlans)
	}
}

func TestMCPProxyServiceCreateRejectsUnknownOrInactivePlan(t *testing.T) {
	for _, plan := range []string{"Platinum", "Legacy"} {
		t.Run(plan, func(t *testing.T) {
			repo := &capturingMCPProxyRepository{}
			svc := newMCPServiceWithPlans(repo)

			req := validMCPProxyRequest()
			req.SubscriptionPlans = &[]string{"Gold", plan}

			_, err := svc.Create("org-1", "alice", req)
			if !apperror.ValidationFailed.Is(err) {
				t.Fatalf("expected ValidationFailed for plan %q, got: %v", plan, err)
			}
			if repo.stored != nil {
				t.Errorf("proxy was stored despite an invalid plan")
			}
		})
	}
}

func TestMCPProxyServiceUpdateReplacesSubscriptionPlans(t *testing.T) {
	repo := &capturingMCPProxyRepository{stored: &model.MCPProxy{
		Handle:        "mcp-proxy-1",
		Configuration: model.MCPProxyConfiguration{SubscriptionPlans: []string{"Gold"}},
	}}
	svc := newMCPServiceWithPlans(repo)

	req := validMCPProxyRequest()
	req.SubscriptionPlans = &[]string{"Silver"}

	got, err := svc.Update("org-1", "mcp-proxy-1", "alice", req)
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if !reflect.DeepEqual(repo.stored.Configuration.SubscriptionPlans, []string{"Silver"}) {
		t.Errorf("stored plans = %v, want [Silver]", repo.stored.Configuration.SubscriptionPlans)
	}
	if got.SubscriptionPlans == nil || !reflect.DeepEqual(*got.SubscriptionPlans, []string{"Silver"}) {
		t.Errorf("returned plans = %v, want [Silver]", got.SubscriptionPlans)
	}
}

func TestMCPProxyServiceUpdateRejectsUnknownPlan(t *testing.T) {
	repo := &capturingMCPProxyRepository{stored: &model.MCPProxy{Handle: "mcp-proxy-1"}}
	svc := newMCPServiceWithPlans(repo)

	req := validMCPProxyRequest()
	req.SubscriptionPlans = &[]string{"Platinum"}

	if _, err := svc.Update("org-1", "mcp-proxy-1", "alice", req); !apperror.ValidationFailed.Is(err) {
		t.Fatalf("expected ValidationFailed, got: %v", err)
	}
}

func TestMapMCPProxySpecToConfigCarriesSubscriptionPlans(t *testing.T) {
	cfg := mapMCPProxySpecToConfig(model.MCPProxyDeploymentSpec{
		DisplayName:       "Docs",
		Version:           "v1.0",
		SubscriptionPlans: []string{"Gold"},
	})
	if !reflect.DeepEqual(cfg.SubscriptionPlans, []string{"Gold"}) {
		t.Errorf("SubscriptionPlans = %v, want [Gold]", cfg.SubscriptionPlans)
	}
}

// ---- deploy-time subscription backfill -------------------------------------------------

type stubBackfillSubscriptionRepo struct {
	repository.SubscriptionRepository
	subs      []*model.Subscription
	err       error
	gotOrg    string
	gotAPIIDs []string
}

func (r *stubBackfillSubscriptionRepo) ListByFilters(orgUUID string, apiUUID *string, _, _, _ *string, _, offset int) ([]*model.Subscription, error) {
	r.gotOrg = orgUUID
	if apiUUID != nil {
		r.gotAPIIDs = append(r.gotAPIIDs, *apiUUID)
	}
	if r.err != nil || offset > 0 {
		return nil, r.err
	}
	return r.subs, nil
}

func decodeSubscriptionCreated(t *testing.T, e eventhub.Event) model.SubscriptionCreatedEvent {
	t.Helper()
	var envelope dto.GatewayEventDTO
	if err := json.Unmarshal([]byte(e.EventData), &envelope); err != nil {
		t.Fatalf("failed to decode event envelope: %v", err)
	}
	if envelope.Type != EventTypeSubscriptionCreated {
		t.Fatalf("unexpected event type %q, want %q", envelope.Type, EventTypeSubscriptionCreated)
	}
	payloadBytes, err := json.Marshal(envelope.Payload)
	if err != nil {
		t.Fatalf("failed to re-marshal payload: %v", err)
	}
	var sub model.SubscriptionCreatedEvent
	if err := json.Unmarshal(payloadBytes, &sub); err != nil {
		t.Fatalf("failed to decode subscription payload: %v", err)
	}
	return sub
}

func TestBackfillSubscriptionsToGateway(t *testing.T) {
	plan := "plan-gold"
	app := "app-1"
	repo := &stubBackfillSubscriptionRepo{subs: []*model.Subscription{
		{UUID: "sub-active", ArtifactUUID: "mcp-1", SubscriptionToken: "tok-1", SubscriptionPlanID: &plan, ApplicationID: &app, Status: model.SubscriptionStatusActive},
		{UUID: "sub-suspended", ArtifactUUID: "mcp-1", SubscriptionToken: "tok-2", Status: model.SubscriptionStatusInactive},
		nil,
	}}
	hub := &capturingEventHub{}
	events := NewGatewayEventsService(hub, newTestIdentityService(), newTestLogger())

	BackfillSubscriptionsToGateway(repo, events, newTestLogger(), "mcp-1", "org-1", "gw-1")

	if repo.gotOrg != "org-1" || len(repo.gotAPIIDs) == 0 || repo.gotAPIIDs[0] != "mcp-1" {
		t.Fatalf("listed subscriptions for org %q, artifacts %v; want org-1 / mcp-1", repo.gotOrg, repo.gotAPIIDs)
	}
	if len(hub.published) != 2 {
		t.Fatalf("expected 2 subscription.created events, got %d", len(hub.published))
	}
	got := map[string]model.SubscriptionCreatedEvent{}
	for _, e := range hub.published {
		if e.GatewayID != "gw-1" {
			t.Errorf("event sent to gateway %q, want gw-1", e.GatewayID)
		}
		s := decodeSubscriptionCreated(t, e)
		got[s.SubscriptionId] = s
	}
	active := got["sub-active"]
	if active.ApiId != "mcp-1" || active.SubscriptionToken != "tok-1" || active.SubscriptionPlanId != "plan-gold" ||
		active.ApplicationId != "app-1" || active.Status != string(model.SubscriptionStatusActive) {
		t.Errorf("unexpected active subscription event: %+v", active)
	}
	if got["sub-suspended"].Status != string(model.SubscriptionStatusInactive) {
		t.Errorf("suspended subscription status = %q, want INACTIVE", got["sub-suspended"].Status)
	}
}

func TestBackfillSubscriptionsToGatewayIsBestEffort(t *testing.T) {
	hub := &capturingEventHub{}
	events := NewGatewayEventsService(hub, newTestIdentityService(), newTestLogger())

	// A repository error and a missing repository must neither panic nor broadcast.
	BackfillSubscriptionsToGateway(&stubBackfillSubscriptionRepo{err: errors.New("db down")}, events, newTestLogger(), "mcp-1", "org-1", "gw-1")
	BackfillSubscriptionsToGateway(nil, events, newTestLogger(), "mcp-1", "org-1", "gw-1")
	BackfillSubscriptionsToGateway(&stubBackfillSubscriptionRepo{}, nil, newTestLogger(), "mcp-1", "org-1", "gw-1")

	if len(hub.published) != 0 {
		t.Fatalf("expected no events, got %d", len(hub.published))
	}
}

// ---- internal gateway API: MCP proxy subscriptions -------------------------------------

type stubInternalMCPProxyRepo struct {
	repository.MCPProxyRepository
	proxy *model.MCPProxy
}

func (r *stubInternalMCPProxyRepo) GetByUUID(uuid, orgUUID string) (*model.MCPProxy, error) {
	if r.proxy == nil || r.proxy.UUID != uuid || r.proxy.OrganizationUUID != orgUUID {
		return nil, nil
	}
	return r.proxy, nil
}

type stubInternalDeploymentRepo struct {
	repository.DeploymentRepository
	deploymentID string
	status       model.DeploymentStatus
}

func (r *stubInternalDeploymentRepo) GetStatus(string, string, string) (string, model.DeploymentStatus, *time.Time, error) {
	return r.deploymentID, r.status, nil, nil
}

func newInternalServiceForMCP(deploymentID string, status model.DeploymentStatus, subs []*model.Subscription) *GatewayInternalAPIService {
	return &GatewayInternalAPIService{
		mcpProxyRepo:     &stubInternalMCPProxyRepo{proxy: &model.MCPProxy{UUID: "mcp-1", OrganizationUUID: "org-1"}},
		deploymentRepo:   &stubInternalDeploymentRepo{deploymentID: deploymentID, status: status},
		subscriptionRepo: &stubBackfillSubscriptionRepo{subs: subs},
		slogger:          newTestLogger(),
	}
}

func TestListSubscriptionsForMCPProxy(t *testing.T) {
	plan := "plan-gold"
	updated := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	svc := newInternalServiceForMCP("dep-1", model.DeploymentStatusDeployed, []*model.Subscription{
		{UUID: "sub-1", ArtifactUUID: "mcp-1", SubscriptionToken: "tok-1", SubscriptionPlanID: &plan, Status: model.SubscriptionStatusActive, UpdatedAt: updated},
	})

	items, err := svc.ListSubscriptionsForMCPProxy("mcp-1", "org-1")
	if err != nil {
		t.Fatalf("ListSubscriptionsForMCPProxy returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(items))
	}
	it := items[0]
	if it.ID != "sub-1" || it.MCPProxyID != "mcp-1" || it.SubscriptionToken != "tok-1" ||
		it.SubscriptionPlanID == nil || *it.SubscriptionPlanID != "plan-gold" || it.Status != "ACTIVE" || it.Etag == "" {
		t.Errorf("unexpected item: %+v", it)
	}

	raw, _ := json.Marshal(it)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["mcpProxyId"]; !ok {
		t.Errorf("response JSON has no mcpProxyId: %s", raw)
	}
	if _, ok := fields["apiId"]; ok {
		t.Errorf("response JSON must not carry apiId: %s", raw)
	}
}

func TestListSubscriptionsForMCPProxyUnknownProxy(t *testing.T) {
	svc := newInternalServiceForMCP("dep-1", model.DeploymentStatusDeployed, nil)

	if _, err := svc.ListSubscriptionsForMCPProxy("mcp-404", "org-1"); !apperror.MCPProxyNotFound.Is(err) {
		t.Fatalf("expected MCPProxyNotFound, got: %v", err)
	}
	if _, err := svc.ListSubscriptionsForMCPProxy("mcp-1", "org-other"); !apperror.MCPProxyNotFound.Is(err) {
		t.Fatalf("expected MCPProxyNotFound for another organization, got: %v", err)
	}
}

func TestIsMCPProxyDeployedOnGateway(t *testing.T) {
	cases := []struct {
		name         string
		deploymentID string
		status       model.DeploymentStatus
		proxyID      string
		wantErr      func(error) bool
	}{
		{"deployed", "dep-1", model.DeploymentStatusDeployed, "mcp-1", func(err error) bool { return err == nil }},
		{"deploying", "dep-1", model.DeploymentStatusDeploying, "mcp-1", func(err error) bool { return err == nil }},
		{"no deployment", "", "", "mcp-1", apperror.DeploymentNotActive.Is},
		{"failed", "dep-1", model.DeploymentStatusFailed, "mcp-1", apperror.DeploymentNotActive.Is},
		{"unknown proxy", "dep-1", model.DeploymentStatusDeployed, "mcp-404", apperror.MCPProxyNotFound.Is},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newInternalServiceForMCP(tc.deploymentID, tc.status, nil)
			if err := svc.IsMCPProxyDeployedOnGateway(tc.proxyID, "gw-1", "org-1"); !tc.wantErr(err) {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}
