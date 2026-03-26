package controller

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	networkingv1alpha1 "github.com/adyanth/cloudflare-operator/api/v1alpha1"
	networkingv1alpha2 "github.com/adyanth/cloudflare-operator/api/v1alpha2"
	"github.com/adyanth/cloudflare-operator/internal/clients/cf"
	"github.com/cloudflare/cloudflare-go"
	"github.com/go-logr/logr"
	logrtesting "github.com/go-logr/logr/testr"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// testReconciler implements GenericTunnelReconciler for unit testing.
type testReconciler struct {
	client      client.Client
	scheme      *runtime.Scheme
	recorder    record.EventRecorder
	ctx         context.Context
	log         logr.Logger
	tunnel      Tunnel
	cfAPI       *cf.API
	cfSecret    *corev1.Secret
	tunnelCreds string
}

func (r *testReconciler) GetClient() client.Client            { return r.client }
func (r *testReconciler) GetRecorder() record.EventRecorder    { return r.recorder }
func (r *testReconciler) GetScheme() *runtime.Scheme           { return r.scheme }
func (r *testReconciler) GetContext() context.Context           { return r.ctx }
func (r *testReconciler) GetLog() logr.Logger                  { return r.log }
func (r *testReconciler) GetTunnel() Tunnel                    { return r.tunnel }
func (r *testReconciler) GetCfAPI() *cf.API                    { return r.cfAPI }
func (r *testReconciler) SetCfAPI(in *cf.API)                  { r.cfAPI = in }
func (r *testReconciler) GetCfSecret() *corev1.Secret          { return r.cfSecret }
func (r *testReconciler) GetTunnelCreds() string               { return r.tunnelCreds }
func (r *testReconciler) SetTunnelCreds(in string)             { r.tunnelCreds = in }
func (r *testReconciler) GetReconciledObject() client.Object   { return r.tunnel.GetObject() }
func (r *testReconciler) GetReconcilerName() string            { return "TestTunnel" }

var _ GenericTunnelReconciler = &testReconciler{}

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = networkingv1alpha1.AddToScheme(s)
	_ = networkingv1alpha2.AddToScheme(s)
	return s
}

func newTestTunnelObj(name, namespace string) *networkingv1alpha2.Tunnel {
	return &networkingv1alpha2.Tunnel{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.cfargotunnel.com/v1alpha2",
			Kind:       "Tunnel",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("test-uid-1234"),
		},
		Spec: networkingv1alpha2.TunnelSpec{
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:      "example.com",
				Secret:      "cf-secret",
				AccountId:   "acc-123",
				AccountName: "test-account",
			},
			FallbackTarget: "http_status:404",
			Protocol:       "auto",
			NoTlsVerify:    false,
		},
		Status: networkingv1alpha2.TunnelStatus{
			TunnelId:   "tun-456",
			TunnelName: "test-tunnel",
			AccountId:  "acc-123",
			ZoneId:     "zone-789",
		},
	}
}

func newTestReconcilerForTunnel(t *testing.T, tunnel *networkingv1alpha2.Tunnel, objs ...client.Object) *testReconciler {
	t.Helper()
	scheme := newTestScheme()
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	fakeClient := builder.Build()
	return &testReconciler{
		client:      fakeClient,
		scheme:      scheme,
		recorder:    record.NewFakeRecorder(100),
		ctx:         context.Background(),
		log:         logrtesting.New(t),
		tunnel:      TunnelAdapter{Tunnel: tunnel},
		cfAPI:       nil,
		cfSecret:    nil,
		tunnelCreds: `{"AccountTag":"acc-123","TunnelID":"tun-456","TunnelName":"test-tunnel","TunnelSecret":"dGVzdC1zZWNyZXQ="}`,
	}
}

func TestConfigMapForTunnel(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")
	r := newTestReconcilerForTunnel(t, tunnel)

	cm := configMapForTunnel(r)

	// Name, namespace
	if cm.Name != "my-tunnel" {
		t.Errorf("ConfigMap name = %q, want %q", cm.Name, "my-tunnel")
	}
	if cm.Namespace != "default" {
		t.Errorf("ConfigMap namespace = %q, want %q", cm.Namespace, "default")
	}

	// Labels
	labels := cm.Labels
	if labels[tunnelLabel] != "my-tunnel" {
		t.Errorf("label %s = %q, want %q", tunnelLabel, labels[tunnelLabel], "my-tunnel")
	}
	if labels[tunnelAppLabel] != "cloudflared" {
		t.Errorf("label %s = %q, want %q", tunnelAppLabel, labels[tunnelAppLabel], "cloudflared")
	}
	if labels[tunnelIdLabel] != "tun-456" {
		t.Errorf("label %s = %q, want %q", tunnelIdLabel, labels[tunnelIdLabel], "tun-456")
	}
	if labels[tunnelNameLabel] != "test-tunnel" {
		t.Errorf("label %s = %q, want %q", tunnelNameLabel, labels[tunnelNameLabel], "test-tunnel")
	}
	if labels[tunnelDomainLabel] != "example.com" {
		t.Errorf("label %s = %q, want %q", tunnelDomainLabel, labels[tunnelDomainLabel], "example.com")
	}
	if labels[isClusterTunnelLabel] != "false" {
		t.Errorf("label %s = %q, want %q", isClusterTunnelLabel, labels[isClusterTunnelLabel], "false")
	}

	// Parse config.yaml content
	configYAML, ok := cm.Data[configmapKey]
	if !ok {
		t.Fatal("ConfigMap missing config.yaml key")
	}
	config := &cf.Configuration{}
	if err := yaml.Unmarshal([]byte(configYAML), config); err != nil {
		t.Fatalf("failed to parse config.yaml: %v", err)
	}

	if config.TunnelId != "tun-456" {
		t.Errorf("config tunnel = %q, want %q", config.TunnelId, "tun-456")
	}
	if config.SourceFile != "/etc/cloudflared/creds/credentials.json" {
		t.Errorf("config credentials-file = %q, want %q", config.SourceFile, "/etc/cloudflared/creds/credentials.json")
	}
	if config.Metrics != "0.0.0.0:2000" {
		t.Errorf("config metrics = %q, want %q", config.Metrics, "0.0.0.0:2000")
	}
	if !config.NoAutoUpdate {
		t.Error("config no-autoupdate should be true")
	}

	// Fallback ingress rule
	if len(config.Ingress) != 1 {
		t.Fatalf("expected 1 ingress rule, got %d", len(config.Ingress))
	}
	if config.Ingress[0].Service != "http_status:404" {
		t.Errorf("fallback service = %q, want %q", config.Ingress[0].Service, "http_status:404")
	}

	// originRequest.noTLSVerify from tunnel spec (false)
	if config.OriginRequest.NoTLSVerify == nil {
		t.Fatal("originRequest.noTLSVerify should not be nil")
	}
	if *config.OriginRequest.NoTLSVerify != false {
		t.Errorf("originRequest.noTLSVerify = %v, want false", *config.OriginRequest.NoTLSVerify)
	}

	// originRequest.caPool should be nil when OriginCaPool is empty
	if config.OriginRequest.CAPool != nil {
		t.Errorf("originRequest.caPool should be nil when OriginCaPool is empty, got %v", *config.OriginRequest.CAPool)
	}
}

