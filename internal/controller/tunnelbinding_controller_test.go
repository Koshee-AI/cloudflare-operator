package controller

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	networkingv1alpha1 "github.com/adyanth/cloudflare-operator/api/v1alpha1"
	networkingv1alpha2 "github.com/adyanth/cloudflare-operator/api/v1alpha2"
	"github.com/adyanth/cloudflare-operator/internal/clients/cf"
	"github.com/adyanth/cloudflare-operator/internal/testutil/cfmock"
	"github.com/cloudflare/cloudflare-go"
	"github.com/go-logr/logr"
	yaml "gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := networkingv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := networkingv1alpha2.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLabelsForBinding(t *testing.T) {
	binding := networkingv1alpha1.TunnelBinding{
		TunnelRef: networkingv1alpha1.TunnelRef{
			Kind: "Tunnel",
			Name: "my-tunnel",
		},
	}
	labels := labelsForBinding(binding)

	if labels[tunnelNameLabel] != "my-tunnel" {
		t.Errorf("expected tunnelNameLabel=%q, got %q", "my-tunnel", labels[tunnelNameLabel])
	}
	if labels[tunnelKindLabel] != "Tunnel" {
		t.Errorf("expected tunnelKindLabel=%q, got %q", "Tunnel", labels[tunnelKindLabel])
	}
}

func TestLabelsForBinding_ClusterTunnel(t *testing.T) {
	binding := networkingv1alpha1.TunnelBinding{
		TunnelRef: networkingv1alpha1.TunnelRef{
			Kind: "ClusterTunnel",
			Name: "ct-1",
		},
	}
	labels := labelsForBinding(binding)

	if labels[tunnelKindLabel] != "ClusterTunnel" {
		t.Errorf("expected tunnelKindLabel=%q, got %q", "ClusterTunnel", labels[tunnelKindLabel])
	}
	if labels[tunnelNameLabel] != "ct-1" {
		t.Errorf("expected tunnelNameLabel=%q, got %q", "ct-1", labels[tunnelNameLabel])
	}
}

func TestGetServiceProto(t *testing.T) {
	r := &TunnelBindingReconciler{}

	tests := []struct {
		name         string
		tunnelProto  string
		validProto   bool
		portProto    corev1.Protocol
		port         int32
		wantProto    string
	}{
		{"TCP port 80 -> http", "", false, corev1.ProtocolTCP, 80, tunnelProtoHTTP},
		{"TCP port 443 -> https", "", false, corev1.ProtocolTCP, 443, tunnelProtoHTTPS},
		{"TCP port 22 -> ssh", "", false, corev1.ProtocolTCP, 22, tunnelProtoSSH},
		{"TCP port 3389 -> rdp", "", false, corev1.ProtocolTCP, 3389, tunnelProtoRDP},
		{"TCP port 139 -> smb", "", false, corev1.ProtocolTCP, 139, tunnelProtoSMB},
		{"TCP port 445 -> smb", "", false, corev1.ProtocolTCP, 445, tunnelProtoSMB},
		{"TCP port 8080 -> http (default)", "", false, corev1.ProtocolTCP, 8080, tunnelProtoHTTP},
		{"UDP -> udp", "", false, corev1.ProtocolUDP, 5000, tunnelProtoUDP},
		{"Explicit https on any port", "https", true, corev1.ProtocolTCP, 8080, tunnelProtoHTTPS},
		{"Explicit tcp", "tcp", true, corev1.ProtocolTCP, 80, tunnelProtoTCP},
		{"Invalid protocol falls back to default", "invalid", false, corev1.ProtocolTCP, 80, tunnelProtoHTTP},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := corev1.ServicePort{
				Protocol: tt.portProto,
				Port:     tt.port,
			}
			got := r.getServiceProto(tt.tunnelProto, tt.validProto, port)
			if got != tt.wantProto {
				t.Errorf("getServiceProto(%q, %v, port{%s,%d}) = %q, want %q",
					tt.tunnelProto, tt.validProto, tt.portProto, tt.port, got, tt.wantProto)
			}
		})
	}
}

func TestReconcile_NotFound_NoPanic(t *testing.T) {
	s := newScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	r := &TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(10),
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{
			Name:      "nonexistent-binding",
			Namespace: "default",
		},
	})

	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("expected empty Result, got %+v", result)
	}
}

func TestReconcile_ClearsStaleState(t *testing.T) {
	s := newScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	r := &TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(10),
	}

	// Set stale state from a hypothetical previous reconcile
	r.binding = &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: "default"},
	}
	r.configmap = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "stale-cm", Namespace: "default"},
	}
	r.cfAPI = &cf.API{TunnelName: "stale-tunnel"}
	r.fallbackTarget = "http_status:503"

	_, _ = r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{
			Name:      "nonexistent",
			Namespace: "default",
		},
	})

	if r.binding != nil {
		t.Errorf("expected r.binding to be nil after reconcile, got %+v", r.binding)
	}
	if r.configmap != nil {
		t.Errorf("expected r.configmap to be nil after reconcile, got %+v", r.configmap)
	}
	if r.cfAPI != nil {
		t.Errorf("expected r.cfAPI to be nil after reconcile, got %+v", r.cfAPI)
	}
	if r.fallbackTarget != "" {
		t.Errorf("expected r.fallbackTarget to be empty, got %q", r.fallbackTarget)
	}
}

