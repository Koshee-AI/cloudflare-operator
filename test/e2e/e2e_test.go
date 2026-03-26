package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	networkingv1alpha1 "github.com/adyanth/cloudflare-operator/api/v1alpha1"
	networkingv1alpha2 "github.com/adyanth/cloudflare-operator/api/v1alpha2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operatorNamespace = "cloudflare-operator-system"
	testNamespace     = "e2e-test"
	tunnelName        = "e2e-tunnel"
	bindingName       = "e2e-binding"
	secretName        = "cloudflare-api-credentials"
	serviceName       = "e2e-whoami"
	pollInterval      = 2 * time.Second
	testTimeout       = 2 * time.Minute
)

var (
	k8sClient    client.Client
	clientset    *kubernetes.Clientset
	schemeSetup  *runtime.Scheme
)

func TestMain(m *testing.M) {
	schemeSetup = runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(schemeSetup)
	_ = networkingv1alpha1.AddToScheme(schemeSetup)
	_ = networkingv1alpha2.AddToScheme(schemeSetup)

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = home + "/.kube/config"
	}

	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubeconfig: %v\n", err)
		os.Exit(1)
	}

	k8sClient, err = client.New(restCfg, client.Options{Scheme: schemeSetup})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create k8s client: %v\n", err)
		os.Exit(1)
	}

	clientset, err = kubernetes.NewForConfig(restCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create clientset: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// ensureNamespace creates a namespace if it doesn't exist.
func ensureNamespace(ctx context.Context, t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := k8sClient.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("failed to create namespace %s: %v", name, err)
	}
}

// deleteNamespaceAndWait deletes a namespace and waits for it to disappear.
func deleteNamespaceAndWait(ctx context.Context, t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := k8sClient.Delete(ctx, ns)
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("failed to delete namespace %s: %v", name, err)
	}
	waitForDeletion(ctx, t, &corev1.Namespace{}, types.NamespacedName{Name: name})
}

// createCFSecret creates the Cloudflare API credentials Secret in the given namespace.
func createCFSecret(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"CLOUDFLARE_API_TOKEN": "test-token-for-e2e",
		},
	}
	err := k8sClient.Create(ctx, secret)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("failed to create secret: %v", err)
	}
}

// createTunnel creates a Tunnel CR with NewTunnel spec.
func createTunnel(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()
	tunnel := &networkingv1alpha2.Tunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnelName,
			Namespace: namespace,
		},
		Spec: networkingv1alpha2.TunnelSpec{
			NewTunnel: &networkingv1alpha2.NewTunnel{
				Name: tunnelName,
			},
			FallbackTarget: "http_status:404",
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:                          "example.com",
				Secret:                          secretName,
				AccountId:                       "test-account-id",
				CLOUDFLARE_API_TOKEN:            "CLOUDFLARE_API_TOKEN",
				CLOUDFLARE_API_KEY:              "CLOUDFLARE_API_KEY",
				CLOUDFLARE_TUNNEL_CREDENTIAL_FILE:   "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
				CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
			},
		},
	}
	err := k8sClient.Create(ctx, tunnel)
	if err != nil {
		t.Fatalf("failed to create tunnel: %v", err)
	}
}

// createService creates a simple ClusterIP Service.
func createService(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "whoami"},
			Ports: []corev1.ServicePort{
				{
					Port:       80,
					TargetPort: intstr.FromInt32(80),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
	err := k8sClient.Create(ctx, svc)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("failed to create service: %v", err)
	}
}

// createTunnelBinding creates a TunnelBinding referencing the tunnel and service.
func createTunnelBinding(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()
	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bindingName,
			Namespace: namespace,
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{
				Kind: "Service",
				Name: serviceName,
				Spec: networkingv1alpha1.TunnelBindingSubjectSpec{
					Fqdn:   serviceName + ".example.com",
					Target: fmt.Sprintf("http://%s.%s.svc:80", serviceName, namespace),
				},
			},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{
			Kind: "Tunnel",
			Name: tunnelName,
		},
	}
	err := k8sClient.Create(ctx, binding)
	if err != nil {
		t.Fatalf("failed to create tunnel binding: %v", err)
	}
}

