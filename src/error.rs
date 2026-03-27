/// Operator error type.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("Kubernetes API error: {0}")]
    Kube(#[from] kube::Error),

    #[error("Cloudflare API error: {0}")]
    Cloudflare(String),

    #[error("Configuration error: {0}")]
    Config(String),

    #[error("Serialization error: {0}")]
    Serialization(#[from] serde_json::Error),

    #[error("Missing field: {0}")]
    MissingField(String),
}

pub type Result<T, E = Error> = std::result::Result<T, E>;