func TestConfigMapForTunnel_WithOriginCaPool(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")
	tunnel.Spec.OriginCaPool = "my-ca-secret"
	r := newTestReconcilerForTunnel(t, tunnel)

	cm := configMapForTunnel(r)

	configYAML := cm.Data[configmapKey]
	config := &cf.Configuration{}
	if err := yaml.Unmarshal([]byte(configYAML), config); err != nil {
		t.Fatalf("failed to parse config.yaml: %v", err)
	}

	if config.OriginRequest.CAPool == nil {
		t.Fatal("originRequest.caPool should not be nil when OriginCaPool is set")
	}
	if *config.OriginRequest.CAPool != "/etc/cloudflared/certs/tls.crt" {
		t.Errorf("originRequest.caPool = %q, want %q", *config.OriginRequest.CAPool, "/etc/cloudflared/certs/tls.crt")
	}
}

func TestSecretForTunnel(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")
	r := newTestReconcilerForTunnel(t, tunnel)

	sec := secretForTunnel(r)

	if sec.Name != "my-tunnel" {
		t.Errorf("Secret name = %q, want %q", sec.Name, "my-tunnel")
	}
	if sec.Namespace != "default" {
		t.Errorf("Secret namespace = %q, want %q", sec.Namespace, "default")
	}

	// Labels
	if sec.Labels[tunnelLabel] != "my-tunnel" {
		t.Errorf("label %s = %q, want %q", tunnelLabel, sec.Labels[tunnelLabel], "my-tunnel")
	}
	if sec.Labels[tunnelAppLabel] != "cloudflared" {
		t.Errorf("label %s = %q, want %q", tunnelAppLabel, sec.Labels[tunnelAppLabel], "cloudflared")
	}

	// Credentials data
	creds, ok := sec.StringData[CredentialsJsonFilename]
	if !ok {
		t.Fatal("Secret missing credentials.json key")
	}
	if creds != r.tunnelCreds {
		t.Errorf("credentials.json = %q, want %q", creds, r.tunnelCreds)
	}
}

func TestDeploymentForTunnel(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")
	r := newTestReconcilerForTunnel(t, tunnel)

	cm := configMapForTunnel(r)
	configStr := cm.Data[configmapKey]
	dep := deploymentForTunnel(r, configStr)

	// Name, namespace, labels
	if dep.Name != "my-tunnel" {
		t.Errorf("Deployment name = %q, want %q", dep.Name, "my-tunnel")
	}
	if dep.Namespace != "default" {
		t.Errorf("Deployment namespace = %q, want %q", dep.Namespace, "default")
	}
	if dep.Labels[tunnelLabel] != "my-tunnel" {
		t.Errorf("label %s = %q, want %q", tunnelLabel, dep.Labels[tunnelLabel], "my-tunnel")
	}

	// Selector
	if dep.Spec.Selector == nil {
		t.Fatal("Deployment selector is nil")
	}
	if dep.Spec.Selector.MatchLabels[tunnelLabel] != "my-tunnel" {
		t.Errorf("selector label %s = %q, want %q", tunnelLabel, dep.Spec.Selector.MatchLabels[tunnelLabel], "my-tunnel")
	}

	// Container image
	containers := dep.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	container := containers[0]
	if container.Image != CloudflaredLatestImage {
		t.Errorf("container image = %q, want %q", container.Image, CloudflaredLatestImage)
	}

	// Args
	expectedArgs := []string{"tunnel", "--protocol", "auto", "--config", "/etc/cloudflared/config/config.yaml", "--metrics", "0.0.0.0:2000", "run"}
	if len(container.Args) != len(expectedArgs) {
		t.Fatalf("container args length = %d, want %d: %v", len(container.Args), len(expectedArgs), container.Args)
	}
	for i, arg := range expectedArgs {
		if container.Args[i] != arg {
			t.Errorf("container args[%d] = %q, want %q", i, container.Args[i], arg)
		}
	}

	// Volumes: creds and config
	volumes := dep.Spec.Template.Spec.Volumes
	if len(volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(volumes))
	}
	foundCreds, foundConfig := false, false
	for _, v := range volumes {
		switch v.Name {
		case "creds":
			foundCreds = true
			if v.VolumeSource.Secret == nil || v.VolumeSource.Secret.SecretName != "my-tunnel" {
				t.Errorf("creds volume secret name = %v, want my-tunnel", v.VolumeSource.Secret)
			}
		case "config":
			foundConfig = true
			if v.VolumeSource.ConfigMap == nil || v.VolumeSource.ConfigMap.Name != "my-tunnel" {
				t.Errorf("config volume configmap name = %v, want my-tunnel", v.VolumeSource.ConfigMap)
			}
		}
	}
	if !foundCreds {
		t.Error("missing 'creds' volume")
	}
	if !foundConfig {
		t.Error("missing 'config' volume")
	}

	// VolumeMounts
	if len(container.VolumeMounts) != 2 {
		t.Fatalf("expected 2 volume mounts, got %d", len(container.VolumeMounts))
	}
	foundConfigMount, foundCredsMount := false, false
	for _, vm := range container.VolumeMounts {
		switch vm.Name {
		case "config":
			foundConfigMount = true
			if vm.MountPath != "/etc/cloudflared/config" {
				t.Errorf("config mount path = %q, want /etc/cloudflared/config", vm.MountPath)
			}
			if !vm.ReadOnly {
				t.Error("config mount should be read-only")
			}
		case "creds":
			foundCredsMount = true
			if vm.MountPath != "/etc/cloudflared/creds" {
				t.Errorf("creds mount path = %q, want /etc/cloudflared/creds", vm.MountPath)
			}
			if !vm.ReadOnly {
				t.Error("creds mount should be read-only")
			}
		}
	}
	if !foundConfigMount {
		t.Error("missing 'config' volume mount")
	}
	if !foundCredsMount {
		t.Error("missing 'creds' volume mount")
	}

	// SecurityContext
	sc := container.SecurityContext
	if sc == nil {
		t.Fatal("container security context is nil")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation should be false")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("ReadOnlyRootFilesystem should be true")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 1002 {
		t.Errorf("RunAsUser = %v, want 1002", sc.RunAsUser)
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Capabilities.Drop should be [ALL], got %v", sc.Capabilities)
	}

	// PodSecurityContext
	psc := dep.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("pod security context is nil")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("RunAsNonRoot should be true")
	}
	if psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("SeccompProfile type = %v, want RuntimeDefault", psc.SeccompProfile)
	}

	// LivenessProbe
	probe := container.LivenessProbe
	if probe == nil {
		t.Fatal("liveness probe is nil")
	}
	if probe.HTTPGet == nil {
		t.Fatal("liveness probe HTTPGet is nil")
	}
	if probe.HTTPGet.Path != "/ready" {
		t.Errorf("probe path = %q, want /ready", probe.HTTPGet.Path)
	}
	if probe.HTTPGet.Port.IntVal != 2000 {
		t.Errorf("probe port = %d, want 2000", probe.HTTPGet.Port.IntVal)
	}

	// Metrics port
	if len(container.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(container.Ports))
	}
	if container.Ports[0].ContainerPort != 2000 {
		t.Errorf("metrics port = %d, want 2000", container.Ports[0].ContainerPort)
	}
	if container.Ports[0].Name != "metrics" {
		t.Errorf("port name = %q, want metrics", container.Ports[0].Name)
	}

	// Node affinity
	affinity := dep.Spec.Template.Spec.Affinity
	if affinity == nil || affinity.NodeAffinity == nil {
		t.Fatal("node affinity is nil")
	}
	req := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if req == nil || len(req.NodeSelectorTerms) != 1 {
		t.Fatal("expected 1 node selector term")
	}
	exprs := req.NodeSelectorTerms[0].MatchExpressions
	if len(exprs) != 2 {
		t.Fatalf("expected 2 match expressions, got %d", len(exprs))
	}
	foundArch, foundOS := false, false
	for _, expr := range exprs {
		switch expr.Key {
		case "kubernetes.io/arch":
			foundArch = true
			if len(expr.Values) != 2 || expr.Values[0] != "amd64" || expr.Values[1] != "arm64" {
				t.Errorf("arch values = %v, want [amd64 arm64]", expr.Values)
			}
		case "kubernetes.io/os":
			foundOS = true
			if len(expr.Values) != 1 || expr.Values[0] != "linux" {
				t.Errorf("os values = %v, want [linux]", expr.Values)
			}
		}
	}
	if !foundArch {
		t.Error("missing kubernetes.io/arch expression")
	}
	if !foundOS {
		t.Error("missing kubernetes.io/os expression")
	}

	// Checksum annotation
	hash := md5.Sum([]byte(configStr))
	expectedChecksum := hex.EncodeToString(hash[:])
	annotations := dep.Spec.Template.Annotations
	if annotations[tunnelConfigChecksum] != expectedChecksum {
		t.Errorf("checksum annotation = %q, want %q", annotations[tunnelConfigChecksum], expectedChecksum)
	}
}

