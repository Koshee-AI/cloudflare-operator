use std::collections::{BTreeMap, HashSet};
use std::sync::Arc;
use std::time::Duration;

use k8s_openapi::api::apps::v1::Deployment;
use k8s_openapi::api::core::v1::{ConfigMap, Secret, Service};
use kube::ResourceExt;
use kube::api::{Api, ListParams, Patch, PatchParams};
use kube::runtime::controller::Action;
use md5::{Digest, Md5};
use tracing::{error, info, warn};

use crate::cloudflare::client::CfClient;
use crate::cloudflare::types::TunnelIngressRule;
use crate::config::cloudflared::{Configuration, UnvalidatedIngressRule};
use crate::crds::cluster_tunnel::ClusterTunnel;
use crate::crds::gateway::{
    Gateway, GatewayClass, GatewayCondition, GatewayStatus, HTTPBackendRef, HTTPRoute,
    HTTPRouteStatus, RouteParentStatus,
};
use crate::crds::tunnel::Tunnel;
use crate::crds::tunnel_binding::TunnelBinding;
use crate::crds::types::{CloudflareDetails, ServiceInfo};
use crate::error::Error;

use super::context::Context;

pub const CONTROLLER_NAME: &str = "cfargotunnel.com/cloudflare-operator";
const TUNNEL_FINALIZER: &str = "cfargotunnel.com/gateway-finalizer";
const CONFIGMAP_KEY: &str = "config.yaml";
const TUNNEL_CONFIG_CHECKSUM: &str = "cfargotunnel.com/checksum";
const TUNNEL_NAME_LABEL: &str = "cfargotunnel.com/name";
const TUNNEL_KIND_LABEL: &str = "cfargotunnel.com/kind";

const ANNOTATION_TUNNEL_NAME: &str = "cfargotunnel.com/tunnel-name";
const ANNOTATION_TUNNEL_KIND: &str = "cfargotunnel.com/tunnel-kind";

// ── GatewayClass controller ─────────────────────────────────────────────

pub async fn reconcile_gateway_class(
    obj: Arc<GatewayClass>,
    _ctx: Arc<Context>,
) -> Result<Action, Error> {
    let name = obj.name_any();

    if obj.spec.controller_name != CONTROLLER_NAME {
        info!(name = %name, controller = %obj.spec.controller_name, "ignoring GatewayClass with different controller");
        return Ok(Action::await_change());
    }

    info!(name = %name, "reconciling GatewayClass");

    let generation = obj.metadata.generation.unwrap_or(0);
    let now = chrono::Utc::now().format("%Y-%m-%dT%H:%M:%SZ").to_string();

    let status = serde_json::json!({
        "status": {
            "conditions": [
                {
                    "type": "Accepted",
                    "status": "True",
                    "reason": "Accepted",
                    "message": "GatewayClass accepted by cloudflare-operator",
                    "lastTransitionTime": now,
                    "observedGeneration": generation
                }
            ]
        }
    });

    let api: Api<GatewayClass> = Api::all(_ctx.client.clone());
    api.patch_status(
        &name,
        &PatchParams::apply("cloudflare-operator"),
        &Patch::Merge(&status),
    )
    .await?;

    info!(name = %name, "GatewayClass accepted");
    Ok(Action::requeue(Duration::from_secs(300)))
}

pub fn gateway_class_error_policy(
    _obj: Arc<GatewayClass>,
    error: &Error,
    _ctx: Arc<Context>,
) -> Action {
    error!(error = %error, "GatewayClass reconciliation error, will retry");
    Action::requeue(Duration::from_secs(15))
}

// ── Gateway controller ──────────────────────────────────────────────────

