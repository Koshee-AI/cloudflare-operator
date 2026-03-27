use serde_json::json;
use wiremock::matchers::{header, method, path, query_param};
use wiremock::{Mock, MockServer, ResponseTemplate};

use cloudflare_operator::cloudflare::client::CfClient;
use cloudflare_operator::cloudflare::types::TunnelIngressRule;

/// Helper: wrap data in the Cloudflare single-result response envelope.
fn cf_ok<T: serde::Serialize>(result: &T) -> serde_json::Value {
    json!({
        "success": true,
        "errors": [],
        "messages": [],
        "result": result
    })
}

/// Helper: wrap data in the Cloudflare list-result response envelope.
fn cf_list_ok<T: serde::Serialize>(result: &[T]) -> serde_json::Value {
    json!({
        "success": true,
        "errors": [],
        "messages": [],
        "result": result,
        "result_info": {
            "page": 1,
            "per_page": 20,
            "count": result.len(),
            "total_count": result.len()
        }
    })
}

fn cf_error(code: u64, message: &str) -> serde_json::Value {
    json!({
        "success": false,
        "errors": [{"code": code, "message": message}],
        "messages": [],
        "result": null
    })
}

fn new_client(base_url: &str) -> CfClient {
    CfClient::new("test-api-token", Some(base_url))
}

// ── test_create_tunnel ──────────────────────────────────────────────────

#[tokio::test]
async fn test_create_tunnel() {
    let server = MockServer::start().await;
    let account_id = "acct-111";

    Mock::given(method("POST"))
        .and(path(format!("/accounts/{account_id}/cfd_tunnel")))
        .and(header("Authorization", "Bearer test-api-token"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!({
            "id": "tun-aaa",
            "name": "my-tunnel"
        }))))
        .expect(1)
        .mount(&server)
        .await;

    let mut client = new_client(&server.uri());
    let (tunnel_id, creds_json) = client.create_tunnel(account_id, "my-tunnel").await.unwrap();

    assert_eq!(tunnel_id, "tun-aaa");
    assert_eq!(client.tunnel_id, "tun-aaa");
    assert_eq!(client.tunnel_name, "my-tunnel");

    // Verify credentials JSON is well-formed and contains expected fields
    let creds: serde_json::Value = serde_json::from_str(&creds_json).unwrap();
    assert_eq!(creds["AccountTag"], account_id);
    assert_eq!(creds["TunnelID"], "tun-aaa");
    assert_eq!(creds["TunnelName"], "my-tunnel");
    assert!(creds["TunnelSecret"].as_str().unwrap().len() > 0);
}

// ── test_delete_tunnel ──────────────────────────────────────────────────

#[tokio::test]
async fn test_delete_tunnel() {
    let server = MockServer::start().await;

    let account_id = "acct-111";
    let tunnel_id = "tun-bbb";

    // Mock DELETE connections
    Mock::given(method("DELETE"))
        .and(path(format!(
            "/accounts/{account_id}/cfd_tunnel/{tunnel_id}/connections"
        )))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!(null))))
        .expect(1)
        .named("clean connections")
        .mount(&server)
        .await;

    // Mock DELETE tunnel
    Mock::given(method("DELETE"))
        .and(path(format!(
            "/accounts/{account_id}/cfd_tunnel/{tunnel_id}"
        )))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!(null))))
        .expect(1)
        .named("delete tunnel")
        .mount(&server)
        .await;

    let mut client = new_client(&server.uri());
    client.account_id = account_id.to_string();
    client.tunnel_id = tunnel_id.to_string();

    client.delete_tunnel().await.unwrap();
    // Both mocks expected exactly 1 call each; wiremock will verify on drop.
}

// ── test_validate_account_by_id ─────────────────────────────────────────

#[tokio::test]
async fn test_validate_account_by_id() {
    let server = MockServer::start().await;
    let account_id = "acct-222";

    Mock::given(method("GET"))
        .and(path(format!("/accounts/{account_id}")))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!({
            "id": account_id,
            "name": "My Account"
        }))))
        .expect(1)
        .mount(&server)
        .await;

    let mut client = new_client(&server.uri());
    let result = client.validate_account(account_id, "").await.unwrap();
    assert_eq!(result, account_id);
    assert_eq!(client.account_id, account_id);

    // Second call should return cached value without hitting the server again
    let result2 = client.validate_account(account_id, "").await.unwrap();
    assert_eq!(result2, account_id);
    // Mock expected exactly 1 call; second call must have been cached.
}