func TestDeploymentForTunnel_WithOriginCaPool(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")
	tunnel.Spec.OriginCaPool = "my-ca"
	r := newTestReconcilerForTunnel(t, tunnel)

	cm := configMapForTunnel(r)
	dep := deploymentForTunnel(r, cm.Data[configmapKey])

	// Extra volume "certs"
	volumes := dep.Spec.Template.Spec.Volumes
	if len(volumes) != 3 {
		t.Fatalf("expected 3 volumes (creds, config, certs), got %d", len(volumes))
	}
	foundCerts := false
	for _, v := range volumes {
		if v.Name == "certs" {
			foundCerts = true
			if v.VolumeSource.Secret == nil || v.VolumeSource.Secret.SecretName != "my-ca" {
				t.Errorf("certs volume secret name = %v, want my-ca", v.VolumeSource.Secret)
			}
		}
	}
	if !foundCerts {
		t.Error("missing 'certs' volume")
	}

	// Extra volumeMount at /etc/cloudflared/certs
	container := dep.Spec.Template.Spec.Containers[0]
	if len(container.VolumeMounts) != 3 {
		t.Fatalf("expected 3 volume mounts, got %d", len(container.VolumeMounts))
	}
	foundCertsMount := false
	for _, vm := range container.VolumeMounts {
		if vm.Name == "certs" {
			foundCertsMount = true
			if vm.MountPath != "/etc/cloudflared/certs" {
				t.Errorf("certs mount path = %q, want /etc/cloudflared/certs", vm.MountPath)
			}
			if !vm.ReadOnly {
				t.Error("certs mount should be read-only")
			}
		}
	}
	if !foundCertsMount {
		t.Error("missing 'certs' volume mount")
	}
}

// applyCapturingClient wraps a fake client.Client and converts server-side apply Patch calls
// into Create calls, since the fake client does not support server-side apply.
// It also captures all objects that were "applied".
type applyCapturingClient struct {
	client.Client
	applied []client.Object
}

