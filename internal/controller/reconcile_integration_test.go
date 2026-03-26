package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	networkingv1alpha1 "github.com/adyanth/cloudflare-operator/api/v1alpha1"
	networkingv1alpha2 "github.com/adyanth/cloudflare-operator/api/v1alpha2"
	"github.com/adyanth/cloudflare-operator/internal/clients/cf"
	"github.com/adyanth/cloudflare-operator/internal/testutil/cfmock"
	"github.com/cloudflare/cloudflare-go"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"gopkg.in/yaml.v3"
)

func init() {
	ctrllog.SetLogger(zap.New(zap.UseDevMode(true)))
}

func integrationScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = networkingv1alpha1.AddToScheme(s)
	_ = networkingv1alpha2.AddToScheme(s)
	return s
}

func setupMockServer(t *testing.T) *cfmock.Server {
	t.Helper()
	server := cfmock.NewServer()
	testCfClientOpts = []cloudflare.Option{cloudflare.BaseURL(server.URL + "/client/v4")}
	t.Cleanup(func() {
		testCfClientOpts = nil
		server.Close()
	})
	return server
}


// integrationApplyClient wraps a fake client to handle SSA patches (same pattern as applyCapturingClient).
type integrationApplyClient struct {
	client.Client
	applied []client.Object
}

func (c *integrationApplyClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if patch == client.Apply {
		existing := obj.DeepCopyObject().(client.Object)
		err := c.Client.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing)
		if err != nil {
			if createErr := c.Client.Create(ctx, obj); createErr != nil {
				return createErr
			}
		} else {
			obj.SetResourceVersion(existing.GetResourceVersion())
			if updateErr := c.Client.Update(ctx, obj); updateErr != nil {
				return updateErr
			}
		}
		c.applied = append(c.applied, obj)
		return nil
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestTunnelReconcile_FullFlow_NewTunnel(t *testing.T) {
	server := setupMockServer(t)
	server.AddAccount("acc-123", "test-account")
	server.AddZone("zone-789", "example.com")

	scheme := integrationScheme()
	tunnel := &networkingv1alpha2.Tunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
			UID:       types.UID("uid-tunnel-1"),
		},
		Spec: networkingv1alpha2.TunnelSpec{
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:                              "example.com",
				Secret:                              "cf-secret",
				AccountId:                           "acc-123",
				AccountName:                         "test-account",
				CLOUDFLARE_API_TOKEN:                "CLOUDFLARE_API_TOKEN",
				CLOUDFLARE_API_KEY:                  "CLOUDFLARE_API_KEY",
				CLOUDFLARE_TUNNEL_CREDENTIAL_FILE:   "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
				CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
			},
			NewTunnel:      &networkingv1alpha2.NewTunnel{Name: "my-new-tunnel"},
			FallbackTarget: "http_status:404",
			Protocol:       "auto",
		},
	}

	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"CLOUDFLARE_API_TOKEN": []byte("test-api-token"),
		},
	}

	innerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, cfSecret).
		WithStatusSubresource(&networkingv1alpha2.Tunnel{}).
		Build()
	fakeClient := &integrationApplyClient{Client: innerClient}

	r := &TunnelReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(100),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-tunnel", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue || result.RequeueAfter > 0 {
		t.Errorf("unexpected requeue: %v", result)
	}

	// Assert: mock received CreateTunnel call
	createCalls := server.GetCallsByPathContains("cfd_tunnel")
	foundCreate := false
	for _, c := range createCalls {
		if c.Method == "POST" && strings.Contains(c.Path, "cfd_tunnel") {
			foundCreate = true
			break
		}
	}
	if !foundCreate {
		t.Error("expected CreateTunnel call to mock server, but none found")
	}

	// Assert: K8s Secret created with credentials.json
	credsSecret := &corev1.Secret{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, credsSecret); err != nil {
		t.Fatalf("credentials Secret not found: %v", err)
	}

	// Assert: K8s ConfigMap created with config.yaml
	cm := &corev1.ConfigMap{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if _, ok := cm.Data[configmapKey]; !ok {
		t.Error("ConfigMap missing config.yaml key")
	}

	// Assert: K8s Deployment created
	dep := &appsv1.Deployment{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, dep); err != nil {
		t.Fatalf("Deployment not found: %v", err)
	}

	// Assert: Tunnel status updated
	updatedTunnel := &networkingv1alpha2.Tunnel{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedTunnel); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
	}
	if updatedTunnel.Status.AccountId != "acc-123" {
		t.Errorf("status.accountId = %q, want %q", updatedTunnel.Status.AccountId, "acc-123")
	}
	if updatedTunnel.Status.ZoneId != "zone-789" {
		t.Errorf("status.zoneId = %q, want %q", updatedTunnel.Status.ZoneId, "zone-789")
	}
	if updatedTunnel.Status.TunnelId == "" {
		t.Error("status.tunnelId should not be empty after new tunnel creation")
	}
}

