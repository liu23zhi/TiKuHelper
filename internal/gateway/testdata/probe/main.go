// Isolated deployment fixture: never used by the application binary.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "host-matrix":
			hostMatrix()
			return
		case "qr-fixture":
			qrFixture()
			return
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "occupy" {
		must(os.WriteFile("/tmp/gateway-port-block.pid", []byte(fmt.Sprint(os.Getpid())), 0600))
		listener, e := net.Listen("tcp", ":9043")
		must(e)
		for {
			connection, e := listener.Accept()
			if e != nil {
				return
			}
			_ = connection.Close()
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "cert" {
		if len(os.Args) != 9 {
			panic("cert dir stem names before after rsa|ec serial")
		}
		dir, stem := os.Args[2], os.Args[3]
		before, e := time.ParseDuration(os.Args[5])
		must(e)
		after, e := time.ParseDuration(os.Args[6])
		must(e)
		var private, public any
		if os.Args[7] == "rsa" {
			k, e := rsa.GenerateKey(rand.Reader, 2048)
			must(e)
			private = k
			public = &k.PublicKey
		} else {
			k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			must(e)
			private = k
			public = &k.PublicKey
		}
		serial, ok := new(big.Int).SetString(os.Args[8], 10)
		if !ok {
			panic("serial")
		}
		cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "fixture"}, DNSNames: strings.Split(os.Args[4], ","), NotBefore: time.Now().Add(before), NotAfter: time.Now().Add(after), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, e := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
		must(e)
		key, e := x509.MarshalPKCS8PrivateKey(private)
		must(e)
		must(os.WriteFile(filepath.Join(dir, stem+".certificate"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
		must(os.WriteFile(filepath.Join(dir, stem+".private"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600))
		return
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/exit" {
			os.Exit(7)
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, rw, e := w.(http.Hijacker).Hijack()
			must(e)
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			_ = rw.Flush()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"host": r.Host, "headers": r.Header, "listen": os.Getenv("TIKU_LISTEN_ADDRESS"), "gateway": os.Getenv("TIKU_INTERNAL_GATEWAY")})
	})
	must(http.ListenAndServe("127.0.0.1:8061", nil))
}
func must(err error) {
	if err != nil {
		panic(fmt.Sprintf("fixture: %v", err))
	}
}