func (c *applyCapturingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if patch == client.Apply {
		// Convert server-side apply to a Create-or-Update on the underlying fake client.
		existing := obj.DeepCopyObject().(client.Object)
		err := c.Client.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing)
		if err != nil {
			// Object doesn't exist — create it
			if createErr := c.Client.Create(ctx, obj); createErr != nil {
				return createErr
			}
		} else {
			// Object exists — update it
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

func TestCreateManagedResources(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")
	scheme := newTestScheme()

	// Don't pre-seed any objects. The applyCapturingClient intercepts server-side apply
	// and converts to Create. MergeOrApply for ConfigMap will fail Merge (not found) and
	// fall back to Apply, which our interceptor handles.
	innerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()
	fakeClient := &applyCapturingClient{Client: innerClient}

	r := &testReconciler{
		client:      fakeClient,
		scheme:      scheme,
		recorder:    record.NewFakeRecorder(100),
		ctx:         context.Background(),
		log:         logrtesting.New(t),
		tunnel:      TunnelAdapter{Tunnel: tunnel},
		tunnelCreds: `{"AccountTag":"acc-123","TunnelID":"tun-456","TunnelName":"test-tunnel","TunnelSecret":"dGVzdC1zZWNyZXQ="}`,
	}

	result, err := createManagedResources(r)
	if err != nil {
		t.Fatalf("createManagedResources returned error: %v", err)
	}
	if result.Requeue || result.RequeueAfter > 0 {
		t.Errorf("unexpected requeue: %v", result)
	}

	// Verify Secret created
	secret := &corev1.Secret{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, secret); err != nil {
		t.Fatalf("Secret not found: %v", err)
	}

	// Verify ConfigMap updated with config
	cm := &corev1.ConfigMap{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if _, ok := cm.Data[configmapKey]; !ok {
		t.Error("ConfigMap missing config.yaml key")
	}

	// Verify Deployment created
	dep := &appsv1.Deployment{}
	if err := innerClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, dep); err != nil {
		t.Fatalf("Deployment not found: %v", err)
	}
	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container in deployment, got %d", len(dep.Spec.Template.Spec.Containers))
	}
	if dep.Spec.Template.Spec.Containers[0].Image != CloudflaredLatestImage {
		t.Errorf("container image = %q, want %q", dep.Spec.Template.Spec.Containers[0].Image, CloudflaredLatestImage)
	}

	// Verify that 3 objects were applied via server-side apply (Secret + ConfigMap + Deployment).
	// ConfigMap goes through MergeOrApply: Merge fails (not found), then Apply is used.
	if len(fakeClient.applied) != 3 {
		t.Errorf("expected 3 server-side apply calls, got %d", len(fakeClient.applied))
	}
}

func TestRebuildTunnelConfig_Integration(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")

	// Create initial ConfigMap with just the catchall rule
	initialConfig := cf.Configuration{
		TunnelId:   "tun-456",
		SourceFile: "/etc/cloudflared/creds/credentials.json",
		Metrics:    "0.0.0.0:2000",
		Ingress: []cf.UnvalidatedIngressRule{
			{Service: "http_status:404"},
		},
	}
	initialConfigBytes, _ := yaml.Marshal(initialConfig)
	initialConfigStr := string(initialConfigBytes)

	initialHash := md5.Sum(initialConfigBytes)
	initialChecksum := hex.EncodeToString(initialHash[:])

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
		},
		Data: map[string]string{configmapKey: initialConfigStr},
	}

	// Create a Deployment for checksum updates
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{tunnelLabel: "my-tunnel"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{tunnelLabel: "my-tunnel"},
					Annotations: map[string]string{
						tunnelConfigChecksum: initialChecksum,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "cloudflared",
						Image: CloudflaredLatestImage,
					}},
				},
			},
		},
	}

	// Create 2 TunnelBindings with labels and status.Services
	noTls := false
	http2 := false
	binding1 := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "binding-alpha",
			Namespace: "default",
			Labels: map[string]string{
				tunnelNameLabel: "my-tunnel",
				tunnelKindLabel: "Tunnel",
			},
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{
				Kind: "Service",
				Name: "svc-a",
				Spec: networkingv1alpha1.TunnelBindingSubjectSpec{
					Fqdn:        "alpha.example.com",
					NoTlsVerify: noTls,
					Http2Origin: http2,
				},
			},
		},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "alpha.example.com", Target: "http://svc-a.default.svc:80"},
			},
		},
	}

	binding2 := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "binding-beta",
			Namespace: "default",
			Labels: map[string]string{
				tunnelNameLabel: "my-tunnel",
				tunnelKindLabel: "Tunnel",
			},
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{
				Kind: "Service",
				Name: "svc-b",
				Spec: networkingv1alpha1.TunnelBindingSubjectSpec{
					Fqdn:        "beta.example.com",
					NoTlsVerify: noTls,
					Http2Origin: http2,
				},
			},
		},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "beta.example.com", Target: "http://svc-b.default.svc:80"},
			},
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cm, dep, binding1, binding2).
		WithStatusSubresource(&networkingv1alpha1.TunnelBinding{}).
		Build()

	// Create mock CF server that accepts UpdateTunnelConfiguration
	handlers := map[string]http.HandlerFunc{
		"PUT /accounts/acc-123/cfd_tunnel/tun-456/configurations": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{}}`))
		},
		"GET /accounts/acc-123": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"acc-123","name":"test-account"}}`))
		},
		"GET /accounts/acc-123/cfd_tunnel/tun-456": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"tun-456","name":"test-tunnel"}}`))
		},
		"GET /zones": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[{"id":"zone-789","name":"example.com"}],"result_info":{"page":1,"per_page":50,"total_pages":1,"count":1,"total_count":1}}`))
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
	}

	if err := rebuildTunnelConfig(r); err != nil {
		t.Fatalf("rebuildTunnelConfig error: %v", err)
	}

	// Read updated ConfigMap
	updatedCM := &corev1.ConfigMap{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, updatedCM); err != nil {
		t.Fatalf("failed to get updated ConfigMap: %v", err)
	}

	config := &cf.Configuration{}
	if err := yaml.Unmarshal([]byte(updatedCM.Data[configmapKey]), config); err != nil {
		t.Fatalf("failed to parse updated config: %v", err)
	}

	// Expect 3 ingress rules: binding-alpha, binding-beta (sorted by name), + catchall
	if len(config.Ingress) != 3 {
		t.Fatalf("expected 3 ingress rules, got %d", len(config.Ingress))
	}

	// Sorted by binding name: alpha before beta
	if config.Ingress[0].Hostname != "alpha.example.com" {
		t.Errorf("ingress[0].hostname = %q, want alpha.example.com", config.Ingress[0].Hostname)
	}
	if config.Ingress[0].Service != "http://svc-a.default.svc:80" {
		t.Errorf("ingress[0].service = %q, want http://svc-a.default.svc:80", config.Ingress[0].Service)
	}
	if config.Ingress[1].Hostname != "beta.example.com" {
		t.Errorf("ingress[1].hostname = %q, want beta.example.com", config.Ingress[1].Hostname)
	}
	if config.Ingress[1].Service != "http://svc-b.default.svc:80" {
		t.Errorf("ingress[1].service = %q, want http://svc-b.default.svc:80", config.Ingress[1].Service)
	}
	// Catchall
	if config.Ingress[2].Service != "http_status:404" {
		t.Errorf("ingress[2] (catchall) service = %q, want http_status:404", config.Ingress[2].Service)
	}
	if config.Ingress[2].Hostname != "" {
		t.Errorf("catchall hostname should be empty, got %q", config.Ingress[2].Hostname)
	}
}