func TestGetRelevantTunnelBindings(t *testing.T) {
	s := newScheme(t)

	matchingBindings := []networkingv1alpha1.TunnelBinding{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "binding-a",
				Namespace: "default",
				Labels: map[string]string{
					tunnelNameLabel: "my-tunnel",
					tunnelKindLabel: "Tunnel",
				},
			},
			Subjects:  []networkingv1alpha1.TunnelBindingSubject{{Kind: "Service", Name: "svc-a"}},
			TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "binding-b",
				Namespace: "default",
				Labels: map[string]string{
					tunnelNameLabel: "my-tunnel",
					tunnelKindLabel: "Tunnel",
				},
			},
			Subjects:  []networkingv1alpha1.TunnelBindingSubject{{Kind: "Service", Name: "svc-b"}},
			TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "binding-c",
				Namespace: "default",
				Labels: map[string]string{
					tunnelNameLabel: "my-tunnel",
					tunnelKindLabel: "Tunnel",
				},
			},
			Subjects:  []networkingv1alpha1.TunnelBindingSubject{{Kind: "Service", Name: "svc-c"}},
			TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		},
	}

	nonMatchingBindings := []networkingv1alpha1.TunnelBinding{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "binding-other-name",
				Namespace: "default",
				Labels: map[string]string{
					tunnelNameLabel: "other-tunnel",
					tunnelKindLabel: "Tunnel",
				},
			},
			Subjects:  []networkingv1alpha1.TunnelBindingSubject{{Kind: "Service", Name: "svc-x"}},
			TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "other-tunnel"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "binding-cluster",
				Namespace: "default",
				Labels: map[string]string{
					tunnelNameLabel: "my-tunnel",
					tunnelKindLabel: "ClusterTunnel",
				},
			},
			Subjects:  []networkingv1alpha1.TunnelBindingSubject{{Kind: "Service", Name: "svc-y"}},
			TunnelRef: networkingv1alpha1.TunnelRef{Kind: "ClusterTunnel", Name: "my-tunnel"},
		},
	}

	var objs []runtime.Object
	for i := range matchingBindings {
		objs = append(objs, &matchingBindings[i])
	}
	for i := range nonMatchingBindings {
		objs = append(objs, &nonMatchingBindings[i])
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build()

	r := &TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(10),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		binding: &networkingv1alpha1.TunnelBinding{
			TunnelRef: networkingv1alpha1.TunnelRef{
				Kind: "Tunnel",
				Name: "my-tunnel",
			},
		},
	}

	results, err := r.getRelevantTunnelBindings()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 bindings, got %d", len(results))
	}

	// Verify sorted by name
	if results[0].Name != "binding-a" {
		t.Errorf("expected results[0].Name=%q, got %q", "binding-a", results[0].Name)
	}
	if results[1].Name != "binding-b" {
		t.Errorf("expected results[1].Name=%q, got %q", "binding-b", results[1].Name)
	}
	if results[2].Name != "binding-c" {
		t.Errorf("expected results[2].Name=%q, got %q", "binding-c", results[2].Name)
	}
}

func TestGetRelevantTunnelBindings_Empty(t *testing.T) {
	s := newScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	r := &TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(10),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		binding: &networkingv1alpha1.TunnelBinding{
			TunnelRef: networkingv1alpha1.TunnelRef{
				Kind: "Tunnel",
				Name: "my-tunnel",
			},
		},
	}

	results, err := r.getRelevantTunnelBindings()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 bindings, got %d", len(results))
	}
}

func newTestCfAPI(t *testing.T, s *cfmock.Server) *cf.API {
	t.Helper()
	client, err := cloudflare.NewWithAPIToken("test-token", cloudflare.BaseURL(s.URL+"/client/v4"))
	if err != nil {
		t.Fatalf("failed to create cloudflare client: %v", err)
	}
	return &cf.API{
		Log:              logr.Discard(),
		CloudflareClient: client,
	}
}

