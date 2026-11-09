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

func (c *Collector) Collect(ctx context.Context) ([]cert.CertInfo, error) {
	list, err := c.client.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("列出Secrets错误:%w", err)
	}

	var out []cert.CertInfo
	for _, s := range list.Items {
		if s.Type != corev1.SecretTypeTLS {
			continue
		}

		info, err := cert.Parse(s.Data["tls.crt"])
		if err != nil {
			continue
		}

		info.Namespace = s.Namespace
		info.SecretName = s.Name
		out = append(out, *info)
	}
	return out, nil
}