func TestRebuildTunnelConfig_NoChange(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")

	// Build the config that exactly matches what rebuildTunnelConfig would produce
	// with no bindings: just a catchall
	noTlsVerify := false
	rebuildConfig := cf.Configuration{
		TunnelId:   "tun-456",
		SourceFile: "/etc/cloudflared/creds/credentials.json",
		Metrics:    "0.0.0.0:2000",
		OriginRequest: cf.OriginRequestConfig{
			NoTLSVerify: &noTlsVerify,
		},
		Ingress: []cf.UnvalidatedIngressRule{
			{Service: "http_status:404"},
		},
	}
	configBytes, _ := yaml.Marshal(rebuildConfig)
	configStr := string(configBytes)

	hash := md5.Sum(configBytes)
	checksum := hex.EncodeToString(hash[:])

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "my-tunnel",
			Namespace:       "default",
			ResourceVersion: "100",
		},
		Data: map[string]string{configmapKey: configStr},
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tunnel",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{tunnelLabel: "my-tunnel"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{tunnelLabel: "my-tunnel"},
					Annotations: map[string]string{
						tunnelConfigChecksum: checksum,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "cloudflared",
						Image: CloudflaredLatestImage,
					}},
				},
			},
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm, dep).Build()

	// Mock CF API for UpdateTunnelConfiguration (should still be called for edge sync)
	handlers := map[string]http.HandlerFunc{
		"PUT /accounts/acc-123/cfd_tunnel/tun-456/configurations": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{}}`))
		},
		"GET /accounts/acc-123": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"acc-123","name":"test-account"}}`))
		},
		"GET /accounts/acc-123/cfd_tunnel/tun-456": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"tun-456","name":"test-tunnel"}}`))
		},
		"GET /zones": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[{"id":"zone-789","name":"example.com"}],"result_info":{"page":1,"per_page":50,"total_pages":1,"count":1,"total_count":1}}`))
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
	}

	// Record the ConfigMap resourceVersion before rebuild
	beforeCM := &corev1.ConfigMap{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, beforeCM); err != nil {
		t.Fatal(err)
	}
	beforeRV := beforeCM.ResourceVersion

	if err := rebuildTunnelConfig(r); err != nil {
		t.Fatalf("rebuildTunnelConfig error: %v", err)
	}

	// ConfigMap resourceVersion should be unchanged (no update)
	afterCM := &corev1.ConfigMap{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "my-tunnel", Namespace: "default"}, afterCM); err != nil {
		t.Fatal(err)
	}
	if afterCM.ResourceVersion != beforeRV {
		t.Errorf("ConfigMap was updated when config didn't change: resourceVersion went from %q to %q", beforeRV, afterCM.ResourceVersion)
	}
}

func TestCleanupDNSRecords_Integration(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")

	// Create TunnelBindings with hostnames in status
	binding1 := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "binding-1",
			Namespace: "default",
			Labels: map[string]string{
				tunnelNameLabel: "my-tunnel",
				tunnelKindLabel: "Tunnel",
			},
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{Kind: "Service", Name: "svc-a"},
		},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "app1.example.com", Target: "http://svc-a.default.svc:80"},
			},
		},
	}
	binding2 := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "binding-2",
			Namespace: "default",
			Labels: map[string]string{
				tunnelNameLabel: "my-tunnel",
				tunnelKindLabel: "Tunnel",
			},
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{Kind: "Service", Name: "svc-b"},
		},
		Status: networkingv1alpha1.TunnelBindingStatus{
			Services: []networkingv1alpha1.ServiceInfo{
				{Hostname: "app2.example.com", Target: "http://svc-b.default.svc:80"},
			},
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(binding1, binding2).
		WithStatusSubresource(&networkingv1alpha1.TunnelBinding{}).
		Build()

	// Set up mock CF server with matching TXT/CNAME records
	txtContent1, _ := json.Marshal(cf.DnsManagedRecordTxt{
		DnsId:      "cname-1",
		TunnelName: "test-tunnel",
		TunnelId:   "tun-456",
	})
	txtContent2, _ := json.Marshal(cf.DnsManagedRecordTxt{
		DnsId:      "cname-2",
		TunnelName: "test-tunnel",
		TunnelId:   "tun-456",
	})

	var deletedIDs []string
	handlers := map[string]http.HandlerFunc{
		"GET /zones/zone-789/dns_records": func(w http.ResponseWriter, r *http.Request) {
			name := r.URL.Query().Get("name")
			recordType := r.URL.Query().Get("type")
			var records []cloudflare.DNSRecord
			if recordType == "TXT" {
				switch name {
				case "_managed.app1.example.com":
					records = []cloudflare.DNSRecord{{ID: "txt-1", Type: "TXT", Name: name, Content: string(txtContent1)}}
				case "_managed.app2.example.com":
					records = []cloudflare.DNSRecord{{ID: "txt-2", Type: "TXT", Name: name, Content: string(txtContent2)}}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(cfListResponse(records))
		},
		"DELETE /zones/zone-789/dns_records/cname-1": func(w http.ResponseWriter, r *http.Request) {
			deletedIDs = append(deletedIDs, "cname-1")
			w.Header().Set("Content-Type", "application/json")
			w.Write(cfDeleteResponse("cname-1"))
		},
		"DELETE /zones/zone-789/dns_records/txt-1": func(w http.ResponseWriter, r *http.Request) {
			deletedIDs = append(deletedIDs, "txt-1")
			w.Header().Set("Content-Type", "application/json")
			w.Write(cfDeleteResponse("txt-1"))
		},
		"DELETE /zones/zone-789/dns_records/cname-2": func(w http.ResponseWriter, r *http.Request) {
			deletedIDs = append(deletedIDs, "cname-2")
			w.Header().Set("Content-Type", "application/json")
			w.Write(cfDeleteResponse("cname-2"))
		},
		"DELETE /zones/zone-789/dns_records/txt-2": func(w http.ResponseWriter, r *http.Request) {
			deletedIDs = append(deletedIDs, "txt-2")
			w.Header().Set("Content-Type", "application/json")
			w.Write(cfDeleteResponse("txt-2"))
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
	}

	if err := cleanupDNSRecords(r); err != nil {
		t.Fatalf("cleanupDNSRecords error: %v", err)
	}

	// Should have deleted 4 records: cname+txt for each hostname
	if len(deletedIDs) != 4 {
		t.Fatalf("expected 4 deletes, got %d: %v", len(deletedIDs), deletedIDs)
	}

	// Verify all expected IDs were deleted
	expected := map[string]bool{"cname-1": true, "txt-1": true, "cname-2": true, "txt-2": true}
	for _, id := range deletedIDs {
		if !expected[id] {
			t.Errorf("unexpected delete of %q", id)
		}
		delete(expected, id)
	}
	for id := range expected {
		t.Errorf("expected delete of %q not found", id)
	}
}

func TestLabelsForTunnel(t *testing.T) {
	tunnel := newTestTunnelObj("my-tunnel", "default")
	adapter := TunnelAdapter{Tunnel: tunnel}

	labels := labelsForTunnel(adapter)

	checks := map[string]string{
		tunnelLabel:          "my-tunnel",
		tunnelAppLabel:       "cloudflared",
		tunnelIdLabel:        "tun-456",
		tunnelNameLabel:      "test-tunnel",
		tunnelDomainLabel:    "example.com",
		isClusterTunnelLabel: "false",
	}

	for key, want := range checks {
		got := labels[key]
		if got != want {
			t.Errorf("label %s = %q, want %q", key, got, want)
		}
	}

	if len(labels) != len(checks) {
		t.Errorf("expected %d labels, got %d", len(checks), len(labels))
	}
}

func TestSetupNewTunnel(t *testing.T) {
	tunnel := newTestTunnelObj("new-tun", "default")
	tunnel.Spec.NewTunnel = &networkingv1alpha2.NewTunnel{Name: "new-tun"}
	tunnel.Spec.ExistingTunnel = nil
	tunnel.Status.TunnelId = ""
	tunnel.Status.TunnelName = ""

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel).
		Build()

	// Mock CF server: CreateTunnel needs account validation + tunnel creation
	handlers := map[string]http.HandlerFunc{
		"GET /accounts/acc-123": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"acc-123","name":"test-account"}}`))
		},
		"POST /accounts/acc-123/cfd_tunnel": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"new-tun-id","name":"new-tun"}}`))
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()
	// Clear pre-populated valid IDs so CreateTunnel resolves them
	cfAPI.ValidTunnelId = ""
	cfAPI.ValidTunnelName = ""

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
		cfSecret: &corev1.Secret{},
	}

	err := setupNewTunnel(r)
	if err != nil {
		t.Fatalf("setupNewTunnel returned error: %v", err)
	}

	// CreateTunnel sets ValidTunnelId
	if cfAPI.ValidTunnelId != "new-tun-id" {
		t.Errorf("ValidTunnelId = %q, want %q", cfAPI.ValidTunnelId, "new-tun-id")
	}

	// tunnelCreds should be non-empty JSON
	if r.tunnelCreds == "" {
		t.Fatal("tunnelCreds should be non-empty after CreateTunnel")
	}
	var creds cf.TunnelCredentialsFile
	if err := json.Unmarshal([]byte(r.tunnelCreds), &creds); err != nil {
		t.Fatalf("tunnelCreds is not valid JSON: %v", err)
	}
	if creds.TunnelID != "new-tun-id" {
		t.Errorf("creds.TunnelID = %q, want %q", creds.TunnelID, "new-tun-id")
	}

	// Finalizer should have been added
	updatedTunnel := &networkingv1alpha2.Tunnel{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "new-tun", Namespace: "default"}, updatedTunnel); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
	}
	found := false
	for _, f := range updatedTunnel.Finalizers {
		if f == tunnelFinalizer {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected tunnelFinalizer to be added to tunnel object")
	}
}

