use std::borrow::Cow;

use k8s_openapi::apimachinery::pkg::apis::meta::v1::ObjectMeta;
use kube::Resource;
use serde::{Deserialize, Serialize};

// ── GatewayClass ────────────────────────────────────────────────────────

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GatewayClass {
    pub metadata: ObjectMeta,
    pub spec: GatewayClassSpec,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<GatewayClassStatus>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GatewayClassSpec {
    pub controller_name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub parameters_ref: Option<ParametersRef>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ParametersRef {
    pub group: String,
    pub kind: String,
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub namespace: Option<String>,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GatewayClassStatus {
    #[serde(default)]
    pub conditions: Vec<GatewayCondition>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GatewayCondition {
    #[serde(rename = "type")]
    pub type_: String,
    pub status: String,
    pub reason: String,
    pub message: String,
    pub last_transition_time: String,
    #[serde(default)]
    pub observed_generation: i64,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct GatewayClassList {
    pub metadata: k8s_openapi::apimachinery::pkg::apis::meta::v1::ListMeta,
    pub items: Vec<GatewayClass>,
}

impl Resource for GatewayClass {
    type DynamicType = ();
    type Scope = k8s_openapi::ClusterResourceScope;

    fn kind(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("GatewayClass")
    }

    fn group(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("gateway.networking.k8s.io")
    }

    fn version(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("v1")
    }

    fn plural(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("gatewayclasses")
    }

    fn meta(&self) -> &ObjectMeta {
        &self.metadata
    }

    fn meta_mut(&mut self) -> &mut ObjectMeta {
        &mut self.metadata
    }

    fn api_version(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("gateway.networking.k8s.io/v1")
    }

    fn url_path(dt: &(), _namespace: Option<&str>) -> String {
        format!("apis/gateway.networking.k8s.io/v1/{}", Self::plural(dt))
    }
}

// ── Gateway ─────────────────────────────────────────────────────────────

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Gateway {
    pub metadata: ObjectMeta,
    pub spec: GatewaySpec,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<GatewayStatus>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GatewaySpec {
    pub gateway_class_name: String,
    #[serde(default)]
    pub listeners: Vec<Listener>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Listener {
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hostname: Option<String>,
    pub port: u16,
    pub protocol: String,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GatewayStatus {
    #[serde(default)]
    pub conditions: Vec<GatewayCondition>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct GatewayList {
    pub metadata: k8s_openapi::apimachinery::pkg::apis::meta::v1::ListMeta,
    pub items: Vec<Gateway>,
}

impl Resource for Gateway {
    type DynamicType = ();
    type Scope = k8s_openapi::NamespaceResourceScope;

    fn kind(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("Gateway")
    }

    fn group(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("gateway.networking.k8s.io")
    }

    fn version(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("v1")
    }

    fn plural(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("gateways")
    }

    fn meta(&self) -> &ObjectMeta {
        &self.metadata
    }

    fn meta_mut(&mut self) -> &mut ObjectMeta {
        &mut self.metadata
    }

    fn api_version(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("gateway.networking.k8s.io/v1")
    }

    fn url_path(dt: &(), namespace: Option<&str>) -> String {
        let prefix = "apis/gateway.networking.k8s.io/v1";
        match namespace {
            Some(ns) => format!("{prefix}/namespaces/{ns}/{}", Self::plural(dt)),
            None => format!("{prefix}/{}", Self::plural(dt)),
        }
    }
}

// ── HTTPRoute ───────────────────────────────────────────────────────────

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HTTPRoute {
    pub metadata: ObjectMeta,
    pub spec: HTTPRouteSpec,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<HTTPRouteStatus>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HTTPRouteSpec {
    #[serde(default)]
    pub parent_refs: Vec<ParentRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hostnames: Option<Vec<String>>,
    #[serde(default)]
    pub rules: Vec<HTTPRouteRule>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ParentRef {
    #[serde(default = "default_gateway_group")]
    pub group: String,
    #[serde(default = "default_gateway_kind")]
    pub kind: String,
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub namespace: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub section_name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub port: Option<u16>,
}

fn default_gateway_group() -> String {
    "gateway.networking.k8s.io".to_string()
}

fn default_gateway_kind() -> String {
    "Gateway".to_string()
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HTTPRouteRule {
    #[serde(default)]
    pub matches: Vec<HTTPRouteMatch>,
    #[serde(default)]
    pub backend_refs: Vec<HTTPBackendRef>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HTTPRouteMatch {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<HTTPPathMatch>,
    #[serde(default)]
    pub headers: Vec<HTTPHeaderMatch>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HTTPPathMatch {
    #[serde(rename = "type", default = "default_path_match_type")]
    pub type_: String,
    #[serde(default = "default_path_value")]
    pub value: String,
}

fn default_path_match_type() -> String {
    "PathPrefix".to_string()
}

fn default_path_value() -> String {
    "/".to_string()
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HTTPHeaderMatch {
    #[serde(rename = "type", default = "default_header_match_type")]
    pub type_: String,
    pub name: String,
    pub value: String,
}

fn default_header_match_type() -> String {
    "Exact".to_string()
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HTTPBackendRef {
    #[serde(default = "default_backend_group")]
    pub group: String,
    #[serde(default = "default_backend_kind")]
    pub kind: String,
    pub name: String,
    pub port: Option<u16>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub weight: Option<i32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub namespace: Option<String>,
}

fn default_backend_group() -> String {
    String::new()
}

fn default_backend_kind() -> String {
    "Service".to_string()
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HTTPRouteStatus {
    #[serde(default)]
    pub parents: Vec<RouteParentStatus>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RouteParentStatus {
    pub parent_ref: ParentRef,
    pub controller_name: String,
    #[serde(default)]
    pub conditions: Vec<GatewayCondition>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct HTTPRouteList {
    pub metadata: k8s_openapi::apimachinery::pkg::apis::meta::v1::ListMeta,
    pub items: Vec<HTTPRoute>,
}

impl Resource for HTTPRoute {
    type DynamicType = ();
    type Scope = k8s_openapi::NamespaceResourceScope;

    fn kind(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("HTTPRoute")
    }

    fn group(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("gateway.networking.k8s.io")
    }

    fn version(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("v1")
    }

    fn plural(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("httproutes")
    }

    fn meta(&self) -> &ObjectMeta {
        &self.metadata
    }

    fn meta_mut(&mut self) -> &mut ObjectMeta {
        &mut self.metadata
    }

    fn api_version(_dt: &()) -> Cow<'_, str> {
        Cow::Borrowed("gateway.networking.k8s.io/v1")
    }

    fn url_path(dt: &(), namespace: Option<&str>) -> String {
        let prefix = "apis/gateway.networking.k8s.io/v1";
        match namespace {
            Some(ns) => format!("{prefix}/namespaces/{ns}/{}", Self::plural(dt)),
            None => format!("{prefix}/{}", Self::plural(dt)),
        }
    }
}
