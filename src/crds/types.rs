use schemars::JsonSchema;
use serde::{Deserialize, Serialize};

/// CloudflareDetails contains all the necessary parameters needed to connect to the Cloudflare API.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct CloudflareDetails {
    /// Cloudflare Domain to which this tunnel belongs to
    pub domain: String,

    /// Secret containing Cloudflare API key/token
    pub secret: String,

    /// Account Name in Cloudflare
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub account_name: String,

    /// Account ID in Cloudflare
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub account_id: String,

    /// Email to use along with API Key
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub email: String,

    /// Key in the secret to use for Cloudflare API Key, defaults to CLOUDFLARE_API_KEY
    #[serde(
        rename = "CLOUDFLARE_API_KEY",
        default = "default_api_key_name",
        skip_serializing_if = "String::is_empty"
    )]
    pub cloudflare_api_key: String,

    /// Key in the secret to use for Cloudflare API token, defaults to CLOUDFLARE_API_TOKEN
    #[serde(
        rename = "CLOUDFLARE_API_TOKEN",
        default = "default_api_token_name",
        skip_serializing_if = "String::is_empty"
    )]
    pub cloudflare_api_token: String,

    /// Key in the secret to use as credentials.json for an existing tunnel
    #[serde(
        rename = "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE",
        default = "default_credential_file_name",
        skip_serializing_if = "String::is_empty"
    )]
    pub cloudflare_tunnel_credential_file: String,

    /// Key in the secret to use as tunnel secret for an existing tunnel
    #[serde(
        rename = "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET",
        default = "default_credential_secret_name",
        skip_serializing_if = "String::is_empty"
    )]
    pub cloudflare_tunnel_credential_secret: String,
}

fn default_api_key_name() -> String {
    "CLOUDFLARE_API_KEY".to_string()
}

fn default_api_token_name() -> String {
    "CLOUDFLARE_API_TOKEN".to_string()
}

fn default_credential_file_name() -> String {
    "CLOUDFLARE_TUNNEL_CREDENTIAL_FILE".to_string()
}

fn default_credential_secret_name() -> String {
    "CLOUDFLARE_TUNNEL_CREDENTIAL_SECRET".to_string()
}

/// ExistingTunnel spec needs either a Tunnel Id or a Name to find it on Cloudflare.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
pub struct ExistingTunnel {
    /// Existing Tunnel ID to run on
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub id: String,

    /// Existing Tunnel name to run on
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
}

/// NewTunnel spec needs a name to create a Tunnel on Cloudflare.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
pub struct NewTunnel {
    /// Tunnel name to create on Cloudflare
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
}

/// TunnelRef defines the Tunnel TunnelBinding connects to.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct TunnelRef {
    /// Kind can be Tunnel or ClusterTunnel
    pub kind: String,

    /// Name of the tunnel resource
    pub name: String,

    /// DisableDNSUpdates disables the DNS updates on Cloudflare
    #[serde(default)]
    pub disable_dns_updates: bool,

    /// CredentialSecretRef is an optional reference to a Secret in a different namespace
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub credential_secret_ref: Option<SecretReference>,
}

/// SecretReference is a reference to a Secret in a specific namespace.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
pub struct SecretReference {
    /// Name of the Secret
    pub name: String,

    /// Namespace of the Secret
    pub namespace: String,
}

/// TunnelBindingSubject defines the subject TunnelBinding connects to the Tunnel.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
pub struct TunnelBindingSubject {
    /// Kind can be Service
    #[serde(default = "default_subject_kind")]
    pub kind: String,

    /// Name of the subject
    pub name: String,

    /// Spec of the subject
    #[serde(default)]
    pub spec: TunnelBindingSubjectSpec,
}

fn default_subject_kind() -> String {
    "Service".to_string()
}

/// OriginRequestSpec exposes the full set of cloudflared originRequest fields for per-service configuration.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct OriginRequestSpec {
    /// Timeout for establishing a new connection to the origin
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub connect_timeout: Option<String>,

    /// Timeout for completing a TLS handshake to the origin
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tls_timeout: Option<String>,

    /// TCP keepalive duration for connections to the origin
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tcp_keep_alive: Option<String>,

    /// Timeout for closing an idle keepalive connection
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub keep_alive_timeout: Option<String>,

    /// Maximum number of idle keepalive connections
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub keep_alive_connections: Option<i64>,

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
    #[serde(default, skip_serializing_if = "Option::is_none")]
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

    /// Disable "happy eyeballs" for IPv4/v6 fallback
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub no_happy_eyeballs: Option<bool>,

    /// Listen address for the proxy
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proxy_address: Option<String>,

    /// Listen port for the proxy
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proxy_port: Option<u32>,

    /// Valid options are 'socks' or empty
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proxy_type: Option<String>,
}

/// TunnelBindingSubjectSpec defines additional configuration for a TunnelBindingSubject.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct TunnelBindingSubjectSpec {
    /// Fqdn specifies the DNS name to access this service from
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub fqdn: String,

    /// Protocol specifies the protocol for the service
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub protocol: String,

    /// Path specifies a regular expression to match on the request
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub path: String,

    /// Target specifies where the tunnel should proxy to
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub target: String,

    /// CaPool trusts the CA certificate referenced by the key in the secret
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub ca_pool: String,

    /// NoTlsVerify disables TLS verification for this service
    #[serde(default)]
    pub no_tls_verify: bool,

    /// Http2Origin makes the service attempt to connect to origin using HTTP2
    #[serde(default)]
    pub http2_origin: bool,

    /// ProxyAddress configures the listen address for the proxy
    #[serde(
        default = "default_proxy_address",
        skip_serializing_if = "String::is_empty"
    )]
    pub proxy_address: String,

    /// ProxyPort configures the listen port for the proxy
    #[serde(default)]
    pub proxy_port: u32,

    /// ProxyType configures the proxy type
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub proxy_type: String,

    /// Full originRequest configuration (takes precedence over legacy flat fields above)
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub origin_request: Option<OriginRequestSpec>,
}

fn default_proxy_address() -> String {
    "127.0.0.1".to_string()
}

/// ServiceInfo stores the Hostname and Target for each service.
#[derive(Clone, Debug, Default, Deserialize, Serialize, JsonSchema)]
pub struct ServiceInfo {
    /// FQDN of the service
    pub hostname: String,

    /// Target for cloudflared
    pub target: String,
}