func TestSetupNewTunnel_AlreadyCreated(t *testing.T) {
	tunnel := newTestTunnelObj("existing-tun", "default")
	tunnel.Spec.NewTunnel = &networkingv1alpha2.NewTunnel{Name: "existing-tun"}
	tunnel.Spec.ExistingTunnel = nil
	tunnel.Status.TunnelId = "existing-id"
	tunnel.Status.TunnelName = "existing-tun"

	// Pre-create the Secret with credentials
	credsJSON := `{"AccountTag":"acc-123","TunnelID":"existing-id","TunnelName":"existing-tun","TunnelSecret":"c2VjcmV0"}`
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-tun",
			Namespace: "default",
		},
		Data: map[string][]byte{
			CredentialsJsonFilename: []byte(credsJSON),
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, secret).
		Build()

	// No CreateTunnel handler -- if called, the test will fail with unhandled route
	createCalled := false
	handlers := map[string]http.HandlerFunc{
		"POST /accounts/acc-123/cfd_tunnel": func(w http.ResponseWriter, r *http.Request) {
			createCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
		cfSecret: &corev1.Secret{},
	}

	err := setupNewTunnel(r)
	if err != nil {
		t.Fatalf("setupNewTunnel returned error: %v", err)
	}

	if createCalled {
		t.Error("CreateTunnel should not be called when TunnelId is already set")
	}

	// Creds should be read from the existing secret
	if r.tunnelCreds != credsJSON {
		t.Errorf("tunnelCreds = %q, want %q", r.tunnelCreds, credsJSON)
	}
}

func TestSetupExistingTunnel(t *testing.T) {
	tunnel := newTestTunnelObj("ext-tun", "default")
	tunnel.Spec.ExistingTunnel = &networkingv1alpha2.ExistingTunnel{
		Id:   "ext-id",
		Name: "ext-tun",
	}
	tunnel.Spec.NewTunnel = nil
	tunnel.Spec.Cloudflare.CLOUDFLARE_TUNNEL_CREDENTIAL_FILE = "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE"
	tunnel.Spec.Cloudflare.CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET = "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET"

	credsJSON := `{"AccountTag":"acc-123","TunnelID":"ext-id","TunnelName":"ext-tun","TunnelSecret":"c2VjcmV0"}`
	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"CLOUDFLARE_TUNNEL_CREDENTIAL_FILE": []byte(credsJSON),
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	cfAPI, server := newCfAPIWithMock(t, map[string]http.HandlerFunc{})
	defer server.Close()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
		cfSecret: cfSecret,
	}

	err := setupExistingTunnel(r)
	if err != nil {
		t.Fatalf("setupExistingTunnel returned error: %v", err)
	}

	if r.cfAPI.TunnelId != "ext-id" {
		t.Errorf("cfAPI.TunnelId = %q, want %q", r.cfAPI.TunnelId, "ext-id")
	}
	if r.tunnelCreds != credsJSON {
		t.Errorf("tunnelCreds = %q, want %q", r.tunnelCreds, credsJSON)
	}
}