pub async fn reconcile_gateway(obj: Arc<Gateway>, ctx: Arc<Context>) -> Result<Action, Error> {
    let k8s = &ctx.client;
    let name = obj.name_any();
    let ns = obj.metadata.namespace.clone().unwrap_or_default();

    info!(name = %name, ns = %ns, "reconciling Gateway");

    // Verify the referenced GatewayClass is ours
    let gc_api: Api<GatewayClass> = Api::all(k8s.clone());
    let gc = gc_api.get(&obj.spec.gateway_class_name).await.map_err(|e| {
        error!(gateway_class = %obj.spec.gateway_class_name, error = %e, "failed to get GatewayClass");
        e
    })?;

    if gc.spec.controller_name != CONTROLLER_NAME {
        info!(name = %name, "Gateway references GatewayClass with different controller, ignoring");
        return Ok(Action::await_change());
    }

    // Check for tunnel annotation
    let annotations = obj.metadata.annotations.as_ref();
    let tunnel_name = annotations
        .and_then(|a| a.get(ANNOTATION_TUNNEL_NAME))
        .cloned()
        .unwrap_or_default();
    let tunnel_kind = annotations
        .and_then(|a| a.get(ANNOTATION_TUNNEL_KIND))
        .cloned()
        .unwrap_or_else(|| "Tunnel".to_string());

    let generation = obj.metadata.generation.unwrap_or(0);
    let now = chrono::Utc::now().format("%Y-%m-%dT%H:%M:%SZ").to_string();

    if tunnel_name.is_empty() {
        let status = serde_json::json!({
            "status": {
                "conditions": [
                    {
                        "type": "Accepted",
                        "status": "False",
                        "reason": "MissingTunnelAnnotation",
                        "message": format!("Gateway requires annotation {ANNOTATION_TUNNEL_NAME}"),
                        "lastTransitionTime": now,
                        "observedGeneration": generation
                    },
                    {
                        "type": "Programmed",
                        "status": "False",
                        "reason": "MissingTunnelAnnotation",
                        "message": format!("Gateway requires annotation {ANNOTATION_TUNNEL_NAME}"),
                        "lastTransitionTime": now,
                        "observedGeneration": generation
                    }
                ]
            }
        });

        let gw_api: Api<Gateway> = Api::namespaced(k8s.clone(), &ns);
        gw_api
            .patch_status(
                &name,
                &PatchParams::apply("cloudflare-operator"),
                &Patch::Merge(&status),
            )
            .await?;

        return Ok(Action::requeue(Duration::from_secs(60)));
    }

    // Verify the tunnel exists
    let tunnel_exists = match tunnel_kind.to_lowercase().as_str() {
        "tunnel" => {
            let api: Api<Tunnel> = Api::namespaced(k8s.clone(), &ns);
            api.get_opt(&tunnel_name).await?.is_some()
        }
        "clustertunnel" => {
            let api: Api<ClusterTunnel> = Api::all(k8s.clone());
            api.get_opt(&tunnel_name).await?.is_some()
        }
        _ => false,
    };

    let (accepted_status, accepted_reason, accepted_msg) = if tunnel_exists {
        (
            "True",
            "Accepted",
            "Gateway accepted and linked to tunnel".to_string(),
        )
    } else {
        (
            "False",
            "TunnelNotFound",
            format!("{tunnel_kind}/{tunnel_name} not found"),
        )
    };

    let (programmed_status, programmed_reason, programmed_msg) = if tunnel_exists {
        ("True", "Programmed", "Gateway is programmed".to_string())
    } else {
        (
            "False",
            "TunnelNotFound",
            format!("{tunnel_kind}/{tunnel_name} not found"),
        )
    };

    let status = serde_json::json!({
        "status": {
            "conditions": [
                {
                    "type": "Accepted",
                    "status": accepted_status,
                    "reason": accepted_reason,
                    "message": accepted_msg,
                    "lastTransitionTime": now,
                    "observedGeneration": generation
                },
                {
                    "type": "Programmed",
                    "status": programmed_status,
                    "reason": programmed_reason,
                    "message": programmed_msg,
                    "lastTransitionTime": now,
                    "observedGeneration": generation
                }
            ]
        }
    });

    let gw_api: Api<Gateway> = Api::namespaced(k8s.clone(), &ns);
    gw_api
        .patch_status(
            &name,
            &PatchParams::apply("cloudflare-operator"),
            &Patch::Merge(&status),
        )
        .await?;

    info!(name = %name, tunnel = %tunnel_name, kind = %tunnel_kind, "Gateway status updated");
    Ok(Action::requeue(Duration::from_secs(300)))
}

pub fn gateway_error_policy(_obj: Arc<Gateway>, error: &Error, _ctx: Arc<Context>) -> Action {
    error!(error = %error, "Gateway reconciliation error, will retry");
    Action::requeue(Duration::from_secs(15))
}

// ── HTTPRoute controller ────────────────────────────────────────────────

/// Resolved tunnel info for HTTPRoute reconciliation.
struct RouteTunnelInfo {
    fallback_target: String,
    cloudflare: CloudflareDetails,
    tunnel_id: String,
    tunnel_name: String,
    account_id: String,
    zone_id: String,
    tunnel_ns: String,
}

