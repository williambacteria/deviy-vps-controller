package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"deviy.cloud/controller/internal/controller"
)

var version = "0.2.0"

func main() {
	listen := flag.String("listen", "127.0.0.1:8088", "controller API listen address")
	nodesRaw := flag.String("nodes", os.Getenv("DEVIY_NODES"), "JSON array of nodes")
	agentCA := flag.String("agent-ca", os.Getenv("DEVIY_AGENT_CA"), "CA file used to verify agents")
	agentCert := flag.String("agent-cert", os.Getenv("DEVIY_AGENT_CERT"), "client certificate for controller -> agent mTLS")
	agentKey := flag.String("agent-key", os.Getenv("DEVIY_AGENT_KEY"), "client key for controller -> agent mTLS")
	listenCert := flag.String("listen-cert", os.Getenv("DEVIY_LISTEN_CERT"), "TLS certificate for controller HTTPS")
	listenKey := flag.String("listen-key", os.Getenv("DEVIY_LISTEN_KEY"), "TLS key for controller HTTPS")
	clientCA := flag.String("client-ca", os.Getenv("DEVIY_LISTEN_CLIENT_CA"), "CA file; when set, require client certs on controller HTTPS")
	allowRegister := flag.Bool("allow-register", os.Getenv("DEVIY_ALLOW_REGISTER") == "true", "allow new agents to register")
	registerToken := flag.String("register-token", os.Getenv("DEVIY_REGISTER_TOKEN"), "optional token required for agent registration")
	statePath := flag.String("state", envOr("DEVIY_CONTROLLER_STATE", "controller-state.json"), "controller state file for images and IP pools")
	adminUser := flag.String("admin-user", envOr("DEVIY_ADMIN_USER", "admin"), "controller web login username")
	adminPassword := flag.String("admin-password", os.Getenv("DEVIY_ADMIN_PASSWORD"), "controller web login password; enables login when set")
	apiToken := flag.String("api-token", os.Getenv("DEVIY_API_TOKEN"), "optional bearer token for machine API access")
	flag.Parse()

	nodes, err := controller.ParseNodes(*nodesRaw)
	if err != nil {
		log.Fatal(err)
	}

	clientTLS, err := controller.LoadClientTLS(*agentCA, *agentCert, *agentKey)
	if err != nil {
		log.Fatal(err)
	}
	httpClient := &http.Client{Timeout: 12 * time.Second}
	if clientTLS != nil {
		httpClient.Transport = &http.Transport{TLSClientConfig: clientTLS}
	}

	app := controller.NewWithOptions(nodes, httpClient, *allowRegister, *registerToken)
	app.ConfigureAuth(*adminUser, *adminPassword, *apiToken)
	if err := app.LoadState(*statePath); err != nil {
		log.Printf("controller state load failed: %v", err)
	}
	serverTLS, err := controller.LoadServerTLS(*listenCert, *listenKey, *clientCA)
	if err != nil {
		log.Fatal(err)
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         serverTLS,
	}

	scheme := "http"
	if serverTLS != nil {
		scheme = "https"
	}
	log.Printf("Deviy Controller %s listening on %s://%s (%d nodes)", version, scheme, *listen, len(nodes))
	if serverTLS != nil {
		if err := server.ListenAndServeTLS("", ""); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}
