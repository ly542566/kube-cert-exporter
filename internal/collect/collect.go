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
	list, err := c.client.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "type=" + string(corev1.SecretTypeTLS),
	})
	if err != nil {
		return nil, fmt.Errorf("列出 Secrets 失败: %w", err)
	}

	res := Result{
		Certs:    make([]cert.CertInfo, 0, len(list.Items)),
		Failures: make([]Failure, 0, len(list.Items)),
	}

	// 这里故意串行，不要再改回 errgroup 并发。
	//
	// 理由不是「并发难写」，而是一笔账：x509 解析是几十微秒级的纯 CPU 操作，
	// 几百上千张证书串行也就几十毫秒；真正的耗时在一次全集群 List 上
	// （网络往返 + API Server 序列化整个 Secret 对象），通常是解析的十倍以上。
	// 8 路并发能省下的那点时间，放在 60s 的抓取间隔和 30s 的 scrapeTimeout 里
	// 完全观测不到，却要额外维护限流、预分配、竞争测试这一整套东西。
	// 如果将来真的变慢，先测出 List 和 Parse 各自的耗时，再决定往哪优化。
	for _, s := range list.Items {
		// 客户端兜底过滤：真实的 API Server 会按 fieldSelector 在服务端过滤，
		// 但 fake clientset（单测用）不实现 fieldSelector，
		// 会把所有类型的 Secret 都返回。留着这行，测试和生产行为才一致。
		if s.Type != corev1.SecretTypeTLS {
			continue
		}

		// 每轮探一次取消：Prometheus 中途断开（或 15s 超时）时立刻放弃，
		// 不能等整批解析完才发现对端已经走了。
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		info, err := cert.Parse(s.Data["tls.crt"])
		if err != nil {
			// 单张证书坏掉不该让整次采集失败，
			// 否则一个坏 secret 会让 exporter 看起来整体挂了，
			// 反而掩盖了真正的问题（该由 KubeCertificateParseFailure 报警）。
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