pub async fn reconcile_httproute(obj: Arc<HTTPRoute>, ctx: Arc<Context>) -> Result<Action, Error> {
    let k8s = &ctx.client;
    let route_ns = obj.metadata.namespace.clone().unwrap_or_default();
    let route_name = obj.name_any();

    info!(name = %route_name, ns = %route_ns, "reconciling HTTPRoute");

    // Find a parent Gateway that we manage
    let (gateway, gateway_name, gateway_ns) = find_managed_gateway(k8s, &obj, &route_ns).await?;

    let annotations = gateway.metadata.annotations.as_ref();
    let tunnel_name = annotations
        .and_then(|a| a.get(ANNOTATION_TUNNEL_NAME))
        .cloned()
        .ok_or_else(|| {
            Error::Config(format!(
                "Gateway {gateway_name} missing annotation {ANNOTATION_TUNNEL_NAME}"
            ))
        })?;
    let tunnel_kind = annotations
        .and_then(|a| a.get(ANNOTATION_TUNNEL_KIND))
        .cloned()
        .unwrap_or_else(|| "Tunnel".to_string());

    // Resolve tunnel info
    let tunnel_info = get_tunnel_info(
        k8s,
        &tunnel_name,
        &tunnel_kind,
        &gateway_ns,
        &ctx.cluster_resource_namespace,
    )
    .await?;

    // Build CfClient
    let (secret_name, secret_ns) = (
        tunnel_info.cloudflare.secret.clone(),
        tunnel_info.tunnel_ns.clone(),
    );
    let secrets_api: Api<Secret> = Api::namespaced(k8s.clone(), &secret_ns);
    let cf_secret = secrets_api.get(&secret_name).await.map_err(|e| {
        error!(secret = %secret_name, ns = %secret_ns, error = %e, "failed to read cloudflare secret");
        e
    })?;

    let mut cf_client = build_cf_client(
        &tunnel_info.cloudflare,
        &cf_secret,
        ctx.cloudflare_api_base_url.as_deref(),
    )?;
    cf_client.domain = tunnel_info.cloudflare.domain.clone();
    cf_client.account_id = tunnel_info.account_id.clone();
    cf_client.tunnel_id = tunnel_info.tunnel_id.clone();
    cf_client.tunnel_name = tunnel_info.tunnel_name.clone();
    cf_client.zone_id = tunnel_info.zone_id.clone();

    cf_client
        .validate_zone(&tunnel_info.cloudflare.domain)
        .await?;

    // Handle deletion
    if obj.metadata.deletion_timestamp.is_some() {
        return handle_httproute_deletion(k8s, &obj, &route_ns, &cf_client).await;
    }

    // Resolve hostnames and backend targets from HTTPRoute spec
    let services = resolve_httproute_services(k8s, &obj, &route_ns, &cf_client).await?;

    // Get the old services from status for FQDN change detection
    let old_hostnames: HashSet<String> = obj
        .status
        .as_ref()
        .map(|s| {
            s.parents
                .iter()
                .flat_map(|p| {
                    p.conditions
                        .iter()
                        .filter(|c| c.type_ == "Accepted" && c.status == "True")
                        .map(|_| p.parent_ref.name.clone())
                })
                .collect()
        })
        .unwrap_or_default();

    // Ensure finalizer
    let has_finalizer = obj
        .metadata
        .finalizers
        .as_ref()
        .map_or(false, |f| f.contains(&TUNNEL_FINALIZER.to_string()));

    if !has_finalizer {
        let mut finalizers = obj.metadata.finalizers.clone().unwrap_or_default();
        finalizers.push(TUNNEL_FINALIZER.to_string());
        let patch = serde_json::json!({
            "metadata": { "finalizers": finalizers }
        });
        let api: Api<HTTPRoute> = Api::namespaced(k8s.clone(), &route_ns);
        api.patch(
            &route_name,
            &PatchParams::apply("cloudflare-operator"),
            &Patch::Merge(&patch),
        )
        .await?;
        info!(name = %route_name, "finalizer added to HTTPRoute");
    }

    // Create/update DNS records for each hostname
    let mut had_errors = false;
    for svc in &services {
        if svc.hostname.is_empty() {
            continue;
        }
        if let Err(e) =
            create_dns_for_hostname(&cf_client, &svc.hostname, ctx.overwrite_unmanaged).await
        {
            error!(hostname = %svc.hostname, error = %e, "failed to create/update DNS for HTTPRoute");
            had_errors = true;
        }
    }

    // Detect hostname changes and clean up old DNS
    let new_hostnames: HashSet<String> = services.iter().map(|s| s.hostname.clone()).collect();
    for old_host in &old_hostnames {
        if !old_host.is_empty() && !new_hostnames.contains(old_host) {
            info!(hostname = %old_host, "HTTPRoute hostname changed, cleaning up old DNS");
            if let Err(e) = delete_dns_for_hostname(&cf_client, old_host).await {
                warn!(hostname = %old_host, error = %e, "failed to clean up old DNS");
                had_errors = true;
            }
        }
    }

    if had_errors {
        return Err(Error::Cloudflare(
            "some DNS entries failed for HTTPRoute".into(),
        ));
    }

    // Rebuild ConfigMap with ingress rules from BOTH TunnelBindings AND HTTPRoutes
    rebuild_combined_tunnel_config(
        k8s,
        &tunnel_name,
        &tunnel_kind,
        &tunnel_info.tunnel_ns,
        &tunnel_info.fallback_target,
        &cf_client,
    )
    .await?;

    // Update HTTPRoute status
    update_httproute_status(
        k8s,
        &obj,
        &route_name,
        &route_ns,
        &gateway_name,
        &gateway_ns,
    )
    .await?;

    info!(name = %route_name, service_count = services.len(), "HTTPRoute reconciled");
    Ok(Action::requeue(Duration::from_secs(300)))
}

pub fn httproute_error_policy(_obj: Arc<HTTPRoute>, error: &Error, _ctx: Arc<Context>) -> Action {
    error!(error = %error, "HTTPRoute reconciliation error, will retry");
    Action::requeue(Duration::from_secs(15))
}

// ── Find managed Gateway from parent refs ───────────────────────────────