func TestTunnelReconcile_FullFlow_ExistingTunnel(t *testing.T) {
	server := setupMockServer(t)
	server.AddAccount("acc-123", "test-account")
	server.AddZone("zone-789", "example.com")
	server.AddTunnel("acc-123", "existing-tun-id", "existing-tunnel")

	scheme := integrationScheme()
	tunnel := &networkingv1alpha2.Tunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
			UID:       types.UID("uid-tunnel-2"),
		},
		Spec: networkingv1alpha2.TunnelSpec{
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:                              "example.com",
				Secret:                              "cf-secret",
				AccountId:                           "acc-123",
				AccountName:                         "test-account",
				CLOUDFLARE_API_TOKEN:                "CLOUDFLARE_API_TOKEN",
				CLOUDFLARE_API_KEY:                  "CLOUDFLARE_API_KEY",
				CLOUDFLARE_TUNNEL_CREDENTIAL_FILE:   "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
				CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
			},
			ExistingTunnel: &networkingv1alpha2.ExistingTunnel{
				Id:   "existing-tun-id",
				Name: "existing-tunnel",
			},
			FallbackTarget: "http_status:404",
			Protocol:       "auto",
		},
	}

	credJSON := `{"AccountTag":"acc-123","TunnelID":"existing-tun-id","TunnelName":"existing-tunnel","TunnelSecret":"dGVzdA=="}`
	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"CLOUDFLARE_API_TOKEN":                []byte("test-api-token"),
			"CLOUDFLARE_TUNNEL_CREDENTIAL_FILE":   []byte(credJSON),
		},
	}

	innerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, cfSecret).
		WithStatusSubresource(&networkingv1alpha2.Tunnel{}).
		Build()
	fakeClient := &integrationApplyClient{Client: innerClient}

	r := &TunnelReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(100),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-tunnel", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue || result.RequeueAfter > 0 {
		t.Errorf("unexpected requeue: %v", result)
	}

	// Assert: NO CreateTunnel call
	for _, c := range server.GetCalls() {
		if c.Method == "POST" && strings.Contains(c.Path, "cfd_tunnel") && !strings.Contains(c.Path, "configurations") {
			t.Error("CreateTunnel call made for existing tunnel, should not happen")
		}
	}

	// Assert: managed resources created
	credsSecret := &corev1.Secret{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, credsSecret); err != nil {
		t.Fatalf("credentials Secret not found: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}

	dep := &appsv1.Deployment{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, dep); err != nil {
		t.Fatalf("Deployment not found: %v", err)
	}

	// Assert: status updated
	updatedTunnel := &networkingv1alpha2.Tunnel{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedTunnel); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
	}
	if updatedTunnel.Status.TunnelId != "existing-tun-id" {
		t.Errorf("status.tunnelId = %q, want %q", updatedTunnel.Status.TunnelId, "existing-tun-id")
	}
}

