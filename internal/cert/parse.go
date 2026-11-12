package cert

import (
	"crypto/x509"  // 解析 X.509 结构，拿 NotAfter / SAN / Issuer
	"encoding/pem" // 把 base64 的 PEM 文本切成「类型 + DER 字节」
	"errors"
	"fmt"
	"time"
)

type CertInfo struct {
	Namespace  string
	SecretName string
	CommonName string
	DNSNames   []string
	Issuer     string
	NotBefore  time.Time
	NotAfter   time.Time
}

func Parse(data []byte) (*CertInfo, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("没有找到 PEM 块：数据可能不是证书，或者被其他格式包着")
	}

	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("PEM 块类型是 %q，不是 CERTIFICATE", block.Type)
	}

	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析x.509失败:%w", err)
	}

	// CN 回退到第一个 SAN。
	// 现在大量证书（cert-manager、服务网格、Let's Encrypt）都不填 Subject.CN，
	// 身份信息全放在 SAN 里。只读 CN 的话标签会是空字符串，
	// 指标能查出来但看不出是哪个域名，等于白采。
	cn := leaf.Subject.CommonName
	if cn == "" && len(leaf.DNSNames) > 0 {
		cn = leaf.DNSNames[0]
	}

	return &CertInfo{
		CommonName: cn,
		DNSNames:   leaf.DNSNames,
		Issuer:     leaf.Issuer.CommonName,
		NotBefore:  leaf.NotBefore,
		NotAfter:   leaf.NotAfter,
	}, nil
}

func (c *CertInfo) DaysLeft(now time.Time) int {
	return int(c.NotAfter.Sub(now).Hours() / 24)
}

// Expired 判断证书在 now 时刻是否已经过期。
// 刻意不用 DaysLeft(now) < 0 来判断：那个值会被整数截断，
// 「还剩 3 小时」会算成 0 天，用它判断过期会漏掉最危险的临界情况。
func (c *CertInfo) Expired(now time.Time) bool {
	return c.NotAfter.Before(now)
}