// createTunnelBindingWithCredRef creates a TunnelBinding with a credentialSecretRef pointing to the operator namespace.
func createTunnelBindingWithCredRef(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()
	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bindingName,
			Namespace: namespace,
		},
		Subjects: []networkingv1alpha1.TunnelBindingSubject{
			{
				Kind: "Service",
				Name: serviceName,
				Spec: networkingv1alpha1.TunnelBindingSubjectSpec{
					Fqdn:   serviceName + ".example.com",
					Target: fmt.Sprintf("http://%s.%s.svc:80", serviceName, namespace),
				},
			},
		},
		TunnelRef: networkingv1alpha1.TunnelRef{
			Kind: "Tunnel",
			Name: tunnelName,
			CredentialSecretRef: &networkingv1alpha1.SecretReference{
				Name:      secretName,
				Namespace: operatorNamespace,
			},
		},
	}
	err := k8sClient.Create(ctx, binding)
	if err != nil {
		t.Fatalf("failed to create tunnel binding with credential ref: %v", err)
	}
}

// waitForTunnelReady waits until the Tunnel has a non-empty TunnelId in status.
func waitForTunnelReady(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		tunnel := &networkingv1alpha2.Tunnel{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: tunnelName, Namespace: namespace}, tunnel); err != nil {
			return false, nil
		}
		return tunnel.Status.TunnelId != "", nil
	})
	if err != nil {
		t.Fatalf("tunnel %s/%s did not become ready: %v", namespace, tunnelName, err)
	}
}

// waitForDeploymentAvailable waits until a Deployment is Available.
func waitForDeploymentAvailable(ctx context.Context, t *testing.T, name, namespace string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		dep := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, dep); err != nil {
			return false, nil
		}
		for _, c := range dep.Status.Conditions {
			if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("deployment %s/%s did not become available: %v", namespace, name, err)
	}
}

// waitForBindingHostnames waits until the TunnelBinding has a non-empty status.hostnames.
func waitForBindingHostnames(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		binding := &networkingv1alpha1.TunnelBinding{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: bindingName, Namespace: namespace}, binding); err != nil {
			return false, nil
		}
		return binding.Status.Hostnames != "", nil
	})
	if err != nil {
		t.Fatalf("tunnel binding %s/%s did not get hostnames: %v", namespace, bindingName, err)
	}
}

// waitForDeletion waits until the given object no longer exists.
func waitForDeletion(ctx context.Context, t *testing.T, obj client.Object, key types.NamespacedName) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		err := k8sClient.Get(ctx, key, obj)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("object %s did not get deleted: %v", key, err)
	}
}

// getConfigMap fetches the tunnel's ConfigMap and returns its config.yaml content.
func getConfigMap(ctx context.Context, t *testing.T, namespace string) string {
	t.Helper()
	cmList := &corev1.ConfigMapList{}
	err := k8sClient.List(ctx, cmList, client.InNamespace(namespace), client.MatchingLabels{
		"cfargotunnel.com/tunnel": tunnelName,
	})
	if err != nil {
		t.Fatalf("failed to list configmaps: %v", err)
	}
	if len(cmList.Items) == 0 {
		return ""
	}
	return cmList.Items[0].Data["config.yaml"]
}

// configMapHasIngress checks if the ConfigMap config.yaml mentions the given hostname.
func configMapHasIngress(configYAML, hostname string) bool {
	var config struct {
		Ingress []struct {
			Hostname string `json:"hostname" yaml:"hostname"`
		} `json:"ingress" yaml:"ingress"`
	}
	if err := json.Unmarshal([]byte(configYAML), &config); err != nil {
		// Try YAML-style parse by looking for substring
		return len(configYAML) > 0 && contains(configYAML, hostname)
	}
	for _, rule := range config.Ingress {
		if rule.Hostname == hostname {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// cleanup deletes resources in a namespace, ignoring NotFound errors.
func cleanup(ctx context.Context, t *testing.T, namespace string) {
	t.Helper()

	// Delete TunnelBinding first (has finalizer)
	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName, Namespace: namespace},
	}
	if err := k8sClient.Delete(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
		t.Logf("warning: failed to delete binding: %v", err)
	}

	// Wait for binding finalizer to complete
	_ = wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: bindingName, Namespace: namespace}, &networkingv1alpha1.TunnelBinding{})
		return apierrors.IsNotFound(err), nil
	})

	// Delete Tunnel
	tunnel := &networkingv1alpha2.Tunnel{
		ObjectMeta: metav1.ObjectMeta{Name: tunnelName, Namespace: namespace},
	}
	if err := k8sClient.Delete(ctx, tunnel); err != nil && !apierrors.IsNotFound(err) {
		t.Logf("warning: failed to delete tunnel: %v", err)
	}

	_ = wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: tunnelName, Namespace: namespace}, &networkingv1alpha2.Tunnel{})
		return apierrors.IsNotFound(err), nil
	})

	// Delete Service
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: serviceName, Namespace: namespace}}
	_ = k8sClient.Delete(ctx, svc)

	// Delete Secret
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace}}
	_ = k8sClient.Delete(ctx, secret)
}