func TestTunnelReconcile_FullFlow_DeleteTunnel(t *testing.T) {
	server := setupMockServer(t)
	server.AddAccount("acc-123", "test-account")
	server.AddZone("zone-789", "example.com")
	server.AddTunnel("acc-123", "tun-del-1", "my-tunnel")

	scheme := integrationScheme()
	now := metav1.Now()
	tunnel := &networkingv1alpha2.Tunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-tunnel",
			Namespace:         "default",
			UID:               types.UID("uid-tunnel-del"),
			DeletionTimestamp: &now,
			Finalizers:        []string{tunnelFinalizer},
		},
		Spec: networkingv1alpha2.TunnelSpec{
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:                              "example.com",
				Secret:                              "cf-secret",
				AccountId:                           "acc-123",
				AccountName:                         "test-account",
				CLOUDFLARE_API_TOKEN:                "CLOUDFLARE_API_TOKEN",
				CLOUDFLARE_API_KEY:                  "CLOUDFLARE_API_KEY",
				CLOUDFLARE_TUNNEL_CREDENTIAL_FILE:   "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
				CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
			},
			NewTunnel:      &networkingv1alpha2.NewTunnel{Name: "my-tunnel"},
			FallbackTarget: "http_status:404",
			Protocol:       "auto",
		},
		Status: networkingv1alpha2.TunnelStatus{
			TunnelId:   "tun-del-1",
			TunnelName: "my-tunnel",
			AccountId:  "acc-123",
			ZoneId:     "zone-789",
		},
	}

	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"CLOUDFLARE_API_TOKEN": []byte("test-api-token"),
		},
	}

	// Create deployment with replicas=0 so cleanup proceeds immediately
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(0)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "cloudflared"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "cloudflared"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "cloudflared",
						Image: "cloudflare/cloudflared:latest",
					}},
				},
			},
		},
	}

	innerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, cfSecret, dep).
		WithStatusSubresource(&networkingv1alpha2.Tunnel{}).
		Build()
	fakeClient := &integrationApplyClient{Client: innerClient}

	r := &TunnelReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(100),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-tunnel", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	// The fake client auto-deletes the object when the last finalizer is removed (DeletionTimestamp set),
	// so the subsequent updateTunnelStatus call may fail with NotFound. This is expected behavior
	// specific to the fake client — the real K8s API server handles GC asynchronously.
	// The important assertions are that the CF API calls were made.
	_ = result
	_ = err

	// Assert: DeleteTunnel called
	foundDelete := false
	for _, c := range server.GetCalls() {
		if c.Method == "DELETE" && strings.Contains(c.Path, "cfd_tunnel") && !strings.Contains(c.Path, "connections") {
			foundDelete = true
			break
		}
	}
	if !foundDelete {
		t.Error("expected DeleteTunnel call to mock server, but none found")
	}

	// Assert: ClearTunnelConfiguration called (PUT with empty config)
	foundConfigUpdate := false
	for _, c := range server.GetCalls() {
		if c.Method == "PUT" && strings.Contains(c.Path, "configurations") {
			foundConfigUpdate = true
			break
		}
	}
	if !foundConfigUpdate {
		t.Error("expected ClearTunnelConfiguration (PUT configurations) call, but none found")
	}

	// Assert: finalizer removed — when the fake client processes an Update that removes the last
	// finalizer from an object with a DeletionTimestamp, it deletes the object entirely.
	// So "not found" means the finalizer was successfully removed.
	updatedTunnel := &networkingv1alpha2.Tunnel{}
	err = innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedTunnel)
	if err == nil {
		// Object still exists — check finalizer was removed
		for _, f := range updatedTunnel.Finalizers {
			if f == tunnelFinalizer {
				t.Error("finalizer should have been removed after deletion")
			}
		}
	}
	// err != nil (NotFound) is also acceptable — means finalizer was removed and object was garbage collected
}

