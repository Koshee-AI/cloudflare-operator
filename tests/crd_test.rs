use serde_json::json;

use cloudflare_operator::crds::tunnel::TunnelSpec;
use cloudflare_operator::crds::tunnel_binding::{TunnelBinding, TunnelBindingStatus};
use cloudflare_operator::crds::types::{
    CloudflareDetails, ExistingTunnel, NewTunnel, SecretReference, ServiceInfo,
    TunnelBindingSubject, TunnelBindingSubjectSpec, TunnelRef,
};

// ── test_tunnel_spec_serialization ──────────────────────────────────────

#[test]
fn test_tunnel_spec_serialization() {
    let spec = TunnelSpec {
        deploy_patch: "{}".to_string(),
        no_tls_verify: true,
        origin_ca_pool: "my-ca-secret".to_string(),
        protocol: "http2".to_string(),
        fallback_target: "http_status:404".to_string(),
        cloudflare: CloudflareDetails {
            domain: "example.com".to_string(),
            secret: "cloudflare-creds".to_string(),
            account_name: "My Account".to_string(),
            account_id: "acct-123".to_string(),
            email: "user@example.com".to_string(),
            cloudflare_api_key: "CLOUDFLARE_API_KEY".to_string(),
            cloudflare_api_token: "CLOUDFLARE_API_TOKEN".to_string(),
            cloudflare_tunnel_credential_file: "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE".to_string(),
            cloudflare_tunnel_credential_secret: "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET".to_string(),
        },
        existing_tunnel: Some(ExistingTunnel {
            id: "tun-abc".to_string(),
            name: "my-tunnel".to_string(),
        }),
        new_tunnel: None,
        cloudflared_image: None,
    };

    let val = serde_json::to_value(&spec).unwrap();

    // Verify camelCase field names match Go CRD conventions
    assert_eq!(val["deployPatch"], "{}");
    assert_eq!(val["noTlsVerify"], true);
    assert_eq!(val["originCaPool"], "my-ca-secret");
    assert_eq!(val["protocol"], "http2");
    assert_eq!(val["fallbackTarget"], "http_status:404");
    assert_eq!(val["cloudflare"]["domain"], "example.com");
    assert_eq!(val["cloudflare"]["secret"], "cloudflare-creds");
    assert_eq!(val["cloudflare"]["accountName"], "My Account");
    assert_eq!(val["cloudflare"]["accountId"], "acct-123");
    assert_eq!(val["cloudflare"]["email"], "user@example.com");
    assert_eq!(val["existingTunnel"]["id"], "tun-abc");
    assert_eq!(val["existingTunnel"]["name"], "my-tunnel");

    // newTunnel should be absent (skip_serializing_if)
    assert!(val.get("newTunnel").is_none());
}

#[test]
fn test_tunnel_spec_with_new_tunnel() {
    let spec = TunnelSpec {
        new_tunnel: Some(NewTunnel {
            name: "new-tun".to_string(),
        }),
        existing_tunnel: None,
        cloudflare: CloudflareDetails {
            domain: "example.com".to_string(),
            secret: "cf-secret".to_string(),
            ..Default::default()
        },
        ..Default::default()
    };

    let val = serde_json::to_value(&spec).unwrap();
    assert_eq!(val["newTunnel"]["name"], "new-tun");
    assert!(val.get("existingTunnel").is_none());
}

#[test]
fn test_tunnel_spec_defaults() {
    let spec: TunnelSpec = serde_json::from_value(json!({
        "cloudflare": {
            "domain": "example.com",
            "secret": "cf-secret"
        }
    }))
    .unwrap();

    assert_eq!(spec.deploy_patch, "{}");
    assert_eq!(spec.protocol, "auto");
    assert_eq!(spec.fallback_target, "http_status:404");
    assert!(!spec.no_tls_verify);
    assert!(spec.origin_ca_pool.is_empty());
    assert!(spec.existing_tunnel.is_none());
    assert!(spec.new_tunnel.is_none());
}

// ── test_tunnel_binding_serialization ───────────────────────────────────