func TestE2E_CreateTunnelAndBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	ensureNamespace(ctx, t, testNamespace)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), testTimeout)
		defer cleanupCancel()
		cleanup(cleanupCtx, t, testNamespace)
	})

	// Step 1: Create Secret with CF credentials
	createCFSecret(ctx, t, testNamespace)

	// Step 2: Create Tunnel with NewTunnel spec
	createTunnel(ctx, t, testNamespace)

	// Step 3: Wait for Tunnel to be ready (has TunnelId in status)
	waitForTunnelReady(ctx, t, testNamespace)

	// Verify a Deployment was created for the tunnel
	depName := fmt.Sprintf("cloudflared-%s", tunnelName)
	waitForDeploymentAvailable(ctx, t, depName, testNamespace)

	// Step 4: Create a Service
	createService(ctx, t, testNamespace)

	// Step 5: Create TunnelBinding
	createTunnelBinding(ctx, t, testNamespace)

	// Step 6: Wait for TunnelBinding to have status.hostnames set
	waitForBindingHostnames(ctx, t, testNamespace)

	// Step 7: Verify the TunnelBinding status
	binding := &networkingv1alpha1.TunnelBinding{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: bindingName, Namespace: testNamespace}, binding)
	if err != nil {
		t.Fatalf("failed to get binding: %v", err)
	}
	if binding.Status.Hostnames == "" {
		t.Fatal("expected non-empty hostnames in binding status")
	}
	if len(binding.Status.Services) == 0 {
		t.Fatal("expected at least one service in binding status")
	}

	// Step 8: Verify ConfigMap has ingress rules
	configYAML := getConfigMap(ctx, t, testNamespace)
	if configYAML == "" {
		t.Fatal("expected non-empty ConfigMap config.yaml")
	}
	expectedHostname := serviceName + ".example.com"
	if !configMapHasIngress(configYAML, expectedHostname) {
		t.Fatalf("expected ConfigMap to contain ingress for %s, got:\n%s", expectedHostname, configYAML)
	}
}

func TestE2E_DeleteBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	ensureNamespace(ctx, t, testNamespace)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), testTimeout)
		defer cleanupCancel()
		cleanup(cleanupCtx, t, testNamespace)
	})

	// Setup: create tunnel + binding
	createCFSecret(ctx, t, testNamespace)
	createTunnel(ctx, t, testNamespace)
	waitForTunnelReady(ctx, t, testNamespace)
	createService(ctx, t, testNamespace)
	createTunnelBinding(ctx, t, testNamespace)
	waitForBindingHostnames(ctx, t, testNamespace)

	// Delete the binding
	binding := &networkingv1alpha1.TunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName, Namespace: testNamespace},
	}
	err := k8sClient.Delete(ctx, binding)
	if err != nil {
		t.Fatalf("failed to delete binding: %v", err)
	}

	// Wait for binding to be fully gone (finalizer processed)
	waitForDeletion(ctx, t, &networkingv1alpha1.TunnelBinding{}, types.NamespacedName{
		Name: bindingName, Namespace: testNamespace,
	})

	// Verify ConfigMap no longer has the binding's routes
	err = wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		configYAML := getConfigMap(ctx, t, testNamespace)
		if configYAML == "" {
			// ConfigMap might have been cleaned or reset to just fallback
			return true, nil
		}
		return !configMapHasIngress(configYAML, serviceName+".example.com"), nil
	})
	if err != nil {
		t.Fatalf("ConfigMap still contains binding routes after deletion: %v", err)
	}
}