func TestConfigureCloudflareDaemon_BuildsIngressRules(t *testing.T) {
	s := newScheme(t)

	mockServer := cfmock.NewServer()
	defer mockServer.Close()
	mockServer.AddAccount("acct-1", "my-account")
	mockServer.AddZone("zone-1", "example.com")
	mockServer.AddTunnel("acct-1", "tun-1", "my-tunnel")

	cfAPI := newTestCfAPI(t, mockServer)
	cfAPI.ValidAccountId = "acct-1"
	cfAPI.ValidTunnelId = "tun-1"
	cfAPI.ValidTunnelName = "my-tunnel"
	cfAPI.ValidZoneId = "zone-1"
	cfAPI.Domain = "example.com"

	noTLS := false
	http2 := false
	proxyAddr := "127.0.0.1"
	var proxyPort uint
	proxyType := ""

	binding1 := networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "binding-1",
			Namespace: "default",
			Labels: map[string]string{
				tunnelNameLabel: "my-tunnel",
				tunnelKindLabel: "Tunnel",
			},
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{
				Kind: "Service",
				Name: "web",
				Spec: networkingv1alpha1.TunnelBindingSubjectSpec{
					NoTlsVerify:  false,
					Http2Origin:  false,
					ProxyAddress: "127.0.0.1",
					ProxyPort:    0,
					ProxyType:    "",
				},
			},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "web.example.com", Target: "http://web.default.svc:80"},
			},
		},
	}

	binding2 := networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "binding-2",
			Namespace: "default",
			Labels: map[string]string{
				tunnelNameLabel: "my-tunnel",
				tunnelKindLabel: "Tunnel",
			},
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{
				Kind: "Service",
				Name: "api",
				Spec: networkingv1alpha1.TunnelBindingSubjectSpec{
					NoTlsVerify:  false,
					Http2Origin:  false,
					ProxyAddress: "127.0.0.1",
					ProxyPort:    0,
					ProxyType:    "",
				},
			},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "api.example.com", Target: "http://api.default.svc:8080"},
			},
		},
	}

	existingConfig := cf.Configuration{TunnelId: "tun-1", Ingress: []cf.UnvalidatedIngressRule{{Service: "http_status:404"}}}
	configBytes, _ := yaml.Marshal(existingConfig)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tunnel", Namespace: "default"},
		Data:       map[string]string{configmapKey: string(configBytes)},
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tunnel", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "cloudflared"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "cloudflared"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "cloudflared", Image: "cloudflare/cloudflared"}}},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(&binding1, &binding2, cm, deploy).Build()

	r := &TunnelBindingReconciler{
		Client:         fakeClient,
		Scheme:         s,
		Recorder:       record.NewFakeRecorder(10),
		ctx:            context.Background(),
		log:            ctrllog.Log,
		cfAPI:          cfAPI,
		fallbackTarget: "http_status:404",
		configmap:      cm,
		binding: &networkingv1alpha1.TunnelBinding{
			TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		},
	}

	if err := r.configureCloudflareDaemon(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Re-read the ConfigMap from the fake client
	updatedCM := &corev1.ConfigMap{}
	if err := fakeClient.Get(context.Background(), apitypes.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedCM); err != nil {
		t.Fatalf("failed to get updated configmap: %v", err)
	}

	var parsedConfig cf.Configuration
	if err := yaml.Unmarshal([]byte(updatedCM.Data[configmapKey]), &parsedConfig); err != nil {
		t.Fatalf("failed to parse config yaml: %v", err)
	}

	// 2 service rules + 1 catchall = 3
	if len(parsedConfig.Ingress) != 3 {
		t.Fatalf("expected 3 ingress rules, got %d", len(parsedConfig.Ingress))
	}

	// First rule: binding-1's service (sorted by name)
	if parsedConfig.Ingress[0].Hostname != "web.example.com" {
		t.Errorf("expected ingress[0].Hostname=%q, got %q", "web.example.com", parsedConfig.Ingress[0].Hostname)
	}
	if parsedConfig.Ingress[0].Service != "http://web.default.svc:80" {
		t.Errorf("expected ingress[0].Service=%q, got %q", "http://web.default.svc:80", parsedConfig.Ingress[0].Service)
	}

	// Second rule: binding-2's service
	if parsedConfig.Ingress[1].Hostname != "api.example.com" {
		t.Errorf("expected ingress[1].Hostname=%q, got %q", "api.example.com", parsedConfig.Ingress[1].Hostname)
	}
	if parsedConfig.Ingress[1].Service != "http://api.default.svc:8080" {
		t.Errorf("expected ingress[1].Service=%q, got %q", "http://api.default.svc:8080", parsedConfig.Ingress[1].Service)
	}

	// Catchall is last
	if parsedConfig.Ingress[2].Hostname != "" {
		t.Errorf("expected catchall to have empty hostname, got %q", parsedConfig.Ingress[2].Hostname)
	}
	if parsedConfig.Ingress[2].Service != "http_status:404" {
		t.Errorf("expected catchall service=%q, got %q", "http_status:404", parsedConfig.Ingress[2].Service)
	}

	// Verify OriginRequestConfig fields on the first rule
	rule0 := parsedConfig.Ingress[0]
	if rule0.OriginRequest.NoTLSVerify == nil || *rule0.OriginRequest.NoTLSVerify != noTLS {
		t.Errorf("expected NoTLSVerify=%v on rule 0", noTLS)
	}
	if rule0.OriginRequest.Http2Origin == nil || *rule0.OriginRequest.Http2Origin != http2 {
		t.Errorf("expected Http2Origin=%v on rule 0", http2)
	}
	if rule0.OriginRequest.ProxyAddress == nil || *rule0.OriginRequest.ProxyAddress != proxyAddr {
		t.Errorf("expected ProxyAddress=%q on rule 0", proxyAddr)
	}
	if rule0.OriginRequest.ProxyPort == nil || *rule0.OriginRequest.ProxyPort != proxyPort {
		t.Errorf("expected ProxyPort=%d on rule 0", proxyPort)
	}
	if rule0.OriginRequest.ProxyType == nil || *rule0.OriginRequest.ProxyType != proxyType {
		t.Errorf("expected ProxyType=%q on rule 0", proxyType)
	}
}

