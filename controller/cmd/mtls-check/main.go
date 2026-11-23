package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"deviy.cloud/controller/internal/controller"
)

func main() {
	url := flag.String("url", "https://127.0.0.1:8089/health", "URL to request")
	ca := flag.String("ca", "", "CA file")
	cert := flag.String("cert", "", "client certificate")
	key := flag.String("key", "", "client key")
	flag.Parse()

	tlsConfig, err := controller.LoadClientTLS(*ca, *cert, *key)
	if err != nil {
		log.Fatal(err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	if tlsConfig != nil {
		client.Transport = &http.Transport{TLSClientConfig: tlsConfig}
	}

	resp, err := client.Get(*url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Printf("status=%d\n%s\n", resp.StatusCode, body)
}