#[test]
fn test_tunnel_binding_serialization() {
    let binding = TunnelBinding {
        metadata: k8s_openapi::apimachinery::pkg::apis::meta::v1::ObjectMeta {
            name: Some("my-binding".to_string()),
            namespace: Some("default".to_string()),
            ..Default::default()
        },
        subjects: vec![TunnelBindingSubject {
            kind: "Service".to_string(),
            name: "my-svc".to_string(),
            spec: TunnelBindingSubjectSpec {
                fqdn: "app.example.com".to_string(),
                protocol: "https".to_string(),
                path: "/api/.*".to_string(),
                no_tls_verify: true,
                ..Default::default()
            },
        }],
        tunnel_ref: TunnelRef {
            kind: "Tunnel".to_string(),
            name: "my-tunnel".to_string(),
            disable_dns_updates: false,
            credential_secret_ref: None,
        },
        status: None,
    };

    let val = serde_json::to_value(&binding).unwrap();

    // TunnelBinding has subjects and tunnelRef at top level, NOT under spec
    assert!(
        val.get("spec").is_none(),
        "TunnelBinding should NOT have a 'spec' wrapper"
    );
    assert!(
        val.get("subjects").is_some(),
        "subjects should be at top level"
    );
    assert!(
        val.get("tunnelRef").is_some(),
        "tunnelRef should be at top level"
    );

    // Verify subjects array
    let subjects = val["subjects"].as_array().unwrap();
    assert_eq!(subjects.len(), 1);
    assert_eq!(subjects[0]["kind"], "Service");
    assert_eq!(subjects[0]["name"], "my-svc");
    assert_eq!(subjects[0]["spec"]["fqdn"], "app.example.com");
    assert_eq!(subjects[0]["spec"]["protocol"], "https");
    assert_eq!(subjects[0]["spec"]["path"], "/api/.*");
    assert_eq!(subjects[0]["spec"]["noTlsVerify"], true);

    // Verify tunnelRef
    assert_eq!(val["tunnelRef"]["kind"], "Tunnel");
    assert_eq!(val["tunnelRef"]["name"], "my-tunnel");
    assert_eq!(val["tunnelRef"]["disableDnsUpdates"], false);

    // status should be absent when None
    assert!(val.get("status").is_none());
}

#[test]
fn test_tunnel_binding_deserialization() {
    // Simulates what Kubernetes API would return - subjects/tunnelRef at top level
    let input = json!({
        "metadata": {
            "name": "test-binding",
            "namespace": "default"
        },
        "subjects": [{
            "kind": "Service",
            "name": "web",
            "spec": {
                "fqdn": "web.example.com",
                "protocol": "http"
            }
        }],
        "tunnelRef": {
            "kind": "ClusterTunnel",
            "name": "global-tunnel",
            "disableDnsUpdates": true
        }
    });

    let binding: TunnelBinding = serde_json::from_value(input).unwrap();
    assert_eq!(binding.subjects.len(), 1);
    assert_eq!(binding.subjects[0].name, "web");
    assert_eq!(binding.subjects[0].spec.fqdn, "web.example.com");
    assert_eq!(binding.tunnel_ref.kind, "ClusterTunnel");
    assert_eq!(binding.tunnel_ref.name, "global-tunnel");
    assert!(binding.tunnel_ref.disable_dns_updates);
    assert!(binding.status.is_none());
}

// ── test_tunnel_binding_status_serialization ────────────────────────────

#[test]
fn test_tunnel_binding_status_serialization() {
    let status = TunnelBindingStatus {
        hostnames: "app.example.com,api.example.com".to_string(),
        services: vec![
            ServiceInfo {
                hostname: "app.example.com".to_string(),
                target: "http://app.default.svc:80".to_string(),
            },
            ServiceInfo {
                hostname: "api.example.com".to_string(),
                target: "https://api.default.svc:443".to_string(),
            },
        ],
    };

    let val = serde_json::to_value(&status).unwrap();
    assert_eq!(val["hostnames"], "app.example.com,api.example.com");

    let services = val["services"].as_array().unwrap();
    assert_eq!(services.len(), 2);
    assert_eq!(services[0]["hostname"], "app.example.com");
    assert_eq!(services[0]["target"], "http://app.default.svc:80");
    assert_eq!(services[1]["hostname"], "api.example.com");
    assert_eq!(services[1]["target"], "https://api.default.svc:443");

    // Roundtrip
    let deserialized: TunnelBindingStatus = serde_json::from_value(val).unwrap();
    assert_eq!(deserialized.hostnames, "app.example.com,api.example.com");
    assert_eq!(deserialized.services.len(), 2);
}

// ── test_credential_secret_ref_serialization ────────────────────────────

#[test]
fn test_credential_secret_ref_serialization() {
    let secret_ref = SecretReference {
        name: "tunnel-creds".to_string(),
        namespace: "infra".to_string(),
    };

    let val = serde_json::to_value(&secret_ref).unwrap();
    assert_eq!(val["name"], "tunnel-creds");
    assert_eq!(val["namespace"], "infra");

    let deserialized: SecretReference = serde_json::from_value(val).unwrap();
    assert_eq!(deserialized.name, "tunnel-creds");
    assert_eq!(deserialized.namespace, "infra");
}