async fn find_managed_gateway(
    k8s: &kube::Client,
    route: &HTTPRoute,
    route_ns: &str,
) -> Result<(Gateway, String, String), Error> {
    let gc_api: Api<GatewayClass> = Api::all(k8s.clone());

    for parent in &route.spec.parent_refs {
        if parent.group != "gateway.networking.k8s.io" && !parent.group.is_empty() {
            continue;
        }
        if parent.kind != "Gateway" && !parent.kind.is_empty() {
            continue;
        }

        let gw_ns = parent.namespace.as_deref().unwrap_or(route_ns);
        let gw_api: Api<Gateway> = Api::namespaced(k8s.clone(), gw_ns);
        let gw = match gw_api.get_opt(&parent.name).await? {
            Some(gw) => gw,
            None => continue,
        };

        // Check if GatewayClass is ours
        match gc_api.get_opt(&gw.spec.gateway_class_name).await? {
            Some(gc) if gc.spec.controller_name == CONTROLLER_NAME => {
                return Ok((gw, parent.name.clone(), gw_ns.to_string()));
            }
            _ => continue,
        }
    }

    Err(Error::Config(
        "no parent Gateway managed by this controller found for HTTPRoute".into(),
    ))
}

// ── Tunnel info resolution ──────────────────────────────────────────────

async fn get_tunnel_info(
    k8s: &kube::Client,
    tunnel_name: &str,
    tunnel_kind: &str,
    gateway_ns: &str,
    cluster_resource_namespace: &str,
) -> Result<RouteTunnelInfo, Error> {
    match tunnel_kind.to_lowercase().as_str() {
        "tunnel" => {
            let api: Api<Tunnel> = Api::namespaced(k8s.clone(), gateway_ns);
            let tunnel = api.get(tunnel_name).await?;
            let status = tunnel.status.clone().unwrap_or_default();
            Ok(RouteTunnelInfo {
                fallback_target: tunnel.spec.fallback_target.clone(),
                cloudflare: tunnel.spec.cloudflare.clone(),
                tunnel_id: status.tunnel_id,
                tunnel_name: status.tunnel_name,
                account_id: status.account_id,
                zone_id: status.zone_id,
                tunnel_ns: gateway_ns.to_string(),
            })
        }
        "clustertunnel" => {
            let api: Api<ClusterTunnel> = Api::all(k8s.clone());
            let ct = api.get(tunnel_name).await?;
            let status = ct.status.clone().unwrap_or_default();
            Ok(RouteTunnelInfo {
                fallback_target: ct.spec.fallback_target.clone(),
                cloudflare: ct.spec.cloudflare.clone(),
                tunnel_id: status.tunnel_id,
                tunnel_name: status.tunnel_name,
                account_id: status.account_id,
                zone_id: status.zone_id,
                tunnel_ns: cluster_resource_namespace.to_string(),
            })
        }
        other => Err(Error::Config(format!(
            "unsupported tunnel kind in Gateway annotation: {other}"
        ))),
    }
}

// ── Build CfClient from K8s Secret ──────────────────────────────────────

fn build_cf_client(
    cf: &CloudflareDetails,
    secret: &Secret,
    base_url: Option<&str>,
) -> Result<CfClient, Error> {
    let data = secret
        .data
        .as_ref()
        .ok_or_else(|| Error::MissingField("cloudflare secret has no data".into()))?;

    let api_token = data
        .get(&cf.cloudflare_api_token)
        .map(|b| String::from_utf8_lossy(&b.0).to_string());
    let api_key = data
        .get(&cf.cloudflare_api_key)
        .map(|b| String::from_utf8_lossy(&b.0).to_string());

    if api_token.is_none() && api_key.is_none() {
        return Err(Error::MissingField(format!(
            "neither {} nor {} found in secret {}",
            cf.cloudflare_api_token, cf.cloudflare_api_key, cf.secret
        )));
    }

    if let Some(token) = api_token {
        Ok(CfClient::new(&token, base_url))
    } else {
        let key = api_key.unwrap();
        Ok(CfClient::new_with_key(&key, &cf.email, base_url))
    }
}

// ── Resolve HTTPRoute to ServiceInfo ────────────────────────────────────

async fn resolve_httproute_services(
    k8s: &kube::Client,
    route: &HTTPRoute,
    route_ns: &str,
    cf_client: &CfClient,
) -> Result<Vec<ServiceInfo>, Error> {
    let svc_api: Api<Service> = Api::namespaced(k8s.clone(), route_ns);
    let hostnames = route.spec.hostnames.as_deref().unwrap_or_default();
    let mut services = Vec::new();

    for rule in &route.spec.rules {
        // Determine the target service from backendRefs
        let target = resolve_backend_target(k8s, &svc_api, &rule.backend_refs, route_ns).await;

        // Determine the path from matches
        let path = extract_path_from_matches(&rule.matches);

        if hostnames.is_empty() {
            // No explicit hostnames -- use default domain
            services.push(ServiceInfo {
                hostname: cf_client.domain.clone(),
                target: target.clone(),
            });
        } else {
            for hostname in hostnames {
                services.push(ServiceInfo {
                    hostname: hostname.clone(),
                    target: target.clone(),
                });
            }
        }

        // Store path info in a way we can retrieve later for ConfigMap building
        // The path will be handled during ConfigMap rebuild by re-reading the HTTPRoute spec
        let _ = path;
    }

    // Deduplicate by hostname
    let mut seen = HashSet::new();
    services.retain(|s| seen.insert(s.hostname.clone()));

    Ok(services)
}