func TestConfigureCloudflareDaemon_CatchallOnly(t *testing.T) {
	s := newScheme(t)

	mockServer := cfmock.NewServer()
	defer mockServer.Close()
	mockServer.AddAccount("acct-1", "my-account")
	mockServer.AddZone("zone-1", "example.com")
	mockServer.AddTunnel("acct-1", "tun-1", "my-tunnel")

	cfAPI := newTestCfAPI(t, mockServer)
	cfAPI.ValidAccountId = "acct-1"
	cfAPI.ValidTunnelId = "tun-1"
	cfAPI.ValidTunnelName = "my-tunnel"
	cfAPI.ValidZoneId = "zone-1"
	cfAPI.Domain = "example.com"

	existingConfig := cf.Configuration{TunnelId: "tun-1", Ingress: []cf.UnvalidatedIngressRule{{Service: "http_status:404"}}}
	configBytes, _ := yaml.Marshal(existingConfig)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tunnel", Namespace: "default"},
		Data:       map[string]string{configmapKey: string(configBytes)},
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tunnel", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "cloudflared"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "cloudflared"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "cloudflared", Image: "cloudflare/cloudflared"}}},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(cm, deploy).Build()

	r := &TunnelBindingReconciler{
		Client:         fakeClient,
		Scheme:         s,
		Recorder:       record.NewFakeRecorder(10),
		ctx:            context.Background(),
		log:            ctrllog.Log,
		cfAPI:          cfAPI,
		fallbackTarget: "http_status:404",
		configmap:      cm,
		binding: &networkingv1alpha1.TunnelBinding{
			TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		},
	}

	if err := r.configureCloudflareDaemon(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updatedCM := &corev1.ConfigMap{}
	if err := fakeClient.Get(context.Background(), apitypes.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedCM); err != nil {
		t.Fatalf("failed to get updated configmap: %v", err)
	}

	var parsedConfig cf.Configuration
	if err := yaml.Unmarshal([]byte(updatedCM.Data[configmapKey]), &parsedConfig); err != nil {
		t.Fatalf("failed to parse config yaml: %v", err)
	}

	if len(parsedConfig.Ingress) != 1 {
		t.Fatalf("expected 1 ingress rule (catchall only), got %d", len(parsedConfig.Ingress))
	}
	if parsedConfig.Ingress[0].Service != "http_status:404" {
		t.Errorf("expected catchall service=%q, got %q", "http_status:404", parsedConfig.Ingress[0].Service)
	}
	if parsedConfig.Ingress[0].Hostname != "" {
		t.Errorf("expected catchall hostname to be empty, got %q", parsedConfig.Ingress[0].Hostname)
	}
}

func TestSetConfigMapConfiguration_ChecksumOnPodTemplate(t *testing.T) {
	s := newScheme(t)

	existingConfig := cf.Configuration{TunnelId: "tun-1", Ingress: []cf.UnvalidatedIngressRule{{Service: "http_status:404"}}}
	configBytes, _ := yaml.Marshal(existingConfig)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tunnel", Namespace: "default"},
		Data:       map[string]string{configmapKey: string(configBytes)},
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tunnel", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "cloudflared"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"app": "cloudflared"},
					Annotations: map[string]string{"existing-annotation": "value"},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "cloudflared", Image: "cloudflare/cloudflared"}}},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(cm, deploy).Build()

	r := &TunnelBindingReconciler{
		Client:    fakeClient,
		Scheme:    s,
		Recorder:  record.NewFakeRecorder(10),
		ctx:       context.Background(),
		log:       ctrllog.Log,
		configmap: cm,
		binding: &networkingv1alpha1.TunnelBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "binding-1", Namespace: "default"},
			TunnelRef:  networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		},
	}

	newConfig := &cf.Configuration{
		TunnelId: "tun-1",
		Ingress: []cf.UnvalidatedIngressRule{
			{Hostname: "app.example.com", Service: "http://app.default.svc:80"},
			{Service: "http_status:404"},
		},
	}

	if err := r.setConfigMapConfiguration(newConfig); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify ConfigMap data updated
	updatedCM := &corev1.ConfigMap{}
	if err := fakeClient.Get(context.Background(), apitypes.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedCM); err != nil {
		t.Fatalf("failed to get updated configmap: %v", err)
	}

	updatedConfigStr := updatedCM.Data[configmapKey]
	var parsedConfig cf.Configuration
	if err := yaml.Unmarshal([]byte(updatedConfigStr), &parsedConfig); err != nil {
		t.Fatalf("failed to parse config yaml: %v", err)
	}
	if len(parsedConfig.Ingress) != 2 {
		t.Fatalf("expected 2 ingress rules in configmap, got %d", len(parsedConfig.Ingress))
	}

	// Verify Deployment checksum annotation
	updatedDeploy := &appsv1.Deployment{}
	if err := fakeClient.Get(context.Background(), apitypes.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedDeploy); err != nil {
		t.Fatalf("failed to get updated deployment: %v", err)
	}

	checksum, ok := updatedDeploy.Spec.Template.Annotations[tunnelConfigChecksum]
	if !ok {
		t.Fatal("expected checksum annotation on pod template, not found")
	}

	expectedHash := md5.Sum([]byte(updatedConfigStr))
	expectedChecksum := hex.EncodeToString(expectedHash[:])
	if checksum != expectedChecksum {
		t.Errorf("expected checksum=%q, got %q", expectedChecksum, checksum)
	}

	// Verify existing annotation preserved
	if updatedDeploy.Spec.Template.Annotations["existing-annotation"] != "value" {
		t.Errorf("expected existing annotation preserved, got %q", updatedDeploy.Spec.Template.Annotations["existing-annotation"])
	}
}

