package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"deviy.cloud/agent/internal/heartbeat"
	"deviy.cloud/agent/internal/server"
	"deviy.cloud/agent/internal/tlsutil"
)

var version = "0.1.0"

func main() {
	listen := flag.String("listen", "127.0.0.1:8443", "agent web listen address")
	provider := flag.String("provider", defaultProvider(), "virtualization provider: pve or hyperv")
	nodeName := flag.String("name", defaultName(), "node display name")
	region := flag.String("region", "香港", "node region")
	token := flag.String("token", os.Getenv("DEVIY_AGENT_TOKEN"), "optional bearer token for API access")
	mode := flag.String("mode", env("DEVIY_MODE", "mock"), "provider mode: mock, pve, hyperv, kvm")
	pveURL := flag.String("pve-url", env("DEVIY_PVE_URL", ""), "Proxmox VE API base URL, e.g. https://10.0.0.10:8006")
	pveToken := flag.String("pve-token", env("DEVIY_PVE_TOKEN", ""), "Proxmox VE API token, e.g. user@pve!deviy=secret")
	pveNode := flag.String("pve-node", env("DEVIY_PVE_NODE", "pve"), "Proxmox VE node name")
	pveStorage := flag.String("pve-storage", env("DEVIY_PVE_STORAGE", "local-lvm"), "Proxmox VE storage")
	pveBridge := flag.String("pve-bridge", env("DEVIY_PVE_BRIDGE", "vmbr0"), "Proxmox VE bridge")
	pveInsecure := flag.Bool("pve-insecure", env("DEVIY_PVE_INSECURE", "false") == "true", "skip PVE TLS verification (self-signed)")
	hypervSwitch := flag.String("hyperv-switch", env("DEVIY_HYPERV_SWITCH", "Default Switch"), "Hyper-V virtual switch")
	hypervStorage := flag.String("hyperv-storage", env("DEVIY_HYPERV_STORAGE", `C:\ProgramData\Deviy\VHDs`), "Hyper-V VHDX directory")
	kvmURI := flag.String("kvm-uri", env("DEVIY_KVM_URI", "qemu:///system"), "libvirt connection URI for Ubuntu KVM")
	kvmStorage := flag.String("kvm-storage", env("DEVIY_KVM_STORAGE", "/var/lib/libvirt/images/deviy"), "KVM qcow2 storage directory")
	kvmNetwork := flag.String("kvm-network", env("DEVIY_KVM_NETWORK", "default"), "libvirt NAT network name")
	kvmBridge := flag.String("kvm-bridge", env("DEVIY_KVM_BRIDGE", "virbr0"), "KVM public bridge name")
	kvmISODir := flag.String("kvm-iso-dir", env("DEVIY_KVM_ISO_DIR", "/var/lib/libvirt/images/iso"), "KVM ISO directory")
	kvmBackupDir := flag.String("kvm-backup-dir", env("DEVIY_KVM_BACKUP_DIR", "/var/lib/libvirt/images/backup"), "KVM backup directory")
	networkBackend := flag.String("network-backend", env("DEVIY_NETWORK_BACKEND", "mock"), "network backend: mock, nftables, windows")
	wanInterface := flag.String("wan-interface", env("DEVIY_WAN_INTERFACE", "eth0"), "WAN interface for nftables masquerade")
	natCIDR := flag.String("nat-cidr", env("DEVIY_NAT_CIDR", "10.0.0.0/24"), "private NAT CIDR")
	allowNetworkApply := flag.Bool("allow-network-apply", env("DEVIY_ALLOW_NETWORK_APPLY", "false") == "true", "allow real network changes (requires admin/root)")
	metricsBackend := flag.String("metrics-backend", env("DEVIY_METRICS_BACKEND", "mock"), "metrics backend: mock, proc, windows")
	metricsInterface := flag.String("metrics-interface", env("DEVIY_METRICS_INTERFACE", ""), "network interface to collect counters from")
	tlsCert := flag.String("tls-cert", env("DEVIY_TLS_CERT", ""), "TLS certificate file for HTTPS")
	tlsKey := flag.String("tls-key", env("DEVIY_TLS_KEY", ""), "TLS private key file for HTTPS")
	tlsClientCA := flag.String("tls-client-ca", env("DEVIY_TLS_CLIENT_CA", ""), "CA file; when set, require and verify client certificates (mTLS)")
	nodeID := flag.String("node-id", env("DEVIY_NODE_ID", ""), "stable node id used by controller registration")
	controllerURL := flag.String("controller-url", env("DEVIY_CONTROLLER_URL", ""), "controller base URL for heartbeat")
	controllerCA := flag.String("controller-ca", env("DEVIY_CONTROLLER_CA", ""), "CA file for controller mTLS")
	controllerCert := flag.String("controller-cert", env("DEVIY_CONTROLLER_CERT", ""), "client certificate for controller mTLS")
	controllerKey := flag.String("controller-key", env("DEVIY_CONTROLLER_KEY", ""), "client key for controller mTLS")
	registerToken := flag.String("register-token", env("DEVIY_REGISTER_TOKEN", ""), "optional controller registration token")
	heartbeatInterval := flag.Duration("heartbeat-interval", 15*time.Second, "heartbeat interval")
	flag.Parse()

	if *nodeID == "" {
		*nodeID = defaultNodeID(*nodeName)
	}

	tlsConfig, err := tlsutil.ServerConfig(*tlsCert, *tlsKey, *tlsClientCA)
	if err != nil {
		log.Fatal(err)
	}
	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
	}

	app := server.New(server.Config{
		Version:           version,
		Provider:          *provider,
		NodeID:            *nodeID,
		NodeName:          *nodeName,
		Region:            *region,
		Token:             *token,
		Mode:              *mode,
		PVEURL:            *pveURL,
		PVEToken:          *pveToken,
		PVENode:           *pveNode,
		PVEStorage:        *pveStorage,
		PVEBridge:         *pveBridge,
		PVEInsecure:       *pveInsecure,
		HyperVSwitch:      *hypervSwitch,
		HyperVStorage:     *hypervStorage,
		KVMURI:            *kvmURI,
		KVMStorage:        *kvmStorage,
		KVMNetwork:        *kvmNetwork,
		KVMBridge:         *kvmBridge,
		KVMISODir:         *kvmISODir,
		KVMBackupDir:      *kvmBackupDir,
		NetworkBackend:    *networkBackend,
		WANInterface:      *wanInterface,
		NATCIDR:           *natCIDR,
		AllowNetworkApply: *allowNetworkApply,
		MetricsBackend:    *metricsBackend,
		MetricsInterface:  *metricsInterface,
		TLSConfig:         tlsConfig,
	})

	log.Printf("Deviy Agent %s listening on %s://%s (provider=%s mode=%s node_id=%s)", version, scheme, *listen, *provider, *mode, *nodeID)
	if *controllerURL != "" {
		publicURL := scheme + "://" + *listen
		client, err := heartbeat.New(heartbeat.Config{
			ControllerURL: *controllerURL,
			CAFile:        *controllerCA,
			CertFile:      *controllerCert,
			KeyFile:       *controllerKey,
			NodeID:        *nodeID,
			Name:          *nodeName,
			Provider:      *provider,
			Region:        *region,
			PublicURL:     publicURL,
			Version:       version,
			RegisterToken: *registerToken,
			Interval:      *heartbeatInterval,
		})
		if err != nil {
			log.Fatal(err)
		}
		go client.Run(context.Background(), app.HeartbeatSnapshot)
		log.Printf("Agent heartbeat enabled: %s -> %s", *nodeID, *controllerURL)
	}

	if err := app.ListenAndServe(*listen); err != nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}

func defaultNodeID(name string) string {
	if name == "" {
		return "deviy-node"
	}

	result := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			result = append(result, r+('a'-'A'))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			result = append(result, r)
		default:
			result = append(result, '-')
		}
	}

	return string(result)
}

func defaultProvider() string {
	if os.PathSeparator == '\\' {
		return "hyperv"
	}

	return "pve"
}

func defaultName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "deviy-node"
	}

	return host
}
