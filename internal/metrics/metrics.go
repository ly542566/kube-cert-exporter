package metrics

import (
	"github.com/lart5/kube-cert-exporter/internal/collect"
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	expiry   *prometheus.GaugeVec
	failures prometheus.Gauge
}

func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		expiry: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kube_cert_expiry_timestamp_seconds",
			Help: "证书到期时间（Unix 时间戳）",
		}, []string{"namespace", "secret", "cn", "issuer"}),

		failures: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kube_cert_parse_failures",
			Help: "本次采集中解析失败的 Secret 数量",
		}),
	}

	reg.MustRegister(m.expiry, m.failures)
	return m
}

// 用指针接收者：值接收者会复制整个结构体，
// 一旦以后加了 sync.Mutex 之类的字段，复制锁会让 go vet 直接报错，
// 而且复制出来的副本和原件是两套状态，Reset 就失效了。
func (m *Metrics) Update(res *collect.Result) {
	m.expiry.Reset()

	for _, c := range res.Certs {
		m.expiry.WithLabelValues(c.Namespace, c.SecretName, c.CommonName, c.Issuer).
			Set(float64(c.NotAfter.Unix()))
	}

	m.failures.Set(float64(len(res.Failures)))
}