// helperMockSetup creates a standard mock CF server with account, zone, tunnel pre-configured,
// and returns the server + a cf.API pointed at it.
func helperMockSetup(t *testing.T) (*cfmock.Server, *cf.API) {
	t.Helper()
	mockServer := cfmock.NewServer()
	mockServer.AddAccount("acct-1", "my-account")
	mockServer.AddZone("zone-1", "example.com")
	mockServer.AddTunnel("acct-1", "tun-1", "my-tunnel")

	cfAPI := newTestCfAPI(t, mockServer)
	cfAPI.ValidAccountId = "acct-1"
	cfAPI.ValidTunnelId = "tun-1"
	cfAPI.ValidTunnelName = "my-tunnel"
	cfAPI.ValidZoneId = "zone-1"
	cfAPI.Domain = "example.com"
	cfAPI.TunnelName = "my-tunnel"
	cfAPI.TunnelId = "tun-1"
	return mockServer, cfAPI
}

func TestCreationLogic(t *testing.T) {
	s := newScheme(t)
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "default",
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{Kind: "Service", Name: "svc-a"},
			{Kind: "Service", Name: "svc-b"},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "svc-a.example.com", Target: "http://svc-a.default.svc:80"},
				{Hostname: "svc-b.example.com", Target: "http://svc-b.default.svc:80"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(binding).Build()

	r := &TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(20),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	if err := r.creationLogic(); err != nil {
		t.Fatalf("creationLogic() returned error: %v", err)
	}

	// Re-read binding from fake client
	updated := &networkingv1alpha1.TunnelBinding{}
	if err := fakeClient.Get(context.Background(), apitypes.NamespacedName{Name: "test-binding", Namespace: "default"}, updated); err != nil {
		t.Fatalf("failed to get updated binding: %v", err)
	}

	// Assert labels set
	if updated.Labels[tunnelNameLabel] != "my-tunnel" {
		t.Errorf("expected label %s=%q, got %q", tunnelNameLabel, "my-tunnel", updated.Labels[tunnelNameLabel])
	}
	if updated.Labels[tunnelKindLabel] != "Tunnel" {
		t.Errorf("expected label %s=%q, got %q", tunnelKindLabel, "Tunnel", updated.Labels[tunnelKindLabel])
	}

	// Assert finalizer added
	hasFinalizer := false
	for _, f := range updated.Finalizers {
		if f == tunnelFinalizer {
			hasFinalizer = true
			break
		}
	}
	if !hasFinalizer {
		t.Errorf("expected finalizer %q to be present on binding", tunnelFinalizer)
	}

	// Assert DNS creation calls for both hostnames
	calls := mockServer.GetCalls()
	dnsCalls := 0
	for _, c := range calls {
		if c.Method == "POST" && strings.Contains(c.Path, "dns_records") {
			dnsCalls++
		}
	}
	// 2 hostnames * 2 records each (CNAME + TXT) = 4 POST calls
	if dnsCalls != 4 {
		t.Errorf("expected 4 DNS creation POST calls (CNAME+TXT for each hostname), got %d", dnsCalls)
	}
}

func TestCreationLogic_DisableDNSUpdates(t *testing.T) {
	s := newScheme(t)
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "default",
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{Kind: "Service", Name: "svc-a"},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{
			Kind:              "Tunnel",
			Name:              "my-tunnel",
			DisableDNSUpdates: true,
		},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "svc-a.example.com", Target: "http://svc-a.default.svc:80"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(binding).Build()

	r := &TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(20),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	if err := r.creationLogic(); err != nil {
		t.Fatalf("creationLogic() returned error: %v", err)
	}

	// Re-read binding
	updated := &networkingv1alpha1.TunnelBinding{}
	if err := fakeClient.Get(context.Background(), apitypes.NamespacedName{Name: "test-binding", Namespace: "default"}, updated); err != nil {
		t.Fatalf("failed to get updated binding: %v", err)
	}

	// Labels should still be set
	if updated.Labels[tunnelNameLabel] != "my-tunnel" {
		t.Errorf("expected label %s=%q, got %q", tunnelNameLabel, "my-tunnel", updated.Labels[tunnelNameLabel])
	}

	// Finalizer should NOT be added
	for _, f := range updated.Finalizers {
		if f == tunnelFinalizer {
			t.Errorf("expected no finalizer when DisableDNSUpdates=true, but found %q", tunnelFinalizer)
		}
	}

	// No DNS API calls should have been made (only the ones from ValidateAll for zone/account/tunnel lookups are okay)
	dnsCalls := mockServer.GetCallsByPathContains("dns_records")
	if len(dnsCalls) != 0 {
		t.Errorf("expected 0 DNS record API calls when DisableDNSUpdates=true, got %d", len(dnsCalls))
	}
}

