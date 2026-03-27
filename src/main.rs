use tracing::info;
use tracing_subscriber::{fmt, EnvFilter};

fn main() {
    fmt()
        .with_env_filter(EnvFilter::from_default_env().add_directive("info".parse().unwrap()))
        .json()
        .init();

    info!("Starting controller...");
}