// ── test_validate_account_by_name ───────────────────────────────────────

#[tokio::test]
async fn test_validate_account_by_name() {
    let server = MockServer::start().await;

    // ID validation fails
    Mock::given(method("GET"))
        .and(path("/accounts/bad-id"))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(cf_error(1003, "Invalid account ID")),
        )
        .mount(&server)
        .await;

    // Fallback to name
    Mock::given(method("GET"))
        .and(path("/accounts"))
        .and(query_param("name", "My Account"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_list_ok(&[json!({
            "id": "acct-333",
            "name": "My Account"
        })])))
        .expect(1)
        .mount(&server)
        .await;

    let mut client = new_client(&server.uri());
    let result = client.validate_account("bad-id", "My Account").await.unwrap();
    assert_eq!(result, "acct-333");
    assert_eq!(client.account_id, "acct-333");
}

// ── test_validate_zone ──────────────────────────────────────────────────

#[tokio::test]
async fn test_validate_zone() {
    let server = MockServer::start().await;

    Mock::given(method("GET"))
        .and(path("/zones"))
        .and(query_param("name", "example.com"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_list_ok(&[json!({
            "id": "zone-444",
            "name": "example.com"
        })])))
        .expect(1)
        .mount(&server)
        .await;

    let mut client = new_client(&server.uri());
    let zone_id = client.validate_zone("example.com").await.unwrap();
    assert_eq!(zone_id, "zone-444");
    assert_eq!(client.zone_id, "zone-444");
    assert_eq!(client.domain, "example.com");
}

// ── test_get_dns_cname_id ───────────────────────────────────────────────

#[tokio::test]
async fn test_get_dns_cname_id() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();

    Mock::given(method("GET"))
        .and(path("/zones/zone-555/dns_records"))
        .and(query_param("type", "CNAME"))
        .and(query_param("name", "app.example.com"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_list_ok(&[json!({
            "id": "dns-cname-1",
            "name": "app.example.com",
            "type": "CNAME",
            "content": "tun-aaa.cfargotunnel.com"
        })])))
        .expect(1)
        .mount(&server)
        .await;

    let dns_id = client.get_dns_cname_id("app.example.com").await.unwrap();
    assert_eq!(dns_id, "dns-cname-1");
}

// ── test_get_managed_dns_txt ────────────────────────────────────────────

#[tokio::test]
async fn test_get_managed_dns_txt_owned_by_this_tunnel() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();
    client.tunnel_id = "tun-aaa".to_string();
    client.tunnel_name = "my-tunnel".to_string();

    let txt_content = json!({
        "DnsId": "dns-cname-1",
        "TunnelName": "my-tunnel",
        "TunnelId": "tun-aaa"
    });

    Mock::given(method("GET"))
        .and(path("/zones/zone-555/dns_records"))
        .and(query_param("type", "TXT"))
        .and(query_param("name", "_managed.app.example.com"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_list_ok(&[json!({
            "id": "dns-txt-1",
            "name": "_managed.app.example.com",
            "type": "TXT",
            "content": txt_content.to_string()
        })])))
        .expect(1)
        .mount(&server)
        .await;

    let (txt_id, txt_data, can_use) =
        client.get_managed_dns_txt("app.example.com").await.unwrap();

    assert_eq!(txt_id, "dns-txt-1");
    assert_eq!(txt_data.dns_id, "dns-cname-1");
    assert_eq!(txt_data.tunnel_id, "tun-aaa");
    assert_eq!(txt_data.tunnel_name, "my-tunnel");
    assert!(can_use, "should be usable when owned by this tunnel");
}

