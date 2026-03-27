use serde::{Deserialize, Serialize};

/// Configuration is a cloudflared configuration YAML model.
/// <https://github.com/cloudflare/cloudflared/blob/master/config/configuration.go>
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub struct Configuration {
    /// Tunnel ID
    pub tunnel: String,

    /// Ingress rules
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ingress: Vec<UnvalidatedIngressRule>,

    /// WARP routing configuration
    #[serde(
        rename = "warp-routing",
        default,
        skip_serializing_if = "Option::is_none"
    )]
    pub warp_routing: Option<WarpRoutingConfig>,

    /// Global origin request configuration
    #[serde(rename = "originRequest", default, skip_serializing_if = "Option::is_none")]
    pub origin_request: Option<OriginRequestConfig>,

    /// Path to the credentials file
    #[serde(rename = "credentials-file")]
    pub credentials_file: String,

    /// Metrics bind address
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub metrics: Option<String>,

    /// Disable auto-update
    #[serde(rename = "no-autoupdate", default, skip_serializing_if = "Option::is_none")]
    pub no_auto_update: Option<bool>,
}

/// UnvalidatedIngressRule is a cloudflared ingress entry model.
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UnvalidatedIngressRule {
    /// Hostname to match
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hostname: Option<String>,

    /// Path regex to match
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,

    /// Service target URL
    pub service: String,

    /// Per-rule origin request configuration
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub origin_request: Option<OriginRequestConfig>,
}

/// WarpRoutingConfig is a cloudflared WARP routing model.
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub struct WarpRoutingConfig {
    /// Whether WARP routing is enabled
    #[serde(default)]
    pub enabled: bool,
}

/// OriginRequestConfig is a cloudflared origin request configuration model.
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct OriginRequestConfig {
    /// HTTP proxy timeout for establishing a new connection
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub connect_timeout: Option<String>,

    /// HTTP proxy timeout for completing a TLS handshake
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tls_timeout: Option<String>,

    /// HTTP proxy TCP keepalive duration
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tcp_keep_alive: Option<String>,

    /// HTTP proxy should disable "happy eyeballs" for IPv4/v6 fallback
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub no_happy_eyeballs: Option<bool>,

    /// HTTP proxy maximum keepalive connection pool size
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub keep_alive_connections: Option<i64>,

    /// HTTP proxy timeout for closing an idle connection
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub keep_alive_timeout: Option<String>,

    /// Sets the HTTP Host header for the local webserver
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub http_host_header: Option<String>,

    /// Hostname on the origin server certificate
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub origin_server_name: Option<String>,

    /// Path to the CA for the certificate of your origin
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ca_pool: Option<String>,

    /// Disables TLS verification of the certificate presented by your origin
    #[serde(rename = "noTLSVerify", default, skip_serializing_if = "Option::is_none")]
    pub no_tls_verify: Option<bool>,

    /// Attempt to connect to origin using HTTP2
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub http2_origin: Option<bool>,

    /// Disables chunked transfer encoding
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub disable_chunked_encoding: Option<bool>,

    /// Runs as jump host
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bastion_mode: Option<bool>,

    /// Listen address for the proxy
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proxy_address: Option<String>,

    /// Listen port for the proxy
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proxy_port: Option<u32>,

    /// Valid options are 'socks' or empty
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proxy_type: Option<String>,

    /// IP rules for the proxy service
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ip_rules: Vec<IngressIPRule>,
}

/// IngressIPRule is a cloudflared origin ingress IP rule config model.
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub struct IngressIPRule {
    /// IP prefix
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub prefix: Option<String>,

    /// Ports
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ports: Vec<i32>,

    /// Allow
    #[serde(default)]
    pub allow: bool,
}