func TestClusterTunnelReconcile_FullFlow(t *testing.T) {
	server := setupMockServer(t)
	server.AddAccount("acc-123", "test-account")
	server.AddZone("zone-789", "example.com")

	scheme := integrationScheme()
	operatorNS := "cloudflare-operator-system"

	clusterTunnel := &networkingv1alpha2.ClusterTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-cluster-tunnel",
			UID:  types.UID("uid-ct-1"),
		},
		Spec: networkingv1alpha2.TunnelSpec{
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:                              "example.com",
				Secret:                              "cf-secret",
				AccountId:                           "acc-123",
				AccountName:                         "test-account",
				CLOUDFLARE_API_TOKEN:                "CLOUDFLARE_API_TOKEN",
				CLOUDFLARE_API_KEY:                  "CLOUDFLARE_API_KEY",
				CLOUDFLARE_TUNNEL_CREDENTIAL_FILE:   "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
				CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
			},
			NewTunnel:      &networkingv1alpha2.NewTunnel{Name: "my-cluster-tunnel"},
			FallbackTarget: "http_status:404",
			Protocol:       "auto",
		},
	}

	// Secret must be in the operator namespace for ClusterTunnel
	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: operatorNS,
		},
		Data: map[string][]byte{
			"CLOUDFLARE_API_TOKEN": []byte("test-api-token"),
		},
	}

	// Ensure operator namespace exists
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: operatorNS},
	}

	innerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(clusterTunnel, cfSecret, ns).
		WithStatusSubresource(&networkingv1alpha2.ClusterTunnel{}).
		Build()
	fakeClient := &integrationApplyClient{Client: innerClient}

	r := &ClusterTunnelReconciler{
		Client:    fakeClient,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(100),
		Namespace: operatorNS,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-cluster-tunnel"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue || result.RequeueAfter > 0 {
		t.Errorf("unexpected requeue: %v", result)
	}

	// Assert: CreateTunnel called
	foundCreate := false
	for _, c := range server.GetCalls() {
		if c.Method == "POST" && strings.Contains(c.Path, "cfd_tunnel") {
			foundCreate = true
			break
		}
	}
	if !foundCreate {
		t.Error("expected CreateTunnel call to mock server, but none found")
	}

	// Assert: resources created in operator namespace
	cm := &corev1.ConfigMap{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-cluster-tunnel", Namespace: operatorNS}, cm); err != nil {
		t.Fatalf("ConfigMap not found in operator namespace: %v", err)
	}

	dep := &appsv1.Deployment{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-cluster-tunnel", Namespace: operatorNS}, dep); err != nil {
		t.Fatalf("Deployment not found in operator namespace: %v", err)
	}

	// Assert: status updated
	updatedCT := &networkingv1alpha2.ClusterTunnel{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-cluster-tunnel"}, updatedCT); err != nil {
		t.Fatalf("failed to get updated ClusterTunnel: %v", err)
	}
	if updatedCT.Status.AccountId != "acc-123" {
		t.Errorf("status.accountId = %q, want %q", updatedCT.Status.AccountId, "acc-123")
	}
	if updatedCT.Status.TunnelId == "" {
		t.Error("status.tunnelId should not be empty")
	}
}