#[tokio::test]
async fn test_get_managed_dns_txt_owned_by_other_tunnel() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();
    client.tunnel_id = "tun-aaa".to_string();

    let txt_content = json!({
        "DnsId": "dns-cname-1",
        "TunnelName": "other-tunnel",
        "TunnelId": "tun-other"
    });

    Mock::given(method("GET"))
        .and(path("/zones/zone-555/dns_records"))
        .and(query_param("type", "TXT"))
        .and(query_param("name", "_managed.app.example.com"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_list_ok(&[json!({
            "id": "dns-txt-2",
            "name": "_managed.app.example.com",
            "type": "TXT",
            "content": txt_content.to_string()
        })])))
        .expect(1)
        .mount(&server)
        .await;

    let (txt_id, txt_data, can_use) =
        client.get_managed_dns_txt("app.example.com").await.unwrap();

    assert_eq!(txt_id, "dns-txt-2");
    assert_eq!(txt_data.tunnel_id, "tun-other");
    assert!(!can_use, "should NOT be usable when owned by another tunnel");
}

#[tokio::test]
async fn test_get_managed_dns_txt_no_records() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();
    client.tunnel_id = "tun-aaa".to_string();

    Mock::given(method("GET"))
        .and(path("/zones/zone-555/dns_records"))
        .and(query_param("type", "TXT"))
        .and(query_param("name", "_managed.new.example.com"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_list_ok::<serde_json::Value>(&[])))
        .expect(1)
        .mount(&server)
        .await;

    let (txt_id, _txt_data, can_use) =
        client.get_managed_dns_txt("new.example.com").await.unwrap();

    assert!(txt_id.is_empty(), "txt_id should be empty when no records exist");
    assert!(can_use, "should be usable when no TXT records exist");
}

// ── test_insert_or_update_cname_create ──────────────────────────────────

#[tokio::test]
async fn test_insert_or_update_cname_create() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();
    client.tunnel_id = "tun-aaa".to_string();

    Mock::given(method("POST"))
        .and(path("/zones/zone-555/dns_records"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!({
            "id": "dns-new-1",
            "name": "app.example.com",
            "type": "CNAME",
            "content": "tun-aaa.cfargotunnel.com"
        }))))
        .expect(1)
        .mount(&server)
        .await;

    // Empty dns_id means create
    let dns_id = client
        .insert_or_update_cname("app.example.com", "")
        .await
        .unwrap();
    assert_eq!(dns_id, "dns-new-1");
}

// ── test_insert_or_update_cname_update ──────────────────────────────────

#[tokio::test]
async fn test_insert_or_update_cname_update() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();
    client.tunnel_id = "tun-aaa".to_string();

    Mock::given(method("PUT"))
        .and(path("/zones/zone-555/dns_records/dns-existing-1"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!({
            "id": "dns-existing-1",
            "name": "app.example.com",
            "type": "CNAME",
            "content": "tun-aaa.cfargotunnel.com"
        }))))
        .expect(1)
        .mount(&server)
        .await;

    // Non-empty dns_id means update (PUT)
    let dns_id = client
        .insert_or_update_cname("app.example.com", "dns-existing-1")
        .await
        .unwrap();
    assert_eq!(dns_id, "dns-existing-1");
}

// ── test_insert_or_update_txt ───────────────────────────────────────────

#[tokio::test]
async fn test_insert_or_update_txt_create() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();
    client.tunnel_id = "tun-aaa".to_string();
    client.tunnel_name = "my-tunnel".to_string();

    Mock::given(method("POST"))
        .and(path("/zones/zone-555/dns_records"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!({
            "id": "dns-txt-new",
            "name": "_managed.app.example.com",
            "type": "TXT",
            "content": "ignored"
        }))))
        .expect(1)
        .mount(&server)
        .await;

    // Empty txt_id means create
    client
        .insert_or_update_txt("app.example.com", "", "dns-cname-1")
        .await
        .unwrap();
}

#[tokio::test]
async fn test_insert_or_update_txt_update() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();
    client.tunnel_id = "tun-aaa".to_string();
    client.tunnel_name = "my-tunnel".to_string();

    Mock::given(method("PUT"))
        .and(path("/zones/zone-555/dns_records/dns-txt-existing"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!({
            "id": "dns-txt-existing",
            "name": "_managed.app.example.com",
            "type": "TXT",
            "content": "ignored"
        }))))
        .expect(1)
        .mount(&server)
        .await;

    // Non-empty txt_id means update (PUT)
    client
        .insert_or_update_txt("app.example.com", "dns-txt-existing", "dns-cname-1")
        .await
        .unwrap();
}