#[test]
fn test_tunnel_ref_with_credential_secret_ref() {
    let tunnel_ref = TunnelRef {
        kind: "ClusterTunnel".to_string(),
        name: "global-tunnel".to_string(),
        disable_dns_updates: false,
        credential_secret_ref: Some(SecretReference {
            name: "other-ns-creds".to_string(),
            namespace: "other-ns".to_string(),
        }),
    };

    let val = serde_json::to_value(&tunnel_ref).unwrap();
    assert_eq!(val["kind"], "ClusterTunnel");
    assert_eq!(val["name"], "global-tunnel");
    assert_eq!(val["disableDnsUpdates"], false);
    assert_eq!(val["credentialSecretRef"]["name"], "other-ns-creds");
    assert_eq!(val["credentialSecretRef"]["namespace"], "other-ns");
}

// ── test_tunnel_ref_serialization ───────────────────────────────────────

#[test]
fn test_tunnel_ref_serialization() {
    let tunnel_ref = TunnelRef {
        kind: "Tunnel".to_string(),
        name: "my-tunnel".to_string(),
        disable_dns_updates: true,
        credential_secret_ref: None,
    };

    let val = serde_json::to_value(&tunnel_ref).unwrap();

    // camelCase
    assert_eq!(val["kind"], "Tunnel");
    assert_eq!(val["name"], "my-tunnel");
    assert_eq!(val["disableDnsUpdates"], true);

    // credentialSecretRef should be absent when None (skip_serializing_if)
    assert!(
        val.get("credentialSecretRef").is_none(),
        "credentialSecretRef should be omitted when None"
    );

    // Roundtrip
    let deserialized: TunnelRef = serde_json::from_value(val).unwrap();
    assert_eq!(deserialized.kind, "Tunnel");
    assert_eq!(deserialized.name, "my-tunnel");
    assert!(deserialized.disable_dns_updates);
    assert!(deserialized.credential_secret_ref.is_none());
}

// ── test_tunnel_binding_subject_defaults ────────────────────────────────

#[test]
fn test_tunnel_binding_subject_defaults() {
    let subject: TunnelBindingSubject = serde_json::from_value(json!({
        "name": "my-svc"
    }))
    .unwrap();

    assert_eq!(subject.kind, "Service", "default kind should be Service");
    assert_eq!(subject.name, "my-svc");
    assert!(subject.spec.fqdn.is_empty());
    assert!(subject.spec.protocol.is_empty());
    assert!(!subject.spec.no_tls_verify);
    assert!(!subject.spec.http2_origin);
    // Note: Go has kubebuilder default "127.0.0.1" but that's applied by the API server,
    // not in the struct. Rust Default gives empty string.
    assert!(subject.spec.proxy_address.is_empty());
    assert_eq!(subject.spec.proxy_port, 0);
}

// ── test_cloudflare_details_serialization ───────────────────────────────

#[test]
fn test_cloudflare_details_custom_key_names() {
    let details = CloudflareDetails {
        domain: "example.com".to_string(),
        secret: "cf-secret".to_string(),
        cloudflare_api_key: "MY_KEY".to_string(),
        cloudflare_api_token: "MY_TOKEN".to_string(),
        cloudflare_tunnel_credential_file: "MY_CRED_FILE".to_string(),
        cloudflare_tunnel_credential_secret: "MY_CRED_SECRET".to_string(),
        ..Default::default()
    };

    let val = serde_json::to_value(&details).unwrap();

    // These use custom serde rename to match the Go CRD CLOUDFLARE_ convention
    assert_eq!(val["CLOUDFLARE_API_KEY"], "MY_KEY");
    assert_eq!(val["CLOUDFLARE_API_TOKEN"], "MY_TOKEN");
    assert_eq!(val["CLOUDFLARE_TUNNEL_CREDENTIAL_FILE"], "MY_CRED_FILE");
    assert_eq!(val["CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET"], "MY_CRED_SECRET");
}

#[test]
fn test_cloudflare_details_defaults() {
    let details: CloudflareDetails = serde_json::from_value(json!({
        "domain": "example.com",
        "secret": "cf-secret"
    }))
    .unwrap();

    assert_eq!(details.domain, "example.com");
    assert_eq!(details.secret, "cf-secret");
    assert!(details.account_name.is_empty());
    assert!(details.account_id.is_empty());
    assert!(details.email.is_empty());
    assert_eq!(details.cloudflare_api_key, "CLOUDFLARE_API_KEY");
    assert_eq!(details.cloudflare_api_token, "CLOUDFLARE_API_TOKEN");
    assert_eq!(
        details.cloudflare_tunnel_credential_file,
        "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE"
    );
    assert_eq!(
        details.cloudflare_tunnel_credential_secret,
        "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET"
    );
}