func TestDeletionLogic(t *testing.T) {
	s := newScheme(t)
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-binding",
			Namespace:  "default",
			Finalizers: []string{tunnelFinalizer},
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{Kind: "Service", Name: "svc-a"},
			{Kind: "Service", Name: "svc-b"},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "svc-a.example.com", Target: "http://svc-a.default.svc:80"},
				{Hostname: "svc-b.example.com", Target: "http://svc-b.default.svc:80"},
			},
		},
	}

	// Pre-create matching TXT and CNAME records for both hostnames
	managedTxt := fmt.Sprintf(`{"DnsId":"cname-a","TunnelName":"my-tunnel","TunnelId":"tun-1"}`)
	managedTxtB := fmt.Sprintf(`{"DnsId":"cname-b","TunnelName":"my-tunnel","TunnelId":"tun-1"}`)
	proxied := true
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "txt-a", ZoneID: "zone-1", Type: "TXT",
		Name: "_managed.svc-a.example.com", Content: managedTxt,
	})
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "cname-a", ZoneID: "zone-1", Type: "CNAME",
		Name: "svc-a.example.com", Content: "tun-1.cfargotunnel.com", Proxied: &proxied,
	})
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "txt-b", ZoneID: "zone-1", Type: "TXT",
		Name: "_managed.svc-b.example.com", Content: managedTxtB,
	})
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "cname-b", ZoneID: "zone-1", Type: "CNAME",
		Name: "svc-b.example.com", Content: "tun-1.cfargotunnel.com", Proxied: &proxied,
	})

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(binding).Build()

	r := &TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(20),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	if err := r.deletionLogic(); err != nil {
		t.Fatalf("deletionLogic() returned error: %v", err)
	}

	// Assert DNS delete calls made (CNAME + TXT for each hostname = 4 DELETEs)
	deleteCalls := mockServer.GetCallsByMethod("DELETE")
	dnsDeleteCalls := 0
	for _, c := range deleteCalls {
		if strings.Contains(c.Path, "dns_records") {
			dnsDeleteCalls++
		}
	}
	if dnsDeleteCalls != 4 {
		t.Errorf("expected 4 DNS DELETE calls, got %d", dnsDeleteCalls)
	}

	// Assert finalizer removed
	updated := &networkingv1alpha1.TunnelBinding{}
	if err := fakeClient.Get(context.Background(), apitypes.NamespacedName{Name: "test-binding", Namespace: "default"}, updated); err != nil {
		t.Fatalf("failed to get updated binding: %v", err)
	}
	for _, f := range updated.Finalizers {
		if f == tunnelFinalizer {
			t.Errorf("expected finalizer to be removed, but it is still present")
		}
	}
}

func TestDeletionLogic_DNSFailure(t *testing.T) {
	s := newScheme(t)
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-binding",
			Namespace:  "default",
			Finalizers: []string{tunnelFinalizer},
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{Kind: "Service", Name: "svc-a"},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "svc-a.example.com", Target: "http://svc-a.default.svc:80"},
			},
		},
	}

	// Pre-create matching TXT and CNAME
	managedTxt := `{"DnsId":"cname-a","TunnelName":"my-tunnel","TunnelId":"tun-1"}`
	proxied := true
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "txt-a", ZoneID: "zone-1", Type: "TXT",
		Name: "_managed.svc-a.example.com", Content: managedTxt,
	})
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "cname-a", ZoneID: "zone-1", Type: "CNAME",
		Name: "svc-a.example.com", Content: "tun-1.cfargotunnel.com", Proxied: &proxied,
	})

	// Make DELETE on dns_records fail
	mockServer.SetError("DELETE", "/client/v4/zones/zone-1/dns_records", 500, "server error")

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(binding).Build()

	r := &TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(20),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	err := r.deletionLogic()
	if err == nil {
		t.Fatal("expected error from deletionLogic() when DNS delete fails, got nil")
	}

	// Finalizer should NOT be removed (so it can retry)
	updated := &networkingv1alpha1.TunnelBinding{}
	if err := fakeClient.Get(context.Background(), apitypes.NamespacedName{Name: "test-binding", Namespace: "default"}, updated); err != nil {
		t.Fatalf("failed to get updated binding: %v", err)
	}
	hasFinalizer := false
	for _, f := range updated.Finalizers {
		if f == tunnelFinalizer {
			hasFinalizer = true
		}
	}
	if !hasFinalizer {
		t.Error("expected finalizer to still be present after DNS failure")
	}
}

func TestCreateDNSLogic(t *testing.T) {
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-binding", Namespace: "default"},
		TunnelRef:  networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
	}

	r := &TunnelBindingReconciler{
		Recorder: record.NewFakeRecorder(20),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	if err := r.createDNSLogic("app.example.com"); err != nil {
		t.Fatalf("createDNSLogic() returned error: %v", err)
	}

	// Verify CNAME and TXT creation calls
	postCalls := mockServer.GetCallsByMethod("POST")
	cnameCreated := false
	txtCreated := false
	for _, c := range postCalls {
		if strings.Contains(c.Path, "dns_records") {
			if strings.Contains(c.Body, `"type":"CNAME"`) && strings.Contains(c.Body, "app.example.com") {
				cnameCreated = true
			}
			if strings.Contains(c.Body, `"type":"TXT"`) && strings.Contains(c.Body, "_managed.app.example.com") {
				txtCreated = true
			}
		}
	}
	if !cnameCreated {
		t.Error("expected CNAME record to be created for app.example.com")
	}
	if !txtCreated {
		t.Error("expected TXT record to be created for _managed.app.example.com")
	}
}