// ── test_delete_dns_id ──────────────────────────────────────────────────

#[tokio::test]
async fn test_delete_dns_id_when_created_true() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();

    Mock::given(method("DELETE"))
        .and(path("/zones/zone-555/dns_records/dns-del-1"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!({"id": "dns-del-1"}))))
        .expect(1)
        .mount(&server)
        .await;

    client
        .delete_dns_id("app.example.com", "dns-del-1", true)
        .await
        .unwrap();
}

#[tokio::test]
async fn test_delete_dns_id_skips_when_created_false() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.zone_id = "zone-555".to_string();

    // No mock registered -- if it calls the server, wiremock will give 404
    // and the test will fail. This proves the skip logic works.
    client
        .delete_dns_id("app.example.com", "dns-del-1", false)
        .await
        .unwrap();
}

// ── test_update_tunnel_configuration ────────────────────────────────────

#[tokio::test]
async fn test_update_tunnel_configuration() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.account_id = "acct-111".to_string();
    client.tunnel_id = "tun-aaa".to_string();

    Mock::given(method("PUT"))
        .and(path("/accounts/acct-111/cfd_tunnel/tun-aaa/configurations"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!({}))))
        .expect(1)
        .mount(&server)
        .await;

    let rules = vec![
        TunnelIngressRule {
            hostname: Some("app.example.com".to_string()),
            service: "http://app.default.svc:80".to_string(),
            path: None,
        },
        TunnelIngressRule {
            hostname: None,
            service: "http_status:404".to_string(),
            path: None,
        },
    ];

    client.update_tunnel_configuration(&rules).await.unwrap();
}

// ── test_clear_tunnel_configuration ─────────────────────────────────────

#[tokio::test]
async fn test_clear_tunnel_configuration() {
    let server = MockServer::start().await;

    let mut client = new_client(&server.uri());
    client.account_id = "acct-111".to_string();
    client.tunnel_id = "tun-aaa".to_string();

    Mock::given(method("PUT"))
        .and(path("/accounts/acct-111/cfd_tunnel/tun-aaa/configurations"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_ok(&json!({}))))
        .expect(1)
        .mount(&server)
        .await;

    client.clear_tunnel_configuration().await.unwrap();
}

// ── Auth mode tests ─────────────────────────────────────────────────────

#[tokio::test]
async fn test_api_key_auth_headers() {
    let server = MockServer::start().await;

    Mock::given(method("GET"))
        .and(path("/zones"))
        .and(header("X-Auth-Key", "my-api-key"))
        .and(header("X-Auth-Email", "user@example.com"))
        .respond_with(ResponseTemplate::new(200).set_body_json(cf_list_ok(&[json!({
            "id": "zone-key-1",
            "name": "example.com"
        })])))
        .expect(1)
        .mount(&server)
        .await;

    let mut client =
        CfClient::new_with_key("my-api-key", "user@example.com", Some(&server.uri()));
    let zone_id = client.validate_zone("example.com").await.unwrap();
    assert_eq!(zone_id, "zone-key-1");
}

// ── Error handling tests ────────────────────────────────────────────────

#[tokio::test]
async fn test_validate_account_both_empty_returns_error() {
    let mut client = CfClient::new("token", Some("http://localhost:1"));
    let result = client.validate_account("", "").await;
    assert!(result.is_err());
    let err_msg = format!("{}", result.unwrap_err());
    assert!(err_msg.contains("both account ID and name cannot be empty"));
}

#[tokio::test]
async fn test_validate_zone_empty_domain_returns_error() {
    let mut client = CfClient::new("token", Some("http://localhost:1"));
    let result = client.validate_zone("").await;
    assert!(result.is_err());
    let err_msg = format!("{}", result.unwrap_err());
    assert!(err_msg.contains("domain cannot be empty"));
}
