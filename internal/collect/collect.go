package collect

import (
	"context"
	"fmt"

	"github.com/lart5/kube-cert-exporter/internal/cert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Collector struct {
	client kubernetes.Interface
}

func New(client kubernetes.Interface) *Collector {
	return &Collector{client: client}
}

type Failure struct {
	Namespace  string
	SecretName string
	Err        error
}

func (f Failure) String() string {
	return fmt.Sprintf("%s/%s: %v", f.Namespace, f.SecretName, f.Err)
}

type Result struct {
	Certs    []cert.CertInfo
	Failures []Failure
}

func (c *Collector) Collect(ctx context.Context) (*Result, error) {
	list, err := c.client.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("列出Secrets错误:%w", err)
	}

	res := Result{}
	for _, s := range list.Items {
		if s.Type != corev1.SecretTypeTLS {
			continue
		}

		info, err := cert.Parse(s.Data["tls.crt"])
		if err != nil {
			res.Failures = append(res.Failures, Failure{
				Namespace:  s.Namespace,
				SecretName: s.Name,
				Err:        err,
			})
			continue
		}
		info.Namespace = s.Namespace
		info.SecretName = s.Name
		res.Certs = append(res.Certs, *info)
	}
	return &res, nil
}