func TestCreateDNSLogic_ExistingManaged(t *testing.T) {
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	// Pre-create existing TXT and CNAME records (same tunnel owns them)
	managedTxt := `{"DnsId":"existing-cname-id","TunnelName":"my-tunnel","TunnelId":"tun-1"}`
	proxied := true
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "existing-txt-id", ZoneID: "zone-1", Type: "TXT",
		Name: "_managed.app.example.com", Content: managedTxt,
	})
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "existing-cname-id", ZoneID: "zone-1", Type: "CNAME",
		Name: "app.example.com", Content: "tun-1.cfargotunnel.com", Proxied: &proxied,
	})

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-binding", Namespace: "default"},
		TunnelRef:  networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
	}

	r := &TunnelBindingReconciler{
		Recorder: record.NewFakeRecorder(20),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	if err := r.createDNSLogic("app.example.com"); err != nil {
		t.Fatalf("createDNSLogic() returned error: %v", err)
	}

	// Should use PATCH (update) for both CNAME and TXT, not POST (create)
	patchCalls := mockServer.GetCallsByMethod("PATCH")
	patchDNSCalls := 0
	for _, c := range patchCalls {
		if strings.Contains(c.Path, "dns_records") {
			patchDNSCalls++
		}
	}
	if patchDNSCalls != 2 {
		t.Errorf("expected 2 PATCH DNS calls (update CNAME + update TXT), got %d", patchDNSCalls)
	}

	// Should have no POST dns_records calls
	postCalls := mockServer.GetCallsByMethod("POST")
	postDNSCalls := 0
	for _, c := range postCalls {
		if strings.Contains(c.Path, "dns_records") {
			postDNSCalls++
		}
	}
	if postDNSCalls != 0 {
		t.Errorf("expected 0 POST DNS calls when records already exist, got %d", postDNSCalls)
	}
}

func TestDeleteDNSLogic(t *testing.T) {
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	// Pre-create matching TXT and CNAME
	managedTxt := `{"DnsId":"cname-id","TunnelName":"my-tunnel","TunnelId":"tun-1"}`
	proxied := true
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "txt-id", ZoneID: "zone-1", Type: "TXT",
		Name: "_managed.app.example.com", Content: managedTxt,
	})
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "cname-id", ZoneID: "zone-1", Type: "CNAME",
		Name: "app.example.com", Content: "tun-1.cfargotunnel.com", Proxied: &proxied,
	})

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-binding", Namespace: "default"},
		TunnelRef:  networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
	}

	r := &TunnelBindingReconciler{
		Recorder: record.NewFakeRecorder(20),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	if err := r.deleteDNSLogic("app.example.com"); err != nil {
		t.Fatalf("deleteDNSLogic() returned error: %v", err)
	}

	// Should have 2 DELETE calls (CNAME + TXT)
	deleteCalls := mockServer.GetCallsByMethod("DELETE")
	dnsDeleteCalls := 0
	for _, c := range deleteCalls {
		if strings.Contains(c.Path, "dns_records") {
			dnsDeleteCalls++
		}
	}
	if dnsDeleteCalls != 2 {
		t.Errorf("expected 2 DNS DELETE calls (CNAME + TXT), got %d", dnsDeleteCalls)
	}
}

func TestDeleteDNSLogic_DifferentTunnel(t *testing.T) {
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	// TXT record exists but belongs to a different tunnel
	managedTxt := `{"DnsId":"cname-id","TunnelName":"other-tunnel","TunnelId":"other-tun-id"}`
	mockServer.AddDNSRecord(cfmock.DNSRecord{
		ID: "txt-id", ZoneID: "zone-1", Type: "TXT",
		Name: "_managed.app.example.com", Content: managedTxt,
	})

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-binding", Namespace: "default"},
		TunnelRef:  networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
	}

	r := &TunnelBindingReconciler{
		Recorder: record.NewFakeRecorder(20),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	err := r.deleteDNSLogic("app.example.com")
	if err != nil {
		t.Fatalf("expected nil error when TXT belongs to different tunnel, got: %v", err)
	}

	// No deletions should have been attempted
	deleteCalls := mockServer.GetCallsByMethod("DELETE")
	dnsDeleteCalls := 0
	for _, c := range deleteCalls {
		if strings.Contains(c.Path, "dns_records") {
			dnsDeleteCalls++
		}
	}
	if dnsDeleteCalls != 0 {
		t.Errorf("expected 0 DNS DELETE calls when TXT belongs to different tunnel, got %d", dnsDeleteCalls)
	}
}

