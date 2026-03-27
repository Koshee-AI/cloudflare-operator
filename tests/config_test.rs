use cloudflare_operator::config::cloudflared::{
    Configuration, IngressIPRule, OriginRequestConfig, UnvalidatedIngressRule, WarpRoutingConfig,
};

// ── test_configuration_yaml_roundtrip ───────────────────────────────────

#[test]
fn test_configuration_yaml_roundtrip() {
    let config = Configuration {
        tunnel: "tun-aaa-bbb-ccc".to_string(),
        credentials_file: "/etc/cloudflared/creds/credentials.json".to_string(),
        ingress: vec![
            UnvalidatedIngressRule {
                hostname: Some("app.example.com".to_string()),
                service: "http://app.default.svc:80".to_string(),
                path: None,
                origin_request: None,
            },
            UnvalidatedIngressRule {
                hostname: None,
                service: "http_status:404".to_string(),
                path: None,
                origin_request: None,
            },
        ],
        warp_routing: None,
        origin_request: None,
        metrics: Some("0.0.0.0:2000".to_string()),
        no_auto_update: Some(true),
    };

    let yaml_str = serde_yaml::to_string(&config).unwrap();
    let deserialized: Configuration = serde_yaml::from_str(&yaml_str).unwrap();

    assert_eq!(deserialized.tunnel, config.tunnel);
    assert_eq!(deserialized.credentials_file, config.credentials_file);
    assert_eq!(deserialized.ingress.len(), 2);
    assert_eq!(deserialized.ingress[0].hostname, Some("app.example.com".to_string()));
    assert_eq!(deserialized.ingress[0].service, "http://app.default.svc:80");
    assert!(deserialized.ingress[1].hostname.is_none());
    assert_eq!(deserialized.ingress[1].service, "http_status:404");
    assert_eq!(deserialized.metrics, Some("0.0.0.0:2000".to_string()));
    assert_eq!(deserialized.no_auto_update, Some(true));
}

// ── test_ingress_rule_serialization ─────────────────────────────────────

#[test]
fn test_ingress_rule_serialization() {
    let rule = UnvalidatedIngressRule {
        hostname: Some("ssh.example.com".to_string()),
        service: "ssh://localhost:22".to_string(),
        path: Some("/admin.*".to_string()),
        origin_request: Some(OriginRequestConfig {
            no_tls_verify: Some(true),
            ..Default::default()
        }),
    };

    let yaml_str = serde_yaml::to_string(&rule).unwrap();
    let parsed: serde_yaml::Value = serde_yaml::from_str(&yaml_str).unwrap();

    // Verify camelCase serialization for the origin request fields
    assert_eq!(parsed["hostname"].as_str().unwrap(), "ssh.example.com");
    assert_eq!(parsed["service"].as_str().unwrap(), "ssh://localhost:22");
    assert_eq!(parsed["path"].as_str().unwrap(), "/admin.*");
    assert_eq!(parsed["originRequest"]["noTLSVerify"].as_bool().unwrap(), true);

    // Roundtrip
    let deserialized: UnvalidatedIngressRule = serde_yaml::from_str(&yaml_str).unwrap();
    assert_eq!(deserialized.hostname, Some("ssh.example.com".to_string()));
    assert_eq!(deserialized.path, Some("/admin.*".to_string()));
    assert_eq!(
        deserialized.origin_request.as_ref().unwrap().no_tls_verify,
        Some(true)
    );
}

// ── test_ingress_rule_omits_none_fields ─────────────────────────────────

#[test]
fn test_ingress_rule_omits_none_fields() {
    let catch_all = UnvalidatedIngressRule {
        hostname: None,
        service: "http_status:404".to_string(),
        path: None,
        origin_request: None,
    };

    let yaml_str = serde_yaml::to_string(&catch_all).unwrap();

    // None fields should be omitted from YAML
    assert!(!yaml_str.contains("hostname"));
    assert!(!yaml_str.contains("path"));
    assert!(!yaml_str.contains("originRequest"));
    assert!(yaml_str.contains("service: http_status:404"));
}

// ── test_origin_request_config_serialization ────────────────────────────

