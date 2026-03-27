use cloudflare_operator::crds::{ClusterTunnel, Tunnel, TunnelBinding};
use kube::CustomResourceExt;

fn main() {
    let tunnel_crd = serde_yaml::to_string(&Tunnel::crd()).unwrap();
    print!("{tunnel_crd}");

    println!("---");

    let cluster_tunnel_crd = serde_yaml::to_string(&ClusterTunnel::crd()).unwrap();
    print!("{cluster_tunnel_crd}");

    println!("---");

    let tunnel_binding_crd = serde_yaml::to_string(&TunnelBinding::crd()).unwrap();
    print!("{tunnel_binding_crd}");
}
