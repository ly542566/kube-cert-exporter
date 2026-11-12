package collect

import (
	"context"
	"fmt"

	"github.com/lart5/kube-cert-exporter/internal/cert"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// defaultWorkers 是并发解析的默认上限。
// 为什么不是「有多少开多少个」：x509 解析是纯 CPU 活，
// 协程数远超核数只会让调度器空转，还要额外承担内存峰值。
// 8 是个稳妥值——单次抓取通常是几十到几百张证书，
// 8 路并发已经能把解析耗时压到接近 API 请求本身的耗时。
const defaultWorkers = 8

// Option 是函数式选项，用于在不破坏 New 签名的前提下扩展参数。
// 这种写法在 gRPC、zap、prometheus 里到处都是，
// 好处是调用方只写自己关心的那几个，以后加参数也不用改所有调用点。
type Option func(*Collector)

// WithWorkers 覆盖默认并发度。n <= 0 时忽略，避免把并发度设成 0 死锁。
func WithWorkers(n int) Option {
	return func(c *Collector) {
		if n > 0 {
			c.workers = n
		}
	}
}

type Collector struct {
	client  kubernetes.Interface
	workers int
}

func New(client kubernetes.Interface, opts ...Option) *Collector {
	c := &Collector{client: client, workers: defaultWorkers}
	for _, opt := range opts {
		opt(c)
	}
	return c
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

	// 关键设计：按下标预分配，而不是让多个 goroutine 往同一个 slice 上 append。
	// append 在并发下会竞争底层数组——轻则丢数据，重则因为扩容时的
	// 指针替换产生数据竞争（go test -race 会直接报错）。
	// 按下标写入不同元素是安全的：每个位置只有一个 goroutine 碰。
	// 用指针切片而非值切片，是为了事后能区分「没填」和「填了零值」。
	certs := make([]*cert.CertInfo, len(list.Items))
	failures := make([]*Failure, len(list.Items))

	// WithContext 派生的 gctx 会随 ctx 取消而取消。
	// 这样 Prometheus 中途断开（或 15s 超时）时，
	// 还没开始的 goroutine 会立刻放弃，不用把整批解析完再扔。
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.workers)

	for i, s := range list.Items {
		i, s := i, s // Go 1.22 起循环变量已是每轮独立，这行是给旧版本留的保险

		// 客户端兜底过滤：真实的 API Server 会按 fieldSelector 在服务端过滤，
		// 但 fake clientset（单测用）不实现 fieldSelector，
		// 会把所有类型的 Secret 都返回。留着这行，测试和生产行为才一致。
		if s.Type != corev1.SecretTypeTLS {
			continue
		}

		g.Go(func() error {
			// 排队等槽位期间可能已经被取消，先探一次，省掉一次无用的解析
			if err := gctx.Err(); err != nil {
				return nil // 不返回 err：取消不是「某张证书失败」，交给外层统一判断
			}

			info, err := cert.Parse(s.Data["tls.crt"])
			if err != nil {
				// 单张证书坏掉不该让整次采集失败，
				// 否则一个坏 secret 会让 exporter 看起来整体挂了，
				// 反而掩盖了真正的问题（该由 KubeCertificateParseFailure 报警）。
				failures[i] = &Failure{
					Namespace:  s.Namespace,
					SecretName: s.Name,
					Err:        err,
				}
				return nil
			}
			info.Namespace = s.Namespace
			info.SecretName = s.Name
			certs[i] = info
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// 到这里所有 goroutine 都已结束，这里读 certs/failures 没有竞争。
	// 若 ctx 已取消，数组里会残留大量 nil——此时绝不能当成结果返回，
	// 否则零值时间的证书会算出荒谬的剩余天数（负一千多万天）污染指标。
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res := Result{
		Certs:    make([]cert.CertInfo, 0, len(certs)),
		Failures: make([]Failure, 0, len(failures)),
	}
	for _, p := range certs {
		if p != nil {
			res.Certs = append(res.Certs, *p)
		}
	}
	for _, p := range failures {
		if p != nil {
			res.Failures = append(res.Failures, *p)
		}
	}
	return &res, nil
}