async fn resolve_backend_target(
    k8s: &kube::Client,
    default_svc_api: &Api<Service>,
    backend_refs: &[HTTPBackendRef],
    route_ns: &str,
) -> String {
    if backend_refs.is_empty() {
        return "http_status:404".to_string();
    }

    let backend = &backend_refs[0];
    if backend.kind != "Service" && !backend.kind.is_empty() {
        warn!(kind = %backend.kind, "unsupported backendRef kind, using 404");
        return "http_status:404".to_string();
    }

    let svc_ns = backend.namespace.as_deref().unwrap_or(route_ns);
    let svc_api = if svc_ns != route_ns {
        Api::namespaced(k8s.clone(), svc_ns)
    } else {
        default_svc_api.clone()
    };

    match svc_api.get(&backend.name).await {
        Ok(svc) => {
            let port = if let Some(p) = backend.port {
                p as i32
            } else {
                svc.spec
                    .as_ref()
                    .and_then(|s| s.ports.as_ref())
                    .and_then(|ports| ports.first())
                    .map(|p| p.port)
                    .unwrap_or(80)
            };

            let protocol = select_protocol_for_port(port);
            format!(
                "{protocol}://{}:{port}",
                format!("{}.{svc_ns}.svc", backend.name)
            )
        }
        Err(e) => {
            warn!(
                service = %backend.name,
                ns = %svc_ns,
                error = %e,
                "failed to get service for HTTPRoute backendRef"
            );
            "http_status:404".to_string()
        }
    }
}

fn select_protocol_for_port(port: i32) -> &'static str {
    match port {
        443 => "https",
        22 => "ssh",
        139 | 445 => "smb",
        3389 => "rdp",
        _ => "http",
    }
}

fn extract_path_from_matches(matches: &[crate::crds::gateway::HTTPRouteMatch]) -> Option<String> {
    for m in matches {
        if let Some(path_match) = &m.path {
            let path = match path_match.type_.as_str() {
                "Exact" => format!("^{}$", regex_escape(&path_match.value)),
                "PathPrefix" => {
                    if path_match.value == "/" {
                        None?
                    } else {
                        format!("^{}", regex_escape(&path_match.value))
                    }
                }
                "RegularExpression" => path_match.value.clone(),
                _ => continue,
            };
            return Some(path);
        }
    }
    None
}

fn regex_escape(s: &str) -> String {
    let mut result = String::with_capacity(s.len() + 8);
    for c in s.chars() {
        match c {
            '.' | '+' | '*' | '?' | '(' | ')' | '[' | ']' | '{' | '}' | '\\' | '|' | '^' | '$' => {
                result.push('\\');
                result.push(c);
            }
            _ => result.push(c),
        }
    }
    result
}

// ── HTTPRoute deletion ──────────────────────────────────────────────────

async fn handle_httproute_deletion(
    k8s: &kube::Client,
    route: &HTTPRoute,
    route_ns: &str,
    cf_client: &CfClient,
) -> Result<Action, Error> {
    let name = route.name_any();
    let has_finalizer = route
        .metadata
        .finalizers
        .as_ref()
        .map_or(false, |f| f.contains(&TUNNEL_FINALIZER.to_string()));

    if !has_finalizer {
        return Ok(Action::requeue(Duration::from_secs(1)));
    }

    info!(name = %name, "running deletion logic for HTTPRoute");

    let hostnames = route.spec.hostnames.as_deref().unwrap_or_default();
    let mut had_errors = false;
    for hostname in hostnames {
        if let Err(e) = delete_dns_for_hostname(cf_client, hostname).await {
            error!(hostname = %hostname, error = %e, "failed to delete DNS during HTTPRoute finalization");
            had_errors = true;
        }
    }

    if had_errors {
        return Err(Error::Cloudflare(
            "errors occurred during HTTPRoute DNS cleanup, will retry".into(),
        ));
    }

    // Remove finalizer
    let new_finalizers: Vec<String> = route
        .metadata
        .finalizers
        .as_ref()
        .map(|f| {
            f.iter()
                .filter(|fin| fin.as_str() != TUNNEL_FINALIZER)
                .cloned()
                .collect()
        })
        .unwrap_or_default();

    let patch = serde_json::json!({
        "metadata": {
            "finalizers": new_finalizers
        }
    });
    let api: Api<HTTPRoute> = Api::namespaced(k8s.clone(), route_ns);
    api.patch(
        &name,
        &PatchParams::apply("cloudflare-operator"),
        &Patch::Merge(&patch),
    )
    .await?;

    info!(name = %name, "finalizer removed from HTTPRoute");
    Ok(Action::requeue(Duration::from_secs(1)))
}

// ── DNS helpers ─────────────────────────────────────────────────────────