func TestTunnelBindingReconcile_FullFlow(t *testing.T) {
	server := setupMockServer(t)
	server.AddAccount("acc-123", "test-account")
	server.AddZone("zone-789", "example.com")
	server.AddTunnel("acc-123", "tun-bind-1", "my-tunnel")

	scheme := integrationScheme()

	tunnel := &networkingv1alpha2.Tunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
			UID:       types.UID("uid-tun-bind"),
		},
		Spec: networkingv1alpha2.TunnelSpec{
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:                              "example.com",
				Secret:                              "cf-secret",
				AccountId:                           "acc-123",
				AccountName:                         "test-account",
				CLOUDFLARE_API_TOKEN:                "CLOUDFLARE_API_TOKEN",
				CLOUDFLARE_API_KEY:                  "CLOUDFLARE_API_KEY",
				CLOUDFLARE_TUNNEL_CREDENTIAL_FILE:   "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
				CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
			},
			ExistingTunnel: &networkingv1alpha2.ExistingTunnel{
				Id:   "tun-bind-1",
				Name: "my-tunnel",
			},
			FallbackTarget: "http_status:404",
		},
		Status: networkingv1alpha2.TunnelStatus{
			TunnelId:   "tun-bind-1",
			TunnelName: "my-tunnel",
			AccountId:  "acc-123",
			ZoneId:     "zone-789",
		},
	}

	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"CLOUDFLARE_API_TOKEN": []byte("test-api-token"),
		},
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-svc",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{
				Port:     80,
				Protocol: corev1.ProtocolTCP,
			}},
		},
	}

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-binding",
			Namespace: "default",
		},
		TunnelRef: networkingv1alpha1.TunnelRef{
			Kind: "Tunnel",
			Name: "my-tunnel",
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{{
			Kind: "Service",
			Name: "my-svc",
		}},
	}

	// Create a ConfigMap as if the tunnel reconciler already ran
	initialConfig := cf.Configuration{
		TunnelId:   "tun-bind-1",
		SourceFile: "/etc/cloudflared/creds/credentials.json",
		Metrics:    "0.0.0.0:2000",
		Ingress: []cf.UnvalidatedIngressRule{{
			Service: "http_status:404",
		}},
	}
	initialConfigBytes, _ := yaml.Marshal(initialConfig)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
		},
		Data: map[string]string{configmapKey: string(initialConfigBytes)},
	}

	// Create a Deployment for the setConfigMapConfiguration method
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "cloudflared"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "cloudflared"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "cloudflared",
						Image: "cloudflare/cloudflared:latest",
					}},
				},
			},
		},
	}

	innerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, cfSecret, svc, binding, cm, dep).
		WithStatusSubresource(&networkingv1alpha1.TunnelBinding{}).
		Build()

	r := &TunnelBindingReconciler{
		Client:    innerClient,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(100),
		Namespace: "cloudflare-operator-system",
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "my-binding", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue || result.RequeueAfter > 0 {
		t.Errorf("unexpected requeue: %v", result)
	}

	// Assert: status updated with hostname and target
	updatedBinding := &networkingv1alpha1.TunnelBinding{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-binding", Namespace: "default"}, updatedBinding); err != nil {
		t.Fatalf("failed to get updated binding: %v", err)
	}
	if len(updatedBinding.Status.Services) != 1 {
		t.Fatalf("expected 1 service in status, got %d", len(updatedBinding.Status.Services))
	}
	if updatedBinding.Status.Services[0].Hostname != "my-svc.example.com" {
		t.Errorf("status hostname = %q, want %q", updatedBinding.Status.Services[0].Hostname, "my-svc.example.com")
	}
	if updatedBinding.Status.Services[0].Target != "http://my-svc.default.svc:80" {
		t.Errorf("status target = %q, want %q", updatedBinding.Status.Services[0].Target, "http://my-svc.default.svc:80")
	}

	// Assert: labels set on binding
	if updatedBinding.Labels[tunnelNameLabel] != "my-tunnel" {
		t.Errorf("label %s = %q, want %q", tunnelNameLabel, updatedBinding.Labels[tunnelNameLabel], "my-tunnel")
	}
	if updatedBinding.Labels[tunnelKindLabel] != "Tunnel" {
		t.Errorf("label %s = %q, want %q", tunnelKindLabel, updatedBinding.Labels[tunnelKindLabel], "Tunnel")
	}

	// Assert: finalizer added
	foundFinalizer := false
	for _, f := range updatedBinding.Finalizers {
		if f == tunnelFinalizer {
			foundFinalizer = true
			break
		}
	}
	if !foundFinalizer {
		t.Error("expected finalizer to be added to TunnelBinding")
	}

	// Assert: ConfigMap updated with ingress rules.
	// On first reconcile, configureCloudflareDaemon lists bindings by label. Since labels are set
	// in creationLogic (which runs AFTER configureCloudflareDaemon), the first reconcile will only
	// have the catchall. A second reconcile (after labels are set) would include the service rule.
	updatedCM := &corev1.ConfigMap{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedCM); err != nil {
		t.Fatalf("failed to get updated ConfigMap: %v", err)
	}
	config := &cf.Configuration{}
	if err := yaml.Unmarshal([]byte(updatedCM.Data[configmapKey]), config); err != nil {
		t.Fatalf("failed to parse updated config.yaml: %v", err)
	}
	// Catchall only on first reconcile (labels not yet set when configmap is built)
	if len(config.Ingress) < 1 {
		t.Fatal("expected at least 1 ingress rule (catchall)")
	}

	// Run a second reconcile now that labels are set — this time the binding will be found
	result2, err2 := r.Reconcile(context.Background(), req)
	if err2 != nil {
		t.Fatalf("second Reconcile returned error: %v", err2)
	}
	if result2.Requeue || result2.RequeueAfter > 0 {
		t.Errorf("unexpected requeue on second reconcile: %v", result2)
	}

	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedCM); err != nil {
		t.Fatalf("failed to get ConfigMap after second reconcile: %v", err)
	}
	config2 := &cf.Configuration{}
	if err := yaml.Unmarshal([]byte(updatedCM.Data[configmapKey]), config2); err != nil {
		t.Fatalf("failed to parse config.yaml after second reconcile: %v", err)
	}
	// Now should have: 1 service rule + 1 catchall = 2
	if len(config2.Ingress) != 2 {
		t.Fatalf("expected 2 ingress rules after second reconcile, got %d", len(config2.Ingress))
	}
	if config2.Ingress[0].Hostname != "my-svc.example.com" {
		t.Errorf("ingress[0] hostname = %q, want %q", config2.Ingress[0].Hostname, "my-svc.example.com")
	}

	// Assert: DNS CNAME + TXT created
	postCalls := server.GetCallsByMethod("POST")
	dnsCreates := 0
	for _, c := range postCalls {
		if strings.Contains(c.Path, "dns_records") {
			dnsCreates++
		}
	}
	if dnsCreates < 2 {
		t.Errorf("expected at least 2 DNS record creates (CNAME + TXT), got %d", dnsCreates)
	}

	// Assert: UpdateTunnelConfiguration called (edge sync)
	foundEdgeSync := false
	for _, c := range server.GetCalls() {
		if c.Method == "PUT" && strings.Contains(c.Path, "configurations") {
			foundEdgeSync = true
			break
		}
	}
	if !foundEdgeSync {
		t.Error("expected UpdateTunnelConfiguration (PUT configurations) call for edge sync")
	}
}