func TestGetConfigForSubject(t *testing.T) {
	s := newScheme(t)
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-svc", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Port: 80, Protocol: corev1.ProtocolTCP},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(svc).Build()

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-binding", Namespace: "default"},
		TunnelRef:  networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
	}

	r := TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(10),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	subject := networkingv1alpha1.TunnelBindingSubject{
		Kind: "Service",
		Name: "my-svc",
	}

	hostname, target, err := r.getConfigForSubject(subject)
	if err != nil {
		t.Fatalf("getConfigForSubject() returned error: %v", err)
	}

	expectedHostname := "my-svc.example.com"
	if hostname != expectedHostname {
		t.Errorf("expected hostname=%q, got %q", expectedHostname, hostname)
	}

	expectedTarget := "http://my-svc.default.svc:80"
	if target != expectedTarget {
		t.Errorf("expected target=%q, got %q", expectedTarget, target)
	}
}

func TestGetConfigForSubject_CustomFQDN(t *testing.T) {
	s := newScheme(t)
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-svc", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Port: 8080, Protocol: corev1.ProtocolTCP},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(svc).Build()

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-binding", Namespace: "default"},
		TunnelRef:  networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
	}

	r := TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(10),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	subject := networkingv1alpha1.TunnelBindingSubject{
		Kind: "Service",
		Name: "my-svc",
		Spec: networkingv1alpha1.TunnelBindingSubjectSpec{
			Fqdn: "custom.example.com",
		},
	}

	hostname, _, err := r.getConfigForSubject(subject)
	if err != nil {
		t.Fatalf("getConfigForSubject() returned error: %v", err)
	}

	if hostname != "custom.example.com" {
		t.Errorf("expected hostname=%q, got %q", "custom.example.com", hostname)
	}
}

func TestGetConfigForSubject_ServiceNotFound(t *testing.T) {
	s := newScheme(t)
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	// No service created in the fake client
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-binding", Namespace: "default"},
		TunnelRef:  networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
	}

	r := TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(10),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	subject := networkingv1alpha1.TunnelBindingSubject{
		Kind: "Service",
		Name: "nonexistent-svc",
	}

	hostname, target, err := r.getConfigForSubject(subject)
	if err == nil {
		t.Fatal("expected error when service not found, got nil")
	}

	// Hostname should still be generated
	expectedHostname := "nonexistent-svc.example.com"
	if hostname != expectedHostname {
		t.Errorf("expected hostname=%q even on error, got %q", expectedHostname, hostname)
	}

	// Target should be the fallback
	if target != "http_status:404" {
		t.Errorf("expected target=%q when service not found, got %q", "http_status:404", target)
	}
}

func TestSetStatus(t *testing.T) {
	s := newScheme(t)
	mockServer, cfAPI := helperMockSetup(t)
	defer mockServer.Close()

	svcA := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-a", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Port: 80, Protocol: corev1.ProtocolTCP},
			},
		},
	}
	svcB := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-b", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Port: 443, Protocol: corev1.ProtocolTCP},
			},
		},
	}

	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-binding", Namespace: "default"},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{Kind: "Service", Name: "svc-a"},
			{Kind: "Service", Name: "svc-b"},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{Kind: "Tunnel", Name: "my-tunnel"},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&networkingv1alpha1.TunnelBinding{}).
		WithRuntimeObjects(binding, svcA, svcB).Build()

	r := &TunnelBindingReconciler{
		Client:   fakeClient,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(20),
		ctx:      context.Background(),
		log:      ctrllog.Log,
		cfAPI:    cfAPI,
		binding:  binding,
	}

	if err := r.setStatus(); err != nil {
		t.Fatalf("setStatus() returned error: %v", err)
	}

	// Re-read binding from fake client to verify status update persisted
	updated := &networkingv1alpha1.TunnelBinding{}
	if err := fakeClient.Get(context.Background(), apitypes.NamespacedName{Name: "test-binding", Namespace: "default"}, updated); err != nil {
		t.Fatalf("failed to get updated binding: %v", err)
	}

	if len(updated.Status.Services) != 2 {
		t.Fatalf("expected 2 services in status, got %d", len(updated.Status.Services))
	}

	if updated.Status.Services[0].Hostname != "svc-a.example.com" {
		t.Errorf("expected services[0].Hostname=%q, got %q", "svc-a.example.com", updated.Status.Services[0].Hostname)
	}
	if updated.Status.Services[0].Target != "http://svc-a.default.svc:80" {
		t.Errorf("expected services[0].Target=%q, got %q", "http://svc-a.default.svc:80", updated.Status.Services[0].Target)
	}
	if updated.Status.Services[1].Hostname != "svc-b.example.com" {
		t.Errorf("expected services[1].Hostname=%q, got %q", "svc-b.example.com", updated.Status.Services[1].Hostname)
	}
	if updated.Status.Services[1].Target != "https://svc-b.default.svc:443" {
		t.Errorf("expected services[1].Target=%q, got %q", "https://svc-b.default.svc:443", updated.Status.Services[1].Target)
	}

	expectedHostnames := "svc-a.example.com,svc-b.example.com"
	if updated.Status.Hostnames != expectedHostnames {
		t.Errorf("expected Hostnames=%q, got %q", expectedHostnames, updated.Status.Hostnames)
	}
}