async fn create_dns_for_hostname(
    cf_client: &CfClient,
    hostname: &str,
    overwrite_unmanaged: bool,
) -> Result<(), Error> {
    let (txt_id, mut txt_data, can_use) = cf_client.get_managed_dns_txt(hostname).await?;
    if !can_use {
        return Err(Error::Cloudflare(format!(
            "FQDN {hostname} already managed by tunnel {} ({})",
            txt_data.tunnel_name, txt_data.tunnel_id
        )));
    }

    match cf_client.get_dns_cname_id(hostname).await {
        Ok(existing_id) if !existing_id.is_empty() => {
            if !overwrite_unmanaged && txt_id.is_empty() {
                return Err(Error::Cloudflare(format!(
                    "unmanaged FQDN {hostname} present and overwrite_unmanaged is false"
                )));
            }
            txt_data.dns_id = existing_id;
        }
        _ => {}
    }

    let new_dns_id = cf_client
        .insert_or_update_cname(hostname, &txt_data.dns_id)
        .await?;

    if let Err(e) = cf_client
        .insert_or_update_txt(hostname, &txt_id, &new_dns_id)
        .await
    {
        error!(hostname = %hostname, error = %e, "failed to insert/update TXT entry");
        if let Err(del_err) = cf_client
            .delete_dns_id(hostname, &new_dns_id, !txt_data.dns_id.is_empty())
            .await
        {
            error!(hostname = %hostname, error = %del_err, "failed to roll back CNAME after TXT failure");
        }
        return Err(e);
    }

    info!(hostname = %hostname, "DNS CNAME+TXT records created/updated for HTTPRoute");
    Ok(())
}

async fn delete_dns_for_hostname(cf_client: &CfClient, hostname: &str) -> Result<(), Error> {
    let (txt_id, txt_data, can_use) = match cf_client.get_managed_dns_txt(hostname).await {
        Ok(result) => result,
        Err(e) => {
            warn!(hostname = %hostname, error = %e, "failed to read managed TXT, skipping DNS cleanup");
            return Ok(());
        }
    };

    if !can_use {
        warn!(hostname = %hostname, "TXT record belongs to different tunnel, skipping cleanup");
        return Ok(());
    }

    if !txt_data.dns_id.is_empty() {
        match cf_client.get_dns_cname_id(hostname).await {
            Ok(cname_id) => {
                if cname_id != txt_data.dns_id {
                    error!(
                        hostname = %hostname,
                        cname_id = %cname_id,
                        txt_dns_id = %txt_data.dns_id,
                        "DNS ID from TXT and real DNS record do not match"
                    );
                    return Err(Error::Cloudflare(format!(
                        "DNS/TXT ID mismatch for {hostname}"
                    )));
                }
                cf_client
                    .delete_dns_id(hostname, &txt_data.dns_id, true)
                    .await?;
                info!(hostname = %hostname, "deleted DNS CNAME record");
            }
            Err(e) => {
                warn!(hostname = %hostname, error = %e, "error fetching DNS CNAME record");
            }
        }
    }

    if !txt_id.is_empty() {
        cf_client.delete_dns_id(hostname, &txt_id, true).await?;
        info!(hostname = %hostname, "deleted DNS TXT record");
    }

    Ok(())
}

// ── Update HTTPRoute status ─────────────────────────────────────────────

async fn update_httproute_status(
    k8s: &kube::Client,
    route: &HTTPRoute,
    route_name: &str,
    route_ns: &str,
    gateway_name: &str,
    gateway_ns: &str,
) -> Result<(), Error> {
    let generation = route.metadata.generation.unwrap_or(0);
    let now = chrono::Utc::now().format("%Y-%m-%dT%H:%M:%SZ").to_string();

    let status = serde_json::json!({
        "status": {
            "parents": [
                {
                    "parentRef": {
                        "group": "gateway.networking.k8s.io",
                        "kind": "Gateway",
                        "name": gateway_name,
                        "namespace": gateway_ns
                    },
                    "controllerName": CONTROLLER_NAME,
                    "conditions": [
                        {
                            "type": "Accepted",
                            "status": "True",
                            "reason": "Accepted",
                            "message": "HTTPRoute accepted by cloudflare-operator",
                            "lastTransitionTime": now,
                            "observedGeneration": generation
                        },
                        {
                            "type": "ResolvedRefs",
                            "status": "True",
                            "reason": "ResolvedRefs",
                            "message": "All backend references resolved",
                            "lastTransitionTime": now,
                            "observedGeneration": generation
                        }
                    ]
                }
            ]
        }
    });

    let api: Api<HTTPRoute> = Api::namespaced(k8s.clone(), route_ns);
    api.patch_status(
        route_name,
        &PatchParams::apply("cloudflare-operator"),
        &Patch::Merge(&status),
    )
    .await?;

    Ok(())
}

// ── Combined ConfigMap rebuild (TunnelBindings + HTTPRoutes) ─────────────