func TestSetupExistingTunnel_FromSecret(t *testing.T) {
	tunnel := newTestTunnelObj("ext-tun", "default")
	tunnel.Spec.ExistingTunnel = &networkingv1alpha2.ExistingTunnel{
		Id:   "ext-id",
		Name: "ext-tun",
	}
	tunnel.Spec.NewTunnel = nil
	tunnel.Spec.Cloudflare.CLOUDFLARE_TUNNEL_CREDENTIAL_FILE = "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE"
	tunnel.Spec.Cloudflare.CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET = "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET"

	tunnelSecret := "my-tunnel-secret-string"
	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET": []byte(tunnelSecret),
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// GetTunnelCreds calls GetAccountId and GetTunnelId (validate via mock)
	handlers := map[string]http.HandlerFunc{
		"GET /accounts/acc-123": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"acc-123","name":"test-account"}}`))
		},
		"GET /accounts/acc-123/cfd_tunnel/ext-id": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"ext-id","name":"ext-tun"}}`))
		},
		"GET /zones": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[{"id":"zone-789","name":"example.com"}],"result_info":{"page":1,"per_page":50,"total_pages":1,"count":1,"total_count":1}}`))
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()
	// Clear ValidTunnelId so GetTunnelId validates via mock
	cfAPI.ValidTunnelId = ""
	cfAPI.ValidTunnelName = ""

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
		cfSecret: cfSecret,
	}

	err := setupExistingTunnel(r)
	if err != nil {
		t.Fatalf("setupExistingTunnel returned error: %v", err)
	}

	// tunnelCreds should contain constructed credentials JSON via GetTunnelCreds
	if r.tunnelCreds == "" {
		t.Fatal("tunnelCreds should be non-empty")
	}
	var credsMap map[string]string
	if err := json.Unmarshal([]byte(r.tunnelCreds), &credsMap); err != nil {
		t.Fatalf("tunnelCreds is not valid JSON: %v", err)
	}
	if credsMap["AccountTag"] != "acc-123" {
		t.Errorf("creds AccountTag = %q, want %q", credsMap["AccountTag"], "acc-123")
	}
	if credsMap["TunnelID"] != "ext-id" {
		t.Errorf("creds TunnelID = %q, want %q", credsMap["TunnelID"], "ext-id")
	}
	if credsMap["TunnelSecret"] != tunnelSecret {
		t.Errorf("creds TunnelSecret = %q, want %q", credsMap["TunnelSecret"], tunnelSecret)
	}
}

func TestSetupTunnel_NewTunnel(t *testing.T) {
	tunnel := newTestTunnelObj("new-tun", "default")
	tunnel.Spec.NewTunnel = &networkingv1alpha2.NewTunnel{Name: "new-tun"}
	tunnel.Spec.ExistingTunnel = nil
	tunnel.Status.TunnelId = ""
	tunnel.Status.TunnelName = ""

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel).
		Build()

	handlers := map[string]http.HandlerFunc{
		"GET /accounts/acc-123": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"acc-123","name":"test-account"}}`))
		},
		"POST /accounts/acc-123/cfd_tunnel": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"created-tun","name":"new-tun"}}`))
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()
	cfAPI.ValidTunnelId = ""
	cfAPI.ValidTunnelName = ""

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
		cfSecret: &corev1.Secret{},
	}

	result, ok, err := setupTunnel(r)
	if err != nil {
		t.Fatalf("setupTunnel returned error: %v", err)
	}
	if !ok {
		t.Errorf("setupTunnel ok = false, want true; result = %v", result)
	}

	// Tunnel creation happened
	if cfAPI.ValidTunnelId != "created-tun" {
		t.Errorf("ValidTunnelId = %q, want %q", cfAPI.ValidTunnelId, "created-tun")
	}
}

func TestSetupTunnel_ExistingTunnel(t *testing.T) {
	tunnel := newTestTunnelObj("ext-tun", "default")
	tunnel.Spec.ExistingTunnel = &networkingv1alpha2.ExistingTunnel{
		Id:   "ext-id",
		Name: "ext-tun",
	}
	tunnel.Spec.NewTunnel = nil
	tunnel.Spec.Cloudflare.CLOUDFLARE_TUNNEL_CREDENTIAL_FILE = "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE"

	credsJSON := `{"AccountTag":"acc-123","TunnelID":"ext-id","TunnelName":"ext-tun","TunnelSecret":"c2VjcmV0"}`
	cfSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"CLOUDFLARE_TUNNEL_CREDENTIAL_FILE": []byte(credsJSON),
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	createCalled := false
	handlers := map[string]http.HandlerFunc{
		"POST /accounts/acc-123/cfd_tunnel": func(w http.ResponseWriter, r *http.Request) {
			createCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
		cfSecret: cfSecret,
	}

	result, ok, err := setupTunnel(r)
	if err != nil {
		t.Fatalf("setupTunnel returned error: %v", err)
	}
	if !ok {
		t.Errorf("setupTunnel ok = false, want true; result = %v", result)
	}
	if createCalled {
		t.Error("CreateTunnel should not be called for ExistingTunnel")
	}
}

func TestSetupTunnel_BothSpecsSet(t *testing.T) {
	tunnel := newTestTunnelObj("bad-tun", "default")
	tunnel.Spec.NewTunnel = &networkingv1alpha2.NewTunnel{Name: "bad-tun"}
	tunnel.Spec.ExistingTunnel = &networkingv1alpha2.ExistingTunnel{Id: "ext-id", Name: "ext-tun"}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    &cf.API{Log: logrtesting.New(t)},
		cfSecret: &corev1.Secret{},
	}

	_, _, err := setupTunnel(r)
	if err == nil {
		t.Fatal("expected error when both NewTunnel and ExistingTunnel are set")
	}
}

func TestSetupTunnel_NeitherSpecSet(t *testing.T) {
	tunnel := newTestTunnelObj("bad-tun", "default")
	tunnel.Spec.NewTunnel = nil
	tunnel.Spec.ExistingTunnel = nil

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    &cf.API{Log: logrtesting.New(t)},
		cfSecret: &corev1.Secret{},
	}

	_, _, err := setupTunnel(r)
	if err == nil {
		t.Fatal("expected error when neither NewTunnel nor ExistingTunnel is set")
	}
}

func TestCleanupTunnel(t *testing.T) {
	now := metav1.Now()
	tunnel := newTestTunnelObj("del-tun", "default")
	tunnel.Finalizers = []string{tunnelFinalizer}
	tunnel.DeletionTimestamp = &now

	// Deployment with replicas=0 (already scaled down)
	var replicas int32
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "del-tun",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{tunnelLabel: "del-tun"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{tunnelLabel: "del-tun"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "cloudflared", Image: "cloudflare/cloudflared:latest"}},
				},
			},
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, dep).
		WithStatusSubresource(&networkingv1alpha2.Tunnel{}).
		Build()

	// Mock CF server for cleanup: ClearTunnelConfiguration, CleanupTunnelConnections, DeleteTunnel
	clearConfigCalled := false
	cleanupConnCalled := false
	deleteTunnelCalled := false
	handlers := map[string]http.HandlerFunc{
		// ValidateAll endpoints (needed by ClearTunnelConfiguration -> UpdateTunnelConfiguration -> ValidateAll)
		"GET /accounts/acc-123": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"acc-123","name":"test-account"}}`))
		},
		"GET /accounts/acc-123/cfd_tunnel/tun-456": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"tun-456","name":"test-tunnel"}}`))
		},
		"GET /zones": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[{"id":"zone-789","name":"example.com"}],"result_info":{"page":1,"per_page":50,"total_pages":1,"count":1,"total_count":1}}`))
		},
		"PUT /accounts/acc-123/cfd_tunnel/tun-456/configurations": func(w http.ResponseWriter, r *http.Request) {
			clearConfigCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{}}`))
		},
		"DELETE /accounts/acc-123/cfd_tunnel/tun-456/connections": func(w http.ResponseWriter, r *http.Request) {
			cleanupConnCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":null}`))
		},
		"DELETE /accounts/acc-123/cfd_tunnel/tun-456": func(w http.ResponseWriter, r *http.Request) {
			deleteTunnelCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":null}`))
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
		cfSecret: &corev1.Secret{},
	}

	result, ok, err := cleanupTunnel(r)
	if err != nil {
		t.Fatalf("cleanupTunnel returned error: %v", err)
	}
	if !ok {
		t.Errorf("cleanupTunnel ok = false, want true; result = %v", result)
	}

	if !clearConfigCalled {
		t.Error("ClearTunnelConfiguration was not called")
	}
	if !cleanupConnCalled {
		t.Error("CleanupTunnelConnections was not called")
	}
	if !deleteTunnelCalled {
		t.Error("DeleteTunnel was not called")
	}

	// Finalizer was removed. With DeletionTimestamp set and no remaining finalizers,
	// the fake client deletes the object entirely, so a Not Found error confirms cleanup succeeded.
	updatedTunnel := &networkingv1alpha2.Tunnel{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "del-tun", Namespace: "default"}, updatedTunnel)
	if err == nil {
		// Object still exists -- check that finalizer was at least removed
		for _, f := range updatedTunnel.Finalizers {
			if f == tunnelFinalizer {
				t.Error("expected tunnelFinalizer to be removed after cleanup")
			}
		}
	}
	// If Not Found, cleanup succeeded (object was garbage collected by fake client)
}