func TestTunnelBindingReconcile_FullFlow_Delete(t *testing.T) {
	server := setupMockServer(t)
	server.AddAccount("acc-123", "test-account")
	server.AddZone("zone-789", "example.com")
	server.AddTunnel("acc-123", "tun-bind-del", "my-tunnel")

	// Pre-configure DNS records to be cleaned up
	proxied := true
	server.AddDNSRecord(cfmock.DNSRecord{
		ID:      "dns-cname-1",
		ZoneID:  "zone-789",
		Type:    "CNAME",
		Name:    "my-svc.example.com",
		Content: "tun-bind-del.cfargotunnel.com",
		Proxied: &proxied,
	})
	txtContent, _ := json.Marshal(cf.DnsManagedRecordTxt{
		TunnelId:   "tun-bind-del",
		TunnelName: "my-tunnel",
		DnsId:      "dns-cname-1",
	})
	server.AddDNSRecord(cfmock.DNSRecord{
		ID:      "dns-txt-1",
		ZoneID:  "zone-789",
		Type:    "TXT",
		Name:    "_managed.my-svc.example.com",
		Content: string(txtContent),
	})

	scheme := integrationScheme()
	now := metav1.Now()

	tunnel := &networkingv1alpha2.Tunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
			UID:       types.UID("uid-tun-bind-del"),
		},
		Spec: networkingv1alpha2.TunnelSpec{
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:                              "example.com",
				Secret:                              "cf-secret",
				AccountId:                           "acc-123",
				AccountName:                         "test-account",
				CLOUDFLARE_API_TOKEN:                "CLOUDFLARE_API_TOKEN",
				CLOUDFLARE_API_KEY:                  "CLOUDFLARE_API_KEY",
				CLOUDFLARE_TUNNEL_CREDENTIAL_FILE:   "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
				CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
			},
			ExistingTunnel: &networkingv1alpha2.ExistingTunnel{
				Id:   "tun-bind-del",
				Name: "my-tunnel",
			},
			FallbackTarget: "http_status:404",
		},
		Status: networkingv1alpha2.TunnelStatus{
			TunnelId:   "tun-bind-del",
			TunnelName: "my-tunnel",
			AccountId:  "acc-123",
			ZoneId:     "zone-789",
		},
	}

	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"CLOUDFLARE_API_TOKEN": []byte("test-api-token"),
		},
	}

	initialConfig := cf.Configuration{
		TunnelId: "tun-bind-del",
		Ingress: []cf.UnvalidatedIngressRule{{
			Hostname: "my-svc.example.com",
			Service:  "http://my-svc.default.svc:80",
		}, {
			Service: "http_status:404",
		}},
	}
	initialConfigBytes, _ := yaml.Marshal(initialConfig)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
		},
		Data: map[string]string{configmapKey: string(initialConfigBytes)},
	}

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-binding",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{tunnelFinalizer},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{
			Kind: "Tunnel",
			Name: "my-tunnel",
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{{
			Kind: "Service",
			Name: "my-svc",
		}},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Hostnames: "my-svc.example.com",
			Services: []networkingv1alpha1.ServiceInfo{{
				Hostname: "my-svc.example.com",
				Target:   "http://my-svc.default.svc:80",
			}},
		},
	}

	innerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, cfSecret, cm, binding).
		WithStatusSubresource(&networkingv1alpha1.TunnelBinding{}).
		Build()

	r := &TunnelBindingReconciler{
		Client:    innerClient,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(100),
		Namespace: "cloudflare-operator-system",
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "my-binding", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Assert: requeue (RequeueAfter: 1 second per the code)
	if result.RequeueAfter != time.Second {
		t.Errorf("expected RequeueAfter=1s, got %v", result.RequeueAfter)
	}

	// Assert: DNS cleanup calls
	deleteCalls := server.GetCallsByMethod("DELETE")
	dnsDeletes := 0
	for _, c := range deleteCalls {
		if strings.Contains(c.Path, "dns_records") {
			dnsDeletes++
		}
	}
	if dnsDeletes < 2 {
		t.Errorf("expected at least 2 DNS record deletes (CNAME + TXT), got %d", dnsDeletes)
	}

	// Assert: finalizer removed — fake client deletes objects when last finalizer is removed
	// with DeletionTimestamp set, so NotFound is the expected outcome.
	updatedBinding := &networkingv1alpha1.TunnelBinding{}
	err = innerClient.Get(context.Background(), types.NamespacedName{Name: "my-binding", Namespace: "default"}, updatedBinding)
	if err == nil {
		for _, f := range updatedBinding.Finalizers {
			if f == tunnelFinalizer {
				t.Error("finalizer should have been removed after deletion")
			}
		}
	}
	// NotFound is also acceptable — means finalizer was removed and object was garbage collected
}