pub async fn rebuild_combined_tunnel_config(
    k8s: &kube::Client,
    tunnel_name: &str,
    tunnel_kind: &str,
    tunnel_ns: &str,
    fallback_target: &str,
    cf_client: &CfClient,
) -> Result<(), Error> {
    let cm_api: Api<ConfigMap> = Api::namespaced(k8s.clone(), tunnel_ns);
    let cm = match cm_api.get_opt(tunnel_name).await? {
        Some(cm) => cm,
        None => return Ok(()),
    };

    let config_str = cm
        .data
        .as_ref()
        .and_then(|d| d.get(CONFIGMAP_KEY))
        .ok_or_else(|| Error::Config(format!("key {CONFIGMAP_KEY} not found in ConfigMap")))?;

    let mut config: Configuration = serde_yaml::from_str(config_str)
        .map_err(|e| Error::Config(format!("failed to parse cloudflared config: {e}")))?;

    let mut final_ingresses: Vec<UnvalidatedIngressRule> = Vec::new();

    // 1. Collect ingress rules from TunnelBindings
    let label_selector =
        format!("{TUNNEL_NAME_LABEL}={tunnel_name},{TUNNEL_KIND_LABEL}={tunnel_kind}");
    let lp = ListParams::default().labels(&label_selector);
    let binding_api: Api<TunnelBinding> = Api::all(k8s.clone());
    let binding_list = binding_api.list(&lp).await?;

    let mut bindings = binding_list.items;
    bindings.sort_by(|a, b| a.name_any().cmp(&b.name_any()));

    for binding in &bindings {
        if let Some(status) = &binding.status {
            for (i, subject) in binding.subjects.iter().enumerate() {
                if i >= status.services.len() {
                    continue;
                }
                let target_service = if !subject.spec.target.is_empty() {
                    subject.spec.target.clone()
                } else {
                    status.services[i].target.clone()
                };

                let mut origin_req = crate::config::cloudflared::OriginRequestConfig::default();
                origin_req.no_tls_verify = Some(subject.spec.no_tls_verify);
                origin_req.http2_origin = Some(subject.spec.http2_origin);
                if !subject.spec.proxy_address.is_empty() {
                    origin_req.proxy_address = Some(subject.spec.proxy_address.clone());
                }
                if subject.spec.proxy_port != 0 {
                    origin_req.proxy_port = Some(subject.spec.proxy_port);
                }
                if !subject.spec.proxy_type.is_empty() {
                    origin_req.proxy_type = Some(subject.spec.proxy_type.clone());
                }
                if !subject.spec.ca_pool.is_empty() {
                    origin_req.ca_pool =
                        Some(format!("/etc/cloudflared/certs/{}", subject.spec.ca_pool));
                }

                final_ingresses.push(UnvalidatedIngressRule {
                    hostname: Some(status.services[i].hostname.clone()),
                    service: target_service,
                    path: if subject.spec.path.is_empty() {
                        None
                    } else {
                        Some(subject.spec.path.clone())
                    },
                    origin_request: Some(origin_req),
                });
            }
        }
    }

    // 2. Collect ingress rules from HTTPRoutes referencing Gateways that link to this tunnel
    let httproute_rules =
        collect_httproute_ingress_rules(k8s, tunnel_name, tunnel_kind, cf_client).await?;
    final_ingresses.extend(httproute_rules);

    // Sort deterministically: by hostname, then path
    final_ingresses.sort_by(|a, b| {
        let ha = a.hostname.as_deref().unwrap_or("");
        let hb = b.hostname.as_deref().unwrap_or("");
        ha.cmp(hb).then_with(|| {
            let pa = a.path.as_deref().unwrap_or("");
            let pb = b.path.as_deref().unwrap_or("");
            pa.cmp(pb)
        })
    });

    // Append catch-all
    final_ingresses.push(UnvalidatedIngressRule {
        hostname: None,
        service: fallback_target.to_string(),
        path: None,
        origin_request: None,
    });

    config.ingress = final_ingresses;

    // Push to CF edge (best-effort)
    let edge_rules: Vec<TunnelIngressRule> = config
        .ingress
        .iter()
        .map(|r| TunnelIngressRule {
            hostname: r.hostname.clone(),
            service: r.service.clone(),
            path: r.path.clone(),
        })
        .collect();
    if let Err(e) = cf_client.update_tunnel_configuration(&edge_rules).await {
        warn!(error = %e, "failed to sync configuration to cloudflare edge");
    }

    // Marshal new config
    let new_config_str = serde_yaml::to_string(&config)
        .map_err(|e| Error::Config(format!("failed to serialize config: {e}")))?;

    // Only update if content changed
    if config_str == &new_config_str {
        return Ok(());
    }

    let cm_patch = serde_json::json!({
        "data": {
            CONFIGMAP_KEY: new_config_str
        }
    });
    cm_api
        .patch(
            tunnel_name,
            &PatchParams::apply("cloudflare-operator"),
            &Patch::Merge(&cm_patch),
        )
        .await?;

    // Update deployment checksum to trigger pod restart
    let new_checksum = compute_md5(&new_config_str);
    let deploy_api: Api<Deployment> = Api::namespaced(k8s.clone(), tunnel_ns);
    if let Ok(Some(dep)) = deploy_api.get_opt(tunnel_name).await {
        let current_checksum = dep
            .spec
            .as_ref()
            .and_then(|s| s.template.metadata.as_ref())
            .and_then(|m| m.annotations.as_ref())
            .and_then(|a| a.get(TUNNEL_CONFIG_CHECKSUM))
            .cloned()
            .unwrap_or_default();

        if current_checksum != new_checksum {
            let dep_patch = serde_json::json!({
                "spec": {
                    "template": {
                        "metadata": {
                            "annotations": {
                                TUNNEL_CONFIG_CHECKSUM: new_checksum
                            }
                        }
                    }
                }
            });
            deploy_api
                .patch(
                    tunnel_name,
                    &PatchParams::apply("cloudflare-operator"),
                    &Patch::Merge(&dep_patch),
                )
                .await?;
            info!(tunnel = %tunnel_name, "deployment checksum updated from HTTPRoute, pods will restart");
        }
    }

    info!(
        tunnel = %tunnel_name,
        "configmap updated from combined TunnelBindings + HTTPRoutes"
    );
    Ok(())
}