func TestCleanupTunnel_ScaleDown(t *testing.T) {
	now := metav1.Now()
	tunnel := newTestTunnelObj("scale-tun", "default")
	tunnel.Finalizers = []string{tunnelFinalizer}
	tunnel.DeletionTimestamp = &now

	// Deployment with replicas=1 (not yet scaled down)
	var replicas int32 = 1
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "scale-tun",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{tunnelLabel: "scale-tun"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{tunnelLabel: "scale-tun"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "cloudflared", Image: "cloudflare/cloudflared:latest"}},
				},
			},
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel, dep).
		WithStatusSubresource(&networkingv1alpha2.Tunnel{}).
		Build()

	deleteTunnelCalled := false
	handlers := map[string]http.HandlerFunc{
		"DELETE /accounts/acc-123/cfd_tunnel/tun-456": func(w http.ResponseWriter, r *http.Request) {
			deleteTunnelCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":null}`))
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
		cfSecret: &corev1.Secret{},
	}

	result, ok, err := cleanupTunnel(r)
	if err != nil {
		t.Fatalf("cleanupTunnel returned error: %v", err)
	}
	if ok {
		t.Error("cleanupTunnel ok = true, want false (should requeue)")
	}
	if result.RequeueAfter.Seconds() != 5 {
		t.Errorf("RequeueAfter = %v, want 5s", result.RequeueAfter)
	}

	// Deployment should be scaled to 0
	updatedDep := &appsv1.Deployment{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "scale-tun", Namespace: "default"}, updatedDep); err != nil {
		t.Fatalf("failed to get updated deployment: %v", err)
	}
	if *updatedDep.Spec.Replicas != 0 {
		t.Errorf("deployment replicas = %d, want 0", *updatedDep.Spec.Replicas)
	}

	// Tunnel should NOT have been deleted
	if deleteTunnelCalled {
		t.Error("DeleteTunnel should not be called before scale-down completes")
	}
}

func TestUpdateTunnelStatus(t *testing.T) {
	tunnel := newTestTunnelObj("status-tun", "default")
	tunnel.Status = networkingv1alpha2.TunnelStatus{} // Start with empty status

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tunnel).
		WithStatusSubresource(&networkingv1alpha2.Tunnel{}).
		Build()

	// Mock CF server for ValidateAll
	handlers := map[string]http.HandlerFunc{
		"GET /accounts/acc-123": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"acc-123","name":"test-account"}}`))
		},
		"GET /accounts/acc-123/cfd_tunnel/tun-456": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"tun-456","name":"test-tunnel"}}`))
		},
		"GET /zones": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[{"id":"zone-789","name":"example.com"}],"result_info":{"page":1,"per_page":50,"total_pages":1,"count":1,"total_count":1}}`))
		},
	}
	cfAPI, server := newCfAPIWithMock(t, handlers)
	defer server.Close()

	r := &testReconciler{
		client:   fakeClient,
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
		ctx:      context.Background(),
		log:      logrtesting.New(t),
		tunnel:   TunnelAdapter{Tunnel: tunnel},
		cfAPI:    cfAPI,
		cfSecret: &corev1.Secret{},
	}

	err := updateTunnelStatus(r)
	if err != nil {
		t.Fatalf("updateTunnelStatus returned error: %v", err)
	}

	// Check status fields on the tunnel object
	updatedTunnel := &networkingv1alpha2.Tunnel{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "status-tun", Namespace: "default"}, updatedTunnel); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
	}

	if updatedTunnel.Status.AccountId != "acc-123" {
		t.Errorf("status.AccountId = %q, want %q", updatedTunnel.Status.AccountId, "acc-123")
	}
	if updatedTunnel.Status.TunnelId != "tun-456" {
		t.Errorf("status.TunnelId = %q, want %q", updatedTunnel.Status.TunnelId, "tun-456")
	}
	if updatedTunnel.Status.TunnelName != "test-tunnel" {
		t.Errorf("status.TunnelName = %q, want %q", updatedTunnel.Status.TunnelName, "test-tunnel")
	}
	if updatedTunnel.Status.ZoneId != "zone-789" {
		t.Errorf("status.ZoneId = %q, want %q", updatedTunnel.Status.ZoneId, "zone-789")
	}

	// Check labels on the tunnel object
	if updatedTunnel.Labels[tunnelLabel] != "status-tun" {
		t.Errorf("label %s = %q, want %q", tunnelLabel, updatedTunnel.Labels[tunnelLabel], "status-tun")
	}
	if updatedTunnel.Labels[tunnelAppLabel] != "cloudflared" {
		t.Errorf("label %s = %q, want %q", tunnelAppLabel, updatedTunnel.Labels[tunnelAppLabel], "cloudflared")
	}
}
