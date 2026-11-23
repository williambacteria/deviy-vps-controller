package main

import (
	"flag"
	"log"
	"net"
	"path/filepath"
	"strings"
	"time"

	"deviy.cloud/agent/internal/tlsutil"
)

func main() {
	out := flag.String("out", ".certs", "output directory")
	name := flag.String("name", "deviy", "certificate name prefix")
	hosts := flag.String("hosts", "localhost,127.0.0.1", "comma separated DNS names / IP addresses")
	days := flag.Int("days", 365, "certificate validity in days")
	flag.Parse()

	dnsNames, ips := splitHosts(*hosts)
	ca, err := tlsutil.GenerateCA(*name + " Root CA")
	if err != nil {
		log.Fatal(err)
	}

	if err := tlsutil.WriteBundle(*out, "ca", ca.CertPEM, ca.KeyPEM); err != nil {
		log.Fatal(err)
	}

	validity := time.Duration(*days) * 24 * time.Hour
	bundles := []struct {
		name       string
		commonName string
		serverAuth bool
		clientAuth bool
	}{
		{name: "agent", commonName: *name + " Agent", serverAuth: true, clientAuth: true},
		{name: "controller-server", commonName: *name + " Controller", serverAuth: true},
		{name: "controller", commonName: *name + " Controller Client", clientAuth: true},
		{name: "php-client", commonName: *name + " PHP Client", clientAuth: true},
	}

	for _, bundle := range bundles {
		certPEM, keyPEM, err := ca.Issue(tlsutil.IssueOptions{
			CommonName: bundle.commonName,
			DNSNames:   dnsNames,
			IPs:        ips,
			ServerAuth: bundle.serverAuth,
			ClientAuth: bundle.clientAuth,
			Validity:   validity,
		})
		if err != nil {
			log.Fatal(err)
		}
		if err := tlsutil.WriteBundle(*out, bundle.name, certPEM, keyPEM); err != nil {
			log.Fatal(err)
		}
	}

	abs, _ := filepath.Abs(*out)
	log.Printf("证书已生成: %s", abs)
	log.Printf("  ca.crt / ca.key")
	log.Printf("  agent.crt / agent.key                    (Agent HTTPS 服务端)")
	log.Printf("  controller-server.crt / .key             (Controller HTTPS 服务端)")
	log.Printf("  controller.crt / controller.key          (Controller 访问 Agent 的客户端证书)")
	log.Printf("  php-client.crt / php-client.key          (PHP 访问 Controller 的客户端证书)")
	log.Printf("  SANs: %s", strings.Join(append(dnsNames, ipStrings(ips)...), ", "))
}

func splitHosts(raw string) ([]string, []net.IP) {
	var dnsNames []string
	var ips []net.IP
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if ip := net.ParseIP(item); ip != nil {
			ips = append(ips, ip)
			continue
		}
		dnsNames = append(dnsNames, item)
	}

	return dnsNames, ips
}

func ipStrings(ips []net.IP) []string {
	result := make([]string, 0, len(ips))
	for _, ip := range ips {
		result = append(result, ip.String())
	}

	return result
}