#[test]
fn test_origin_request_config_serialization() {
    let origin_req = OriginRequestConfig {
        connect_timeout: Some("30s".to_string()),
        tls_timeout: Some("10s".to_string()),
        tcp_keep_alive: Some("30s".to_string()),
        no_happy_eyeballs: Some(false),
        keep_alive_connections: Some(100),
        keep_alive_timeout: Some("90s".to_string()),
        http_host_header: Some("internal.example.com".to_string()),
        origin_server_name: Some("origin.example.com".to_string()),
        ca_pool: Some("/etc/ssl/certs/ca.pem".to_string()),
        no_tls_verify: Some(false),
        http2_origin: Some(true),
        disable_chunked_encoding: Some(false),
        bastion_mode: Some(false),
        proxy_address: Some("127.0.0.1".to_string()),
        proxy_port: Some(8080),
        proxy_type: Some("socks".to_string()),
        ip_rules: vec![IngressIPRule {
            prefix: Some("10.0.0.0/8".to_string()),
            ports: vec![80, 443],
            allow: true,
        }],
    };

    let yaml_str = serde_yaml::to_string(&origin_req).unwrap();
    let parsed: serde_yaml::Value = serde_yaml::from_str(&yaml_str).unwrap();

    // Verify camelCase field names
    assert_eq!(parsed["connectTimeout"].as_str().unwrap(), "30s");
    assert_eq!(parsed["tlsTimeout"].as_str().unwrap(), "10s");
    assert_eq!(parsed["tcpKeepAlive"].as_str().unwrap(), "30s");
    assert_eq!(parsed["noHappyEyeballs"].as_bool().unwrap(), false);
    assert_eq!(parsed["keepAliveConnections"].as_i64().unwrap(), 100);
    assert_eq!(parsed["keepAliveTimeout"].as_str().unwrap(), "90s");
    assert_eq!(parsed["httpHostHeader"].as_str().unwrap(), "internal.example.com");
    assert_eq!(parsed["originServerName"].as_str().unwrap(), "origin.example.com");
    assert_eq!(parsed["caPool"].as_str().unwrap(), "/etc/ssl/certs/ca.pem");
    // noTLSVerify is a custom rename, not standard camelCase
    assert_eq!(parsed["noTLSVerify"].as_bool().unwrap(), false);
    assert_eq!(parsed["http2Origin"].as_bool().unwrap(), true);
    assert_eq!(parsed["disableChunkedEncoding"].as_bool().unwrap(), false);
    assert_eq!(parsed["bastionMode"].as_bool().unwrap(), false);
    assert_eq!(parsed["proxyAddress"].as_str().unwrap(), "127.0.0.1");
    assert_eq!(parsed["proxyPort"].as_i64().unwrap(), 8080);
    assert_eq!(parsed["proxyType"].as_str().unwrap(), "socks");
    assert_eq!(parsed["ipRules"][0]["prefix"].as_str().unwrap(), "10.0.0.0/8");

    // Roundtrip
    let deserialized: OriginRequestConfig = serde_yaml::from_str(&yaml_str).unwrap();
    assert_eq!(deserialized.connect_timeout, Some("30s".to_string()));
    assert_eq!(deserialized.http2_origin, Some(true));
    assert_eq!(deserialized.proxy_port, Some(8080));
    assert_eq!(deserialized.ip_rules.len(), 1);
    assert_eq!(deserialized.ip_rules[0].prefix, Some("10.0.0.0/8".to_string()));
    assert_eq!(deserialized.ip_rules[0].ports, vec![80, 443]);
    assert!(deserialized.ip_rules[0].allow);
}

// ── test_empty_config_defaults ──────────────────────────────────────────

#[test]
fn test_empty_config_defaults() {
    // Minimal YAML with just the required fields
    let yaml = r#"
tunnel: "tun-123"
credentials-file: "/etc/cloudflared/creds/credentials.json"
"#;

    let config: Configuration = serde_yaml::from_str(yaml).unwrap();

    assert_eq!(config.tunnel, "tun-123");
    assert_eq!(config.credentials_file, "/etc/cloudflared/creds/credentials.json");
    assert!(config.ingress.is_empty());
    assert!(config.warp_routing.is_none());
    assert!(config.origin_request.is_none());
    assert!(config.metrics.is_none());
    assert!(config.no_auto_update.is_none());
}

// ── test_warp_routing_config ────────────────────────────────────────────

#[test]
fn test_warp_routing_config() {
    let config = Configuration {
        tunnel: "tun-123".to_string(),
        credentials_file: "/creds.json".to_string(),
        warp_routing: Some(WarpRoutingConfig { enabled: true }),
        ..Default::default()
    };

    let yaml_str = serde_yaml::to_string(&config).unwrap();
    assert!(yaml_str.contains("warp-routing"));

    let deserialized: Configuration = serde_yaml::from_str(&yaml_str).unwrap();
    assert!(deserialized.warp_routing.is_some());
    assert!(deserialized.warp_routing.unwrap().enabled);
}

// ── test_config_with_warp_routing_disabled_omitted ──────────────────────

#[test]
fn test_config_warp_routing_none_omitted() {
    let config = Configuration {
        tunnel: "tun-123".to_string(),
        credentials_file: "/creds.json".to_string(),
        warp_routing: None,
        ..Default::default()
    };

    let yaml_str = serde_yaml::to_string(&config).unwrap();
    assert!(!yaml_str.contains("warp-routing"));
}

// ── test_full_cloudflared_config_yaml_compat ────────────────────────────

#[test]
fn test_full_cloudflared_config_yaml_compat() {
    // Simulates a real cloudflared config YAML that the operator would produce
    let yaml = r#"
tunnel: abc-def-123
credentials-file: /etc/cloudflared/creds/credentials.json
metrics: 0.0.0.0:2000
no-autoupdate: true
ingress:
  - hostname: app.example.com
    service: http://app.default.svc:80
    originRequest:
      noTLSVerify: true
  - hostname: api.example.com
    service: https://api.default.svc:443
    path: /v1/.*
    originRequest:
      http2Origin: true
      originServerName: api.internal
  - service: http_status:404
"#;

    let config: Configuration = serde_yaml::from_str(yaml).unwrap();
    assert_eq!(config.tunnel, "abc-def-123");
    assert_eq!(config.ingress.len(), 3);

    // First rule
    assert_eq!(config.ingress[0].hostname, Some("app.example.com".to_string()));
    assert_eq!(config.ingress[0].service, "http://app.default.svc:80");
    assert!(config.ingress[0].origin_request.as_ref().unwrap().no_tls_verify.unwrap());

    // Second rule with path
    assert_eq!(config.ingress[1].hostname, Some("api.example.com".to_string()));
    assert_eq!(config.ingress[1].path, Some("/v1/.*".to_string()));
    assert!(config.ingress[1].origin_request.as_ref().unwrap().http2_origin.unwrap());
    assert_eq!(
        config.ingress[1].origin_request.as_ref().unwrap().origin_server_name,
        Some("api.internal".to_string())
    );

    // Catch-all
    assert!(config.ingress[2].hostname.is_none());
    assert_eq!(config.ingress[2].service, "http_status:404");
}
