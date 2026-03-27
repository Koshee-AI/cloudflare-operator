use kube::CustomResource;
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};

use super::types::{CloudflareDetails, ExistingTunnel, NewTunnel};

/// TunnelSpec defines the desired state of Tunnel.
#[derive(CustomResource, Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
#[kube(
    group = "networking.cfargotunnel.com",
    version = "v1alpha2",
    kind = "Tunnel",
    namespaced,
    status = "TunnelStatus",
    printcolumn = r#"{"name":"TunnelID","type":"string","jsonPath":".status.tunnelId"}"#
)]
#[serde(rename_all = "camelCase")]
pub struct TunnelSpec {
    /// Deployment patch for the cloudflared deployment
    #[serde(default = "default_deploy_patch")]
    pub deploy_patch: String,

    /// NoTlsVerify disables origin TLS certificate checks when the endpoint is HTTPS
    #[serde(default)]
    pub no_tls_verify: bool,

    /// OriginCaPool specifies the secret with tls.crt of the Root CA
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub origin_ca_pool: String,

    /// Protocol specifies the protocol to use for the tunnel
    #[serde(default = "default_protocol")]
    pub protocol: String,

    /// FallbackTarget specifies the target for requests that do not match an ingress
    #[serde(default = "default_fallback_target")]
    pub fallback_target: String,

    /// Cloudflare Credentials
    pub cloudflare: CloudflareDetails,

    /// Existing tunnel object
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub existing_tunnel: Option<ExistingTunnel>,

    /// New tunnel object
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub new_tunnel: Option<NewTunnel>,
}

/// TunnelStatus defines the observed state of Tunnel.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct TunnelStatus {
    pub tunnel_id: String,
    pub tunnel_name: String,
    pub account_id: String,
    pub zone_id: String,
}

fn default_deploy_patch() -> String {
    "{}".to_string()
}

fn default_protocol() -> String {
    "auto".to_string()
}

fn default_fallback_target() -> String {
    "http_status:404".to_string()
}