/// Collect ingress rules from all HTTPRoutes that reference Gateways linked to a specific tunnel.
async fn collect_httproute_ingress_rules(
    k8s: &kube::Client,
    tunnel_name: &str,
    tunnel_kind: &str,
    cf_client: &CfClient,
) -> Result<Vec<UnvalidatedIngressRule>, Error> {
    // List all Gateways across all namespaces
    let gw_api: Api<Gateway> = Api::all(k8s.clone());
    let gateways = gw_api.list(&ListParams::default()).await?;

    // Find Gateways that reference this tunnel
    let mut managed_gateways: BTreeMap<(String, String), ()> = BTreeMap::new();
    for gw in &gateways.items {
        let annotations = gw.metadata.annotations.as_ref();
        let gw_tunnel_name = annotations.and_then(|a| a.get(ANNOTATION_TUNNEL_NAME));
        let gw_tunnel_kind = annotations
            .and_then(|a| a.get(ANNOTATION_TUNNEL_KIND))
            .map(|s| s.as_str())
            .unwrap_or("Tunnel");

        if gw_tunnel_name.map(|s| s.as_str()) == Some(tunnel_name) && gw_tunnel_kind == tunnel_kind
        {
            let gw_name = gw.name_any();
            let gw_ns = gw.metadata.namespace.clone().unwrap_or_default();
            managed_gateways.insert((gw_ns, gw_name), ());
        }
    }

    if managed_gateways.is_empty() {
        return Ok(Vec::new());
    }

    // List all HTTPRoutes across all namespaces
    let route_api: Api<HTTPRoute> = Api::all(k8s.clone());
    let routes = route_api.list(&ListParams::default()).await?;

    let svc_api_cache: std::cell::RefCell<BTreeMap<String, Api<Service>>> =
        std::cell::RefCell::new(BTreeMap::new());

    let mut rules = Vec::new();

    for route in &routes.items {
        let route_ns = route.metadata.namespace.as_deref().unwrap_or_default();

        // Check if any parent ref matches a managed gateway
        let is_managed = route.spec.parent_refs.iter().any(|p| {
            let pns = p.namespace.as_deref().unwrap_or(route_ns);
            managed_gateways.contains_key(&(pns.to_string(), p.name.clone()))
        });

        if !is_managed {
            continue;
        }

        let hostnames = route.spec.hostnames.as_deref().unwrap_or_default();

        for rule in &route.spec.rules {
            let target = resolve_backend_target_sync(k8s, &rule.backend_refs, route_ns).await;
            let path = extract_path_from_matches(&rule.matches);

            if hostnames.is_empty() {
                rules.push(UnvalidatedIngressRule {
                    hostname: Some(cf_client.domain.clone()),
                    service: target.clone(),
                    path: path.clone(),
                    origin_request: None,
                });
            } else {
                for hostname in hostnames {
                    rules.push(UnvalidatedIngressRule {
                        hostname: Some(hostname.clone()),
                        service: target.clone(),
                        path: path.clone(),
                        origin_request: None,
                    });
                }
            }
        }
    }

    Ok(rules)
}

async fn resolve_backend_target_sync(
    k8s: &kube::Client,
    backend_refs: &[HTTPBackendRef],
    route_ns: &str,
) -> String {
    if backend_refs.is_empty() {
        return "http_status:404".to_string();
    }

    let backend = &backend_refs[0];
    if backend.kind != "Service" && !backend.kind.is_empty() {
        return "http_status:404".to_string();
    }

    let svc_ns = backend.namespace.as_deref().unwrap_or(route_ns);
    let svc_api: Api<Service> = Api::namespaced(k8s.clone(), svc_ns);

    match svc_api.get(&backend.name).await {
        Ok(svc) => {
            let port = if let Some(p) = backend.port {
                p as i32
            } else {
                svc.spec
                    .as_ref()
                    .and_then(|s| s.ports.as_ref())
                    .and_then(|ports| ports.first())
                    .map(|p| p.port)
                    .unwrap_or(80)
            };

            let protocol = select_protocol_for_port(port);
            format!("{protocol}://{}.{svc_ns}.svc:{port}", backend.name)
        }
        Err(e) => {
            warn!(
                service = %backend.name,
                ns = %svc_ns,
                error = %e,
                "failed to get service for HTTPRoute backendRef"
            );
            "http_status:404".to_string()
        }
    }
}

// ── MD5 helper ──────────────────────────────────────────────────────────

fn compute_md5(input: &str) -> String {
    let mut hasher = Md5::new();
    hasher.update(input.as_bytes());
    let result = hasher.finalize();
    result.iter().map(|b| format!("{b:02x}")).collect()
}