func TestE2E_DeleteTunnel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	ensureNamespace(ctx, t, testNamespace)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), testTimeout)
		defer cleanupCancel()
		cleanup(cleanupCtx, t, testNamespace)
	})

	// Setup: create tunnel + binding
	createCFSecret(ctx, t, testNamespace)
	createTunnel(ctx, t, testNamespace)
	waitForTunnelReady(ctx, t, testNamespace)

	depName := fmt.Sprintf("cloudflared-%s", tunnelName)
	waitForDeploymentAvailable(ctx, t, depName, testNamespace)

	// Get the tunnel to know its ID for verifying cleanup
	tunnel := &networkingv1alpha2.Tunnel{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: tunnelName, Namespace: testNamespace}, tunnel)
	if err != nil {
		t.Fatalf("failed to get tunnel: %v", err)
	}
	tunnelID := tunnel.Status.TunnelId
	if tunnelID == "" {
		t.Fatal("expected tunnel to have a TunnelId")
	}

	// Delete the tunnel
	err = k8sClient.Delete(ctx, tunnel)
	if err != nil {
		t.Fatalf("failed to delete tunnel: %v", err)
	}

	// Wait for tunnel to be fully gone
	waitForDeletion(ctx, t, &networkingv1alpha2.Tunnel{}, types.NamespacedName{
		Name: tunnelName, Namespace: testNamespace,
	})

	// Verify managed resources are cleaned up: Deployment
	err = wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		dep := &appsv1.Deployment{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: depName, Namespace: testNamespace}, dep)
		return apierrors.IsNotFound(err), nil
	})
	if err != nil {
		t.Fatalf("deployment %s was not cleaned up after tunnel deletion: %v", depName, err)
	}

	// Verify ConfigMap is cleaned up
	cmList := &corev1.ConfigMapList{}
	err = k8sClient.List(ctx, cmList, client.InNamespace(testNamespace), client.MatchingLabels{
		"cfargotunnel.com/tunnel": tunnelName,
	})
	if err != nil {
		t.Fatalf("failed to list configmaps: %v", err)
	}
	if len(cmList.Items) != 0 {
		t.Fatalf("expected no configmaps for tunnel %s, found %d", tunnelName, len(cmList.Items))
	}
}

func TestE2E_NamespaceDeletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	nsName := "e2e-ns-deletion-test"

	// Clean up from any previous failed run
	_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}})
	_ = wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, &corev1.Namespace{})
		return apierrors.IsNotFound(err), nil
	})

	// Create the test namespace
	ensureNamespace(ctx, t, nsName)

	// Create the credential secret in the operator namespace (for credentialSecretRef)
	operatorSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: operatorNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"CLOUDFLARE_API_TOKEN": "test-token-for-e2e",
		},
	}
	err := k8sClient.Create(ctx, operatorSecret)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("failed to create operator namespace secret: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_ = k8sClient.Delete(cleanupCtx, operatorSecret)
	})

	// Create Secret in the test namespace (for tunnel spec)
	createCFSecret(ctx, t, nsName)

	// Create Tunnel
	tunnel := &networkingv1alpha2.Tunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnelName,
			Namespace: nsName,
		},
		Spec: networkingv1alpha2.TunnelSpec{
			NewTunnel: &networkingv1alpha2.NewTunnel{
				Name: tunnelName + "-ns-test",
			},
			FallbackTarget: "http_status:404",
			Cloudflare: networkingv1alpha2.CloudflareDetails{
				Domain:                          "example.com",
				Secret:                          secretName,
				AccountId:                       "test-account-id",
				CLOUDFLARE_API_TOKEN:            "CLOUDFLARE_API_TOKEN",
				CLOUDFLARE_API_KEY:              "CLOUDFLARE_API_KEY",
				CLOUDFLARE_TUNNEL_CREDENTIAL_FILE:   "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
				CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
			},
		},
	}
	err = k8sClient.Create(ctx, tunnel)
	if err != nil {
		t.Fatalf("failed to create tunnel: %v", err)
	}
	waitForTunnelReady(ctx, t, nsName)

	// Create Service
	createService(ctx, t, nsName)

	// Create TunnelBinding with credentialSecretRef pointing to operator namespace
	createTunnelBindingWithCredRef(ctx, t, nsName)
	waitForBindingHostnames(ctx, t, nsName)

	// Delete the test namespace
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	err = k8sClient.Delete(ctx, ns)
	if err != nil {
		t.Fatalf("failed to delete namespace: %v", err)
	}

	// Wait for namespace to be fully gone -- this verifies no stuck finalizer
	waitForDeletion(ctx, t, &corev1.Namespace{}, types.NamespacedName{Name: nsName})
}
