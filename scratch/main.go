package main

import (
	"fmt"
	"time"
)

func main() {
	cn := "localhost"
	ns := "default"
	secret := "demo-tls"
	notAfter := time.Date(2027, 11, 7, 14, 42, 55, 0, time.UTC)

	fmt.Println("# HELP kube_cert_expiry_timestamp_seconds 证书到期时间（Unix 时间戳）")
	fmt.Println("# TYPE kube_cert_expiry_timestamp_seconds gauge")
	fmt.Printf("kube_cert_expiry_timestamp_seconds{cn=%q,issuer=%q,namespace=%q,secret=%q} %v\n",
		cn, cn, ns, secret, float64(notAfter.Unix()))

	fmt.Println()
	fmt.Printf("// Unix 秒 = %d，换算回时间 = %s\n",
		notAfter.Unix(), time.Unix(notAfter.Unix(), 0).UTC().Format(time.RFC3339))
}