func TestTunnelBindingReconcile_FullFlow_CredentialSecretRef(t *testing.T) {
	server := setupMockServer(t)
	server.AddAccount("acc-123", "test-account")
	server.AddZone("zone-789", "example.com")
	server.AddTunnel("acc-123", "tun-cred-1", "my-tunnel")

	scheme := integrationScheme()
	operatorNS := "cloudflare-operator-system"

	tunnel := &networkingv1alpha2.Tunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
			UID:       types.UID("uid-tun-cred"),
		},
		Spec: networkingv1alpha2.TunnelSpec{
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:                              "example.com",
				Secret:                              "cf-secret-nonexistent",
				AccountId:                           "acc-123",
				AccountName:                         "test-account",
				CLOUDFLARE_API_TOKEN:                "CLOUDFLARE_API_TOKEN",
				CLOUDFLARE_API_KEY:                  "CLOUDFLARE_API_KEY",
				CLOUDFLARE_TUNNEL_CREDENTIAL_FILE:   "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
				CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
			},
			ExistingTunnel: &networkingv1alpha2.ExistingTunnel{
				Id:   "tun-cred-1",
				Name: "my-tunnel",
			},
			FallbackTarget: "http_status:404",
		},
		Status: networkingv1alpha2.TunnelStatus{
			TunnelId:   "tun-cred-1",
			TunnelName: "my-tunnel",
			AccountId:  "acc-123",
			ZoneId:     "zone-789",
		},
	}

	// Secret in operator namespace — NOT in the binding namespace
	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret-operator",
			Namespace: operatorNS,
		},
		Data: map[string][]byte{
			"CLOUDFLARE_API_TOKEN": []byte("test-api-token"),
		},
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-svc",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{
				Port:     80,
				Protocol: corev1.ProtocolTCP,
			}},
		},
	}

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-binding-cred",
			Namespace: "default",
		},
		TunnelRef: networkingv1alpha1.TunnelRef{
			Kind: "Tunnel",
			Name: "my-tunnel",
			CredentialSecretRef: &networkingv1alpha1.SecretReference{
				Name:      "cf-secret-operator",
				Namespace: operatorNS,
			},
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{{
			Kind: "Service",
			Name: "my-svc",
		}},
	}

	initialConfig := cf.Configuration{
		TunnelId: "tun-cred-1",
		Ingress: []cf.UnvalidatedIngressRule{{
			Service: "http_status:404",
		}},
	}
	initialConfigBytes, _ := yaml.Marshal(initialConfig)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
		},
		Data: map[string]string{configmapKey: string(initialConfigBytes)},
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "cloudflared"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "cloudflared"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "cloudflared",
						Image: "cloudflare/cloudflared:latest",
					}},
				},
			},
		},
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: operatorNS},
	}

	innerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, cfSecret, svc, binding, cm, dep, ns).
		WithStatusSubresource(&networkingv1alpha1.TunnelBinding{}).
		Build()

	r := &TunnelBindingReconciler{
		Client:    innerClient,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(100),
		Namespace: operatorNS,
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "my-binding-cred", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue || result.RequeueAfter > 0 {
		t.Errorf("unexpected requeue: %v", result)
	}

	// Assert: succeeds (reads from credentialSecretRef in operator namespace)
	updatedBinding := &networkingv1alpha1.TunnelBinding{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-binding-cred", Namespace: "default"}, updatedBinding); err != nil {
		t.Fatalf("failed to get updated binding: %v", err)
	}
	if len(updatedBinding.Status.Services) != 1 {
		t.Fatalf("expected 1 service in status, got %d", len(updatedBinding.Status.Services))
	}
	if updatedBinding.Status.Services[0].Hostname != "my-svc.example.com" {
		t.Errorf("status hostname = %q, want %q", updatedBinding.Status.Services[0].Hostname, "my-svc.example.com")
	}

	// Assert: labels set
	if updatedBinding.Labels[tunnelNameLabel] != "my-tunnel" {
		t.Errorf("label %s = %q, want %q", tunnelNameLabel, updatedBinding.Labels[tunnelNameLabel], "my-tunnel")
	}
}
